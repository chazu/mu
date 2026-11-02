package coordinator

import (
	"context"
	"os"
	"testing"

	"github.com/chazu/mu/internal/config"
)

func TestPlan_TargetSealedOutputModeAttachesToSingleAction(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name:              "//secrets/token",
				Toolchain:         "mock",
				SealedOutputs:     map[string]string{"TOKEN": "fake:apps/token"},
				SealedOutputModes: map[string]string{"TOKEN": "create_if_absent"},
			}},
			Plugins: []config.PluginDef{{
				Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh"),
			}, {
				Name: "fake", Command: mockPluginCommand(t, "mock_provider.sh"),
			}},
		},
		Store: newTestStore(t),
	}

	plan, err := c.Plan(context.Background(), []string{"//secrets/token"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	actions := plan.Graph.Actions()
	if len(actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(actions))
	}
	if got := actions[0].SealedOutputs["TOKEN"]; got != "fake:apps/token" {
		t.Fatalf("sealed output TOKEN = %q", got)
	}
	if got := actions[0].SealedOutputModes["TOKEN"]; got != "create_if_absent" {
		t.Fatalf("sealed output mode TOKEN = %q, want create_if_absent", got)
	}
}

func TestPlan_PluginReceivesTargetSealedOutputModes(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name:              "//secrets/token",
				Toolchain:         "sealed-output",
				SealedOutputs:     map[string]string{"TOKEN": "sealed-output:apps/token"},
				SealedOutputModes: map[string]string{"TOKEN": "create_if_absent"},
			}},
			Plugins: []config.PluginDef{{
				Name: "sealed-output", Command: mockPluginCommand(t, "mock_plugin_sealed_output_modes.sh"),
			}},
		},
		Store: newTestStore(t),
	}

	plan, err := c.Plan(context.Background(), []string{"//secrets/token"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	actions := plan.Graph.Actions()
	if len(actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(actions))
	}
	if got := actions[0].SealedOutputModes["TOKEN"]; got != "create_if_absent" {
		t.Fatalf("plugin-planned sealed output mode TOKEN = %q, want create_if_absent", got)
	}
}

func TestPlan_StrictSealedInputResolutionWaitsUntilExecute(t *testing.T) {
	const variable = "MU_TEST_STRICT_PLAN_SECRET"
	previous, existed := os.LookupEnv(variable)
	requireNoError(t, os.Unsetenv(variable))
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(variable, previous)
		} else {
			_ = os.Unsetenv(variable)
		}
	})
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{Targets: []config.Target{{
			Name:             "//strict",
			SealedInputs:     map[string]string{"TOKEN": "env:" + variable},
			SealedInputModes: map[string]string{"TOKEN": "env"},
			Toolchain:        "cfg",
			Config: map[string]any{"actions": []any{
				map[string]any{
					"id":                 "read",
					"command":            []any{"sh", "-c", `test -n "$TOKEN"`},
					"outputs":            []any{},
					"sealed_inputs":      map[string]any{"TOKEN": "env:" + variable},
					"sealed_input_modes": map[string]any{"TOKEN": "env"},
					"impure":             true,
				},
			}},
		}},
			Plugins: []config.PluginDef{{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")}},
		},
		Store: newTestStore(t), Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//strict"})
	if err != nil {
		t.Fatalf("Plan resolved a secret value: %v", err)
	}
	result, err := c.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.ExecResult.Failed) != 1 || !contains(result.ExecResult.Failed[0].Err.Error(), variable) {
		t.Fatalf("Execute failures = %#v, want missing execute-time env secret", result.ExecResult.Failed)
	}
	requireNoError(t, os.Setenv(variable, "runtime-only-value"))
	if _, err := c.Execute(context.Background(), plan); err != nil {
		t.Fatalf("Execute with secret: %v", err)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
