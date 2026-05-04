package coordinator

import (
	"context"
	"strings"
	"testing"

	"github.com/chau/mu/internal/config"
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
				Name:      "//secrets/admin",
				Toolchain: "secret-gen",
				Config: map[string]any{
					"ref":        "pass:registry/admin",
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

func TestPlan_SealedOutputAllowedByPolicy(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name:      "//secrets/admin",
				Toolchain: "secret-gen",
				Config: map[string]any{
					"ref":        "pass:registry/admin",
					"derivation": []any{"true"},
				},
			}},
			Secrets: &config.SecretsConfig{
				WritableRefs: []string{"pass:registry/*"},
			},
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

func TestPlan_DenyAllPolicyBlocksWrites(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name:      "//secrets/admin",
				Toolchain: "secret-gen",
				Config: map[string]any{
					"ref":        "pass:registry/admin",
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
