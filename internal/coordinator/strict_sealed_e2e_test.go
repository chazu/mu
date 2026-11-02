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
	runSealedRoutingEndToEnd(t, "explicit")
}

// TestSealedRoutingSingleActionAutoClaim covers the one routing convenience: a
// lone action that makes no claims of its own is given the target's sealed
// maps, and the build reads and stores through the provider.
func TestSealedRoutingSingleActionAutoClaim(t *testing.T) {
	runSealedRoutingEndToEnd(t, "bare")
}

func TestSealedRoutingRejectsUndeclaredActionClaim(t *testing.T) {
	t.Setenv("MU_STRICT_SECRET_HELPER", "1")
	t.Setenv("MU_STRICT_HELPER_PLAN", "undeclared")
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{{Name: "fake", Command: strictHelperCommand()}},
			Targets: []config.Target{{Name: "//undeclared", Toolchain: "fake"}},
		},
		Store: newTestStore(t), Workers: 1,
	}
	_, err := c.Plan(context.Background(), []string{"//undeclared"})
	if err == nil || !contains(err.Error(), `claims undeclared input "STOLEN"`) {
		t.Fatalf("Plan error = %v, want undeclared sealed input claim", err)
	}
}

func TestSealedRoutingMultiActionPlanDoesNotInherit(t *testing.T) {
	t.Setenv("MU_STRICT_SECRET_HELPER", "1")
	t.Setenv("MU_STRICT_HELPER_PLAN", "two-bare")
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{{Name: "fake", Command: strictHelperCommand()}},
			Targets: []config.Target{{
				Name: "//multi", Toolchain: "fake",
				SealedInputs: map[string]string{"IN": "fake:source"},
			}},
		},
		Store: newTestStore(t), Workers: 1,
	}
	_, err := c.Plan(context.Background(), []string{"//multi"})
	if err == nil || !contains(err.Error(), `declared input "IN" is not claimed`) {
		t.Fatalf("Plan error = %v, want unclaimed input on a multi-action plan", err)
	}
}

func strictHelperCommand() []string {
	return []string{os.Args[0], "-test.run=^TestStrictSecretPluginHelperProcess$"}
}

func runSealedRoutingEndToEnd(t *testing.T, planShape string) {
	stateDir := t.TempDir()
	storedPath := filepath.Join(stateDir, "stored-secret")
	modePath := filepath.Join(stateDir, "stored-mode")
	t.Setenv("MU_STRICT_SECRET_HELPER", "1")
	t.Setenv("MU_STRICT_HELPER_PLAN", planShape)
	t.Setenv("MU_STRICT_INPUT_SECRET", "runtime-only-value")
	t.Setenv("MU_STRICT_STORED_PATH", storedPath)
	t.Setenv("MU_STRICT_MODE_PATH", modePath)
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{{Name: "fake", Command: strictHelperCommand()}},
			Secrets: &config.SecretsConfig{WritableRefs: []string{"fake:*"}},
			Targets: []config.Target{{
				Name: "//strict/sealed", Toolchain: "fake",
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

	result, err := c.Execute(context.Background(), plan)
	if err != nil {
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
	manifest, err := json.Marshal(NewManifest(result, result.ExecResult, result.Targets, 0))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"runtime-only-value", "fake:source", "fake:destination"} {
		if bytes.Contains(manifest, []byte(forbidden)) {
			t.Fatalf("build manifest contains sealed material %q: %s", forbidden, manifest)
		}
	}
}

// TestStrictSecretPluginHelperProcess is an NDJSON planner/provider launched as
// a subprocess by the sealed-routing tests. MU_STRICT_HELPER_PLAN picks the plan
// shape: by default it copies target declarations to one explicit action;
// "bare" emits that action with no claims; "two-bare" adds a second unclaimed
// action; "undeclared" claims a ref the target never declared. Provider writes
// land only in the test's private directory.
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
			copyAction := plugin.ActionSpec{
				ID: "copy", Command: []string{"sh", "-c", `printf '%s' "$IN" > "$MU_SEALED_OUT_DIR/OUT"`},
			}
			actions := []plugin.ActionSpec{copyAction}
			switch os.Getenv("MU_STRICT_HELPER_PLAN") {
			case "bare":
			case "two-bare":
				actions = append(actions, plugin.ActionSpec{ID: "other", Command: []string{"true"}})
			case "undeclared":
				actions[0].SealedInputs = map[string]string{"STOLEN": "fake:anything"}
			default:
				actions[0].SealedInputs = request.Target.SealedInputs
				actions[0].SealedInputModes = request.Target.SealedInputModes
				actions[0].SealedOutputs = request.Target.SealedOutputs
				actions[0].SealedOutputModes = request.Target.SealedOutputModes
			}
			_ = encoder.Encode(plugin.PlanResponse{Actions: actions})
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
