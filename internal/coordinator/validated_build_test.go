package coordinator

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/config"
)

func TestBuildValidatedRejectsBeforeExecution(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	coordinator := &Coordinator{
		ProjectRoot: root,
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name: "//app", Toolchain: "cfg", Config: map[string]any{"actions": []any{map[string]any{
					"id": "run", "command": []any{"touch", marker}, "outputs": []any{}, "impure": true,
				}}},
			}},
			Plugins: []config.PluginDef{{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")}},
		},
		Store: newTestStore(t), Workers: 1,
	}
	want := errors.New("plan not approved")
	if _, err := coordinator.BuildValidated(context.Background(), []string{"//app"}, func(*PlanResult) error { return want }); !errors.Is(err, want) {
		t.Fatalf("BuildValidated error = %v, want %v", err, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("validation rejection executed action; stat error = %v", err)
	}
}

func TestBuildValidatedExecutesTheValidatedPlanResult(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "executed")
	coordinator := &Coordinator{
		ProjectRoot: root,
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name: "//app", Toolchain: "cfg", Config: map[string]any{"actions": []any{map[string]any{
					"id": "run", "command": []any{"touch", marker}, "outputs": []any{}, "impure": true,
				}}},
			}},
			Plugins: []config.PluginDef{{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")}},
		},
		Store: newTestStore(t), Workers: 1,
	}
	var validated *PlanResult
	result, err := coordinator.BuildValidated(context.Background(), []string{"//app"}, func(plan *PlanResult) error {
		validated = plan
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if validated == nil || result.Graph != validated.Graph {
		t.Fatal("executed graph was not the graph passed to validation")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("validated action did not execute: %v", err)
	}
}

func TestBuildValidatedRetainsResolvedProviderArtifact(t *testing.T) {
	root := t.TempDir()
	providerPath := filepath.Join(root, "provider.sh")
	provider := `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"discover"'*) echo '{"name":"fake","version":"1.0.0","protocol_version":1,"consumes":[],"produces":[],"capabilities":["discover","resolve_secret"]}' ;;
    *'"method":"resolve_secret"'*) echo '{"value":"planned-provider-value"}' ;;
  esac
done
`
	if err := os.WriteFile(providerPath, []byte(provider), 0o755); err != nil {
		t.Fatal(err)
	}
	coordinator := &Coordinator{
		ProjectRoot: root,
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{
				{Name: "fake", Script: providerPath},
				{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")},
			},
			Targets: []config.Target{{
				Name:             "//app",
				Toolchain:        "cfg",
				SealedInputs:     map[string]string{"TOKEN": "fake:apps/token"},
				SealedInputModes: map[string]string{"TOKEN": "env"},
				Config: map[string]any{"actions": []any{map[string]any{
					"id": "read", "command": []any{"sh", "-c", `test "$TOKEN" = planned-provider-value`},
					"outputs": []any{}, "impure": true,
					"sealed_inputs":      map[string]any{"TOKEN": "fake:apps/token"},
					"sealed_input_modes": map[string]any{"TOKEN": "env"},
				}}},
			}},
		},
		Store: newTestStore(t), Workers: 1,
	}
	result, err := coordinator.BuildValidated(context.Background(), []string{"//app"}, func(plan *PlanResult) error {
		var provider *PluginIdentity
		for i := range plan.Plugins {
			if plan.Plugins[i].Name == "fake" {
				provider = &plan.Plugins[i]
			}
		}
		if provider == nil || provider.Digest == "" {
			t.Fatalf("provider identity = %#v, want a content-addressed row for fake", plan.Plugins)
		}
		return os.WriteFile(providerPath, []byte("#!/bin/sh\nexit 99\n"), 0o755)
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Failed != 0 || result.Completed != 1 {
		t.Fatalf("result = %#v, want execution through retained provider artifact", result)
	}
}
