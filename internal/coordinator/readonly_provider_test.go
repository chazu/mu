package coordinator

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/sdk/muplugin"
)

// TestReadOnlySDKProviderRejectsSealedOutputAtPlan registers a Go SDK
// secret provider whose backend implements only Resolve, then plans a
// secret-gen target that would store through it. Because SecretPlugin does
// not advertise store_secret for such a backend, planning must fail before
// any action runs.
func TestReadOnlySDKProviderRejectsSealedOutputAtPlan(t *testing.T) {
	t.Setenv("MU_READONLY_PROVIDER_HELPER", "1")
	command := []string{os.Args[0], "-test.run=^TestReadOnlyProviderHelperProcess$"}
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{{Name: "ro", Command: command}},
			Secrets: &config.SecretsConfig{WritableRefs: []string{"ro:*"}},
			Targets: []config.Target{{
				Name:              "//secrets/minted",
				Toolchain:         "secret-gen",
				SealedOutputs:     map[string]string{"VALUE": "ro:dest"},
				SealedOutputModes: map[string]string{"VALUE": "create_if_absent"},
				Config: map[string]any{
					"derivation": []any{"echo", "abc"},
				},
			}},
		},
		Store: newTestStore(t), Workers: 1,
	}

	_, err := c.Plan(context.Background(), []string{"//secrets/minted"})
	if err == nil {
		t.Fatal("Plan succeeded, want store_secret capability error")
	}
	if !strings.Contains(err.Error(), "does not support store_secret") {
		t.Fatalf("Plan error = %v, want it to mention store_secret", err)
	}
}

type readOnlyResolver struct{}

func (readOnlyResolver) Resolve(_ context.Context, ref string) (string, error) {
	return "value-of-" + ref, nil
}

// TestReadOnlyProviderHelperProcess runs a read-only SDK secret provider as
// a subprocess for TestReadOnlySDKProviderRejectsSealedOutputAtPlan.
func TestReadOnlyProviderHelperProcess(t *testing.T) {
	if os.Getenv("MU_READONLY_PROVIDER_HELPER") != "1" {
		return
	}
	p := muplugin.SecretPlugin("ro", "test", readOnlyResolver{})
	if err := p.Run(context.Background(), os.Stdin, os.Stdout); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
