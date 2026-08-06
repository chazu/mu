package coordinator

import (
	"context"
	"testing"

	"github.com/chazu/mu/internal/config"
)

// TestPlan_SealedInputsAttachToPithPlanAction is a regression guard: a target
// with a pith `plan` program emits bare action specs, and the coordinator must
// attach the target's sealed_inputs (and modes) to those emitted actions.
// Without this, a pith body can never receive a sealed input (secret/get and
// env-mode injection both depend on the action carrying SealedInputs).
func TestPlan_SealedInputsAttachToPithPlanAction(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{
					Name:             "//inventory/gitlab",
					SealedInputs:     map[string]string{"GITLAB_TOKEN": "env:GITLAB_TOKEN"},
					SealedInputModes: map[string]string{"GITLAB_TOKEN": "env"},
					Plan: []any{
						map[string]any{
							"id":      "fetch",
							"body":    []any{"'GITLAB_TOKEN", "secret/get"},
							"outputs": []any{},
						},
						"action/emit",
					},
				},
			},
			Plugins: []config.PluginDef{
				{Name: "env", Command: mockPluginCommand(t, "mock_env_secret.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//inventory/gitlab"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	var fetch *actionView
	for _, a := range plan.Graph.Actions() {
		if a.ID == "//inventory/gitlab:fetch" {
			fetch = &actionView{
				sealedInputs:     a.SealedInputs,
				sealedInputModes: a.SealedInputModes,
			}
		}
	}
	if fetch == nil {
		t.Fatalf("emitted action //inventory/gitlab:fetch not found in graph")
	}
	if got := fetch.sealedInputs["GITLAB_TOKEN"]; got != "env:GITLAB_TOKEN" {
		t.Errorf("sealed input not attached to pith-plan action: got %q, want %q",
			got, "env:GITLAB_TOKEN")
	}
	if got := fetch.sealedInputModes["GITLAB_TOKEN"]; got != "env" {
		t.Errorf("sealed input mode not attached: got %q, want %q", got, "env")
	}
}

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
				SealedOutputs:     map[string]string{"TOKEN": "fake:apps/token"},
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

// TestPlan_ExplicitActionSealedInputsNotOverridden verifies that if an emitted
// action declares its own sealed_inputs, the coordinator does NOT clobber them
// with the target-level set (the convenience only fills in the empty case).
func TestPlan_ExplicitActionSealedInputsNotOverridden(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{
					Name:         "//multi",
					SealedInputs: map[string]string{"TARGET_LEVEL": "env:TARGET_LEVEL"},
					Plan: []any{
						map[string]any{
							"id":            "explicit",
							"body":          []any{"'ACTION_LEVEL", "secret/get"},
							"outputs":       []any{},
							"sealed_inputs": map[string]any{"ACTION_LEVEL": "env:ACTION_LEVEL"},
						},
						"action/emit",
					},
				},
			},
			Plugins: []config.PluginDef{
				{Name: "env", Command: mockPluginCommand(t, "mock_env_secret.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//multi"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	for _, a := range plan.Graph.Actions() {
		if a.ID != "//multi:explicit" {
			continue
		}
		if _, leaked := a.SealedInputs["TARGET_LEVEL"]; leaked {
			t.Errorf("target-level sealed input clobbered an action that set its own: %v", a.SealedInputs)
		}
		if got := a.SealedInputs["ACTION_LEVEL"]; got != "env:ACTION_LEVEL" {
			t.Errorf("action's own sealed input lost: got %q", got)
		}
		return
	}
	t.Fatal("emitted action //multi:explicit not found")
}

// actionView is a tiny copy of the fields under test, kept local so the test
// does not depend on the full dag.Action surface.
type actionView struct {
	sealedInputs     map[string]string
	sealedInputModes map[string]string
}
