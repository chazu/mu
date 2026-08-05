package coordinator

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

func TestStrictSealedRoutingFakeProviderEndToEnd(t *testing.T) {
	stateDir := t.TempDir()
	storedPath := filepath.Join(stateDir, "stored-secret")
	modePath := filepath.Join(stateDir, "stored-mode")
	t.Setenv("MU_STRICT_SECRET_HELPER", "1")
	t.Setenv("MU_STRICT_INPUT_SECRET", "runtime-only-value")
	t.Setenv("MU_STRICT_STORED_PATH", storedPath)
	t.Setenv("MU_STRICT_MODE_PATH", modePath)
	command := []string{os.Args[0], "-test.run=^TestStrictSecretPluginHelperProcess$"}
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{{Name: "fake", Command: command}},
			Secrets: &config.SecretsConfig{WritableRefs: []string{"fake:*"}},
			Targets: []config.Target{{
				Name: "//strict/sealed", Toolchain: "fake", SealedRouting: "strict",
				SealedInputs:      map[string]string{"IN": "fake:source"},
				SealedInputModes:  map[string]string{"IN": "env"},
				SealedOutputs:     map[string]string{"OUT": "fake:destination"},
				SealedOutputModes: map[string]string{"OUT": "create_if_absent"},
			}},
		},
		Store: newTestStore(t), Workers: 1,
	}
	var stdout bytes.Buffer
	c.SubprocessStdout = &stdout

	plan, err := c.Plan(context.Background(), []string{"//strict/sealed"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if _, err := os.Stat(storedPath); !os.IsNotExist(err) {
		t.Fatalf("planning stored a secret: %v", err)
	}
	actions := plan.Graph.Actions()
	if len(actions) != 1 || actions[0].SealedInputs["IN"] != "fake:source" || actions[0].SealedOutputs["OUT"] != "fake:destination" {
		t.Fatalf("strict routed action = %#v", actions)
	}

	if _, err := c.Execute(context.Background(), plan); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	stored, err := os.ReadFile(storedPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(stored) != "runtime-only-value" {
		t.Fatalf("stored value = %q", stored)
	}
	mode, err := os.ReadFile(modePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(mode) != "create_if_absent" {
		t.Fatalf("stored mode = %q", mode)
	}
	if stdout.String() != "" {
		t.Fatalf("secret action wrote stdout: %q", stdout.String())
	}
}

// TestStrictSecretPluginHelperProcess is an NDJSON planner/provider launched as
// a subprocess by TestStrictSealedRoutingFakeProviderEndToEnd. It copies target
// declarations to one explicit action and records provider writes only in the
// test's private directory.
func TestStrictSecretPluginHelperProcess(t *testing.T) {
	if os.Getenv("MU_STRICT_SECRET_HELPER") != "1" {
		return
	}
	encoder := json.NewEncoder(os.Stdout)
	scanner := bufio.NewScanner(os.Stdin)
	for scanner.Scan() {
		var request plugin.Request
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			os.Exit(2)
		}
		switch request.Method {
		case "discover":
			_ = encoder.Encode(plugin.DiscoverResponse{
				Name: "fake", Version: "test", ProtocolVersion: plugin.ProtocolVersion,
				Capabilities: []string{"discover", "plan", "resolve_secret", "store_secret"},
			})
		case "plan":
			_ = encoder.Encode(plugin.PlanResponse{Actions: []plugin.ActionSpec{{
				ID: "copy", Command: []string{"sh", "-c", `printf '%s' "$IN" > "$MU_SEALED_OUT_DIR/OUT"`},
				SealedInputs: request.Target.SealedInputs, SealedInputModes: request.Target.SealedInputModes,
				SealedOutputs: request.Target.SealedOutputs, SealedOutputModes: request.Target.SealedOutputModes,
			}}})
		case "resolve_secret":
			_ = encoder.Encode(plugin.ResolveSecretResponse{Value: os.Getenv("MU_STRICT_INPUT_SECRET")})
		case "store_secret":
			if err := os.WriteFile(os.Getenv("MU_STRICT_STORED_PATH"), []byte(request.SecretValue), 0o600); err != nil {
				os.Exit(3)
			}
			if err := os.WriteFile(os.Getenv("MU_STRICT_MODE_PATH"), []byte(request.SecretMode), 0o600); err != nil {
				os.Exit(4)
			}
			_ = encoder.Encode(plugin.StoreSecretResponse{})
		default:
			os.Exit(5)
		}
	}
	if err := scanner.Err(); err != nil {
		os.Exit(6)
	}
}
