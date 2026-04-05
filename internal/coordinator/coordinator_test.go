package coordinator

import (
	"testing"

	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/dag"
	"github.com/chau/mu/internal/plugin"
)

// ---------- resolveTargets tests ----------

func makeCoordinator(targets []config.Target) *Coordinator {
	return &Coordinator{
		Config: &config.ProjectConfig{Targets: targets},
	}
}

func TestResolveTargets_SingleNoDepends(t *testing.T) {
	c := makeCoordinator([]config.Target{
		{Name: "//lib/a", Toolchain: "go"},
	})
	got, err := c.resolveTargets([]string{"//lib/a"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 1 || got[0].Name != "//lib/a" {
		t.Fatalf("expected [//lib/a], got %v", names(got))
	}
}

func TestResolveTargets_WithDeps(t *testing.T) {
	c := makeCoordinator([]config.Target{
		{Name: "//app", Toolchain: "go", Deps: []string{"//lib/a", "//lib/b"}},
		{Name: "//lib/a", Toolchain: "go", Deps: []string{"//lib/b"}},
		{Name: "//lib/b", Toolchain: "go"},
	})
	got, err := c.resolveTargets([]string{"//app"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 targets, got %d: %v", len(got), names(got))
	}
	// //lib/b must come before //lib/a, and both before //app.
	idx := make(map[string]int)
	for i, tgt := range got {
		idx[tgt.Name] = i
	}
	if idx["//lib/b"] >= idx["//lib/a"] {
		t.Errorf("//lib/b (idx %d) should come before //lib/a (idx %d)", idx["//lib/b"], idx["//lib/a"])
	}
	if idx["//lib/a"] >= idx["//app"] {
		t.Errorf("//lib/a (idx %d) should come before //app (idx %d)", idx["//lib/a"], idx["//app"])
	}
}

func TestResolveTargets_MissingTarget(t *testing.T) {
	c := makeCoordinator([]config.Target{
		{Name: "//lib/a", Toolchain: "go"},
	})
	_, err := c.resolveTargets([]string{"//does/not/exist"})
	if err == nil {
		t.Fatal("expected error for missing target")
	}
}

func TestResolveTargets_MissingDep(t *testing.T) {
	c := makeCoordinator([]config.Target{
		{Name: "//app", Toolchain: "go", Deps: []string{"//missing"}},
	})
	_, err := c.resolveTargets([]string{"//app"})
	if err == nil {
		t.Fatal("expected error for missing dependency")
	}
}

func TestResolveTargets_CycleDetection(t *testing.T) {
	c := makeCoordinator([]config.Target{
		{Name: "//a", Toolchain: "go", Deps: []string{"//b"}},
		{Name: "//b", Toolchain: "go", Deps: []string{"//c"}},
		{Name: "//c", Toolchain: "go", Deps: []string{"//a"}},
	})
	_, err := c.resolveTargets([]string{"//a"})
	if err == nil {
		t.Fatal("expected cycle detection error")
	}
}

// ---------- BuildResult counting tests ----------

func TestBuildResultCounting(t *testing.T) {
	er := &dag.ExecuteResult{
		Completed: []dag.ActionStatus{
			{ID: "a", Cached: false},
			{ID: "b", Cached: true},
			{ID: "c", Cached: true},
			{ID: "d", Cached: false},
		},
		Failed: []dag.ActionStatus{
			{ID: "e", ExitCode: 1},
		},
		Cancelled: []string{"f", "g"},
	}

	br := buildResultFrom(er)

	if br.Completed != 2 {
		t.Errorf("Completed: got %d, want 2", br.Completed)
	}
	if br.Cached != 2 {
		t.Errorf("Cached: got %d, want 2", br.Cached)
	}
	if br.Failed != 1 {
		t.Errorf("Failed: got %d, want 1", br.Failed)
	}
	if br.Cancelled != 2 {
		t.Errorf("Cancelled: got %d, want 2", br.Cancelled)
	}
}

func TestBuildResultCounting_Empty(t *testing.T) {
	er := &dag.ExecuteResult{}
	br := buildResultFrom(er)
	if br.Completed != 0 || br.Cached != 0 || br.Failed != 0 || br.Cancelled != 0 {
		t.Errorf("expected all zeros, got %+v", br)
	}
}

// ---------- prefixActions tests ----------

func TestPrefixActions(t *testing.T) {
	actions := []plugin.ActionSpec{
		{ID: "compile", DependsOn: nil},
		{ID: "link", DependsOn: []string{"compile"}},
	}
	prefixActions("//lib/auth", actions)

	if actions[0].ID != "//lib/auth:compile" {
		t.Errorf("got %q, want //lib/auth:compile", actions[0].ID)
	}
	if actions[1].ID != "//lib/auth:link" {
		t.Errorf("got %q, want //lib/auth:link", actions[1].ID)
	}
	if actions[1].DependsOn[0] != "//lib/auth:compile" {
		t.Errorf("DependsOn got %q, want //lib/auth:compile", actions[1].DependsOn[0])
	}
}

// ---------- parseSecretRef tests ----------

func TestParseSecretRef(t *testing.T) {
	tests := []struct {
		ref        string
		wantScheme string
		wantPath   string
		wantOk     bool
	}{
		{"pass:deploy/token", "pass", "deploy/token", true},
		{"vault:secrets/api/key", "vault", "secrets/api/key", true},
		{"1password:op://vault/item", "1password", "op://vault/item", true},
		{"nocolon", "", "", false},
		{":nocheme", "", "", false},
		{"empty:", "", "", false},
		{"", "", "", false},
	}

	for _, tt := range tests {
		scheme, path, ok := parseSecretRef(tt.ref)
		if ok != tt.wantOk {
			t.Errorf("parseSecretRef(%q) ok = %v, want %v", tt.ref, ok, tt.wantOk)
			continue
		}
		if !ok {
			continue
		}
		if scheme != tt.wantScheme {
			t.Errorf("parseSecretRef(%q) scheme = %q, want %q", tt.ref, scheme, tt.wantScheme)
		}
		if path != tt.wantPath {
			t.Errorf("parseSecretRef(%q) path = %q, want %q", tt.ref, path, tt.wantPath)
		}
	}
}

// helpers

func names(targets []config.Target) []string {
	out := make([]string, len(targets))
	for i, t := range targets {
		out[i] = t.Name
	}
	return out
}
