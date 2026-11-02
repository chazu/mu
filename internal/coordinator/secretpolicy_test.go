package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
)

func TestSecretWritePolicy_NilAllowsAll(t *testing.T) {
	p, err := newSecretWritePolicy(nil)
	if err != nil {
		t.Fatalf("newSecretWritePolicy: %v", err)
	}
	if p != nil {
		t.Fatalf("expected nil policy when no SecretsConfig")
	}
	// nil-receiver Allow must be permissive
	if !p.Allow("pass:anything/whatever") {
		t.Error("nil policy must allow everything")
	}
}

func TestSecretWritePolicy_NilWritableRefsAllowsAll(t *testing.T) {
	p, err := newSecretWritePolicy(&config.SecretsConfig{WritableRefs: nil})
	if err != nil {
		t.Fatalf("newSecretWritePolicy: %v", err)
	}
	if p != nil {
		t.Error("nil WritableRefs (field omitted) should produce nil policy")
	}
}

func TestSecretWritePolicy_EmptyDeniesAll(t *testing.T) {
	p, err := newSecretWritePolicy(&config.SecretsConfig{WritableRefs: []string{}})
	if err != nil {
		t.Fatalf("newSecretWritePolicy: %v", err)
	}
	if p == nil {
		t.Fatal("explicit [] WritableRefs must produce a non-nil policy")
	}
	if p.Allow("pass:anything") {
		t.Error("empty allow-list must deny everything")
	}
	if !strings.Contains(p.Description(), "deny-all") {
		t.Errorf("Description = %q, want it to mention deny-all", p.Description())
	}
}

func TestSecretWritePolicy_PatternMatching(t *testing.T) {
	p, err := newSecretWritePolicy(&config.SecretsConfig{
		WritableRefs: []string{
			"pass:registry/*",
			"pass:loosh/secret-thing",
		},
	})
	if err != nil {
		t.Fatalf("newSecretWritePolicy: %v", err)
	}

	cases := []struct {
		ref   string
		allow bool
	}{
		{"pass:registry/admin", true},
		{"pass:registry/htpasswd", true},
		{"pass:registry/sub/admin", false}, // * does not span /
		{"pass:loosh/secret-thing", true},
		{"pass:loosh/other", false},
		{"vault:registry/admin", false}, // wrong scheme
		{"pass:personal/everything-important", false},
	}
	for _, tc := range cases {
		if got := p.Allow(tc.ref); got != tc.allow {
			t.Errorf("Allow(%q) = %v, want %v", tc.ref, got, tc.allow)
		}
	}
}

// TestPlan_SealedOutputDeniedByPolicy verifies that a secret-gen target
// whose ref is not covered by secrets.writable_refs is rejected at
// plan time, before any provider plugin gets started.
func TestPlan_SealedOutputDeniedByPolicy(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name:              "//secrets/admin",
				Toolchain:         "secret-gen",
				SealedOutputs:     map[string]string{"VALUE": "pass:registry/admin"},
				SealedOutputModes: map[string]string{"VALUE": "create_if_absent"},
				Config: map[string]any{
					"derivation": []any{"true"},
				},
			}},
			Secrets: &config.SecretsConfig{
				WritableRefs: []string{"pass:loosh/*"},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	_, err := c.Plan(context.Background(), []string{"//secrets/admin"})
	if err == nil {
		t.Fatal("expected Plan to fail when ref is not in writable_refs")
	}
	if !strings.Contains(err.Error(), "writable_refs") {
		t.Errorf("error = %v, want it to mention writable_refs", err)
	}
}

// A secret-gen target names its destination only in sealed_outputs; with
// no sealed_output_modes it plans with the create_if_absent default and
// passes strict routing, and building routes the value to that ref.
func TestBuild_SecretGenSealedOutputsOnly(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name: "//secrets/admin", Toolchain: "secret-gen",
				SealedOutputs: map[string]string{"VALUE": "pass:registry/admin"},
				Config:        map[string]any{"derivation": []any{"echo", "hunter2"}},
			}},
			Secrets: &config.SecretsConfig{WritableRefs: []string{"pass:registry/*"}},
			Plugins: []config.PluginDef{{Name: "pass", Command: mockPluginCommand(t, "mock_provider.sh")}},
		},
		Store: newTestStore(t), Workers: 1,
	}
	plan, err := c.Plan(context.Background(), []string{"//secrets/admin"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	a := plan.Graph.Action("//secrets/admin:gen")
	if a.SealedOutputs["VALUE"] != "pass:registry/admin" || a.SealedOutputModes["VALUE"] != "create_if_absent" {
		t.Fatalf("claims = %v / %v, want VALUE -> pass:registry/admin create_if_absent", a.SealedOutputs, a.SealedOutputModes)
	}
	if got := c.Config.Targets[0].SealedOutputModes; len(got) != 0 {
		t.Errorf("config target modes mutated: %v", got)
	}
	result, err := c.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if n := len(result.ExecResult.Failed); n != 0 {
		t.Fatalf("failed actions: %v", result.ExecResult.Failed[0].Err)
	}
}

func TestPlan_SealedOutputAllowedByPolicy(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name:              "//secrets/admin",
				Toolchain:         "secret-gen",
				SealedOutputs:     map[string]string{"VALUE": "pass:registry/admin"},
				SealedOutputModes: map[string]string{"VALUE": "create_if_absent"},
				Config: map[string]any{
					"derivation": []any{"true"},
				},
			}},
			Secrets: &config.SecretsConfig{
				WritableRefs: []string{"pass:registry/*"},
			},
			Plugins: []config.PluginDef{{Name: "pass", Command: mockPluginCommand(t, "mock_provider.sh")}},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//secrets/admin"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.SecretWritePolicy == nil {
		t.Fatal("expected SecretWritePolicy to be populated")
	}
	if !plan.SecretWritePolicy.Allow("pass:registry/admin") {
		t.Error("policy must allow pass:registry/admin under pass:registry/*")
	}
}

func TestExecuteRechecksSealedOutputPolicyAgainstTamperedPlan(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name: "//secrets/admin", Toolchain: "secret-gen",
				SealedOutputs:     map[string]string{"VALUE": "pass:registry/admin"},
				SealedOutputModes: map[string]string{"VALUE": "create_if_absent"},
				Config:            map[string]any{"derivation": []any{"echo", "hunter2"}},
			}},
			Secrets: &config.SecretsConfig{WritableRefs: []string{"pass:registry/*"}},
			Plugins: []config.PluginDef{{Name: "pass", Command: mockPluginCommand(t, "mock_provider.sh")}},
		},
		Store: newTestStore(t), Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//secrets/admin"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	for _, action := range plan.Graph.Actions() {
		for name := range action.SealedOutputs {
			action.SealedOutputs[name] = "pass:personal/admin"
		}
	}
	result, err := c.Execute(context.Background(), plan)
	if err != nil {
		t.Fatalf("Execute orchestration: %v", err)
	}
	if len(result.ExecResult.Failed) != 1 {
		t.Fatalf("failed actions = %#v, want one write-policy failure", result.ExecResult.Failed)
	}
	if !strings.Contains(result.ExecResult.Failed[0].Err.Error(), "writable_refs") {
		t.Fatalf("action error = %v, want write-policy rejection", result.ExecResult.Failed[0].Err)
	}
}

func TestPlan_DenyAllPolicyBlocksWrites(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name:              "//secrets/admin",
				Toolchain:         "secret-gen",
				SealedOutputs:     map[string]string{"VALUE": "pass:registry/admin"},
				SealedOutputModes: map[string]string{"VALUE": "create_if_absent"},
				Config: map[string]any{
					"derivation": []any{"true"},
				},
			}},
			Secrets: &config.SecretsConfig{
				// Explicit empty list = lockdown
				WritableRefs: []string{},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	_, err := c.Plan(context.Background(), []string{"//secrets/admin"})
	if err == nil {
		t.Fatal("expected Plan to fail under deny-all policy")
	}
	if !strings.Contains(err.Error(), "deny-all") {
		t.Errorf("error = %v, want it to mention deny-all", err)
	}
}

func TestSecretWritePolicy_InvalidPatternRejected(t *testing.T) {
	_, err := newSecretWritePolicy(&config.SecretsConfig{
		WritableRefs: []string{"pass:[unterminated"},
	})
	if err == nil {
		t.Fatal("expected error for invalid pattern")
	}
	if !strings.Contains(err.Error(), "invalid pattern") {
		t.Errorf("error = %q, want it to mention invalid pattern", err)
	}
}
