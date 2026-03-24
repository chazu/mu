package dag

import (
	"sort"
	"testing"
)

func TestBuildEnvNil(t *testing.T) {
	// nil env map = inherit parent environment (backward compat).
	got := buildEnv(nil)
	if got != nil {
		t.Errorf("buildEnv(nil) = %v, want nil", got)
	}
}

func TestBuildEnvEmptyMap(t *testing.T) {
	// Explicit empty map = clean environment (no inherited vars).
	got := buildEnv(map[string]string{})
	if got == nil {
		t.Fatal("buildEnv(empty map) = nil, want non-nil empty slice")
	}
	if len(got) != 0 {
		t.Errorf("buildEnv(empty map) = %v, want empty slice", got)
	}
}

func TestBuildEnvWithVars(t *testing.T) {
	got := buildEnv(map[string]string{"FOO": "bar"})
	if len(got) != 1 {
		t.Fatalf("buildEnv returned %d entries, want 1", len(got))
	}
	if got[0] != "FOO=bar" {
		t.Errorf("buildEnv = %v, want [FOO=bar]", got)
	}
}

func TestBuildEnvMultipleVars(t *testing.T) {
	got := buildEnv(map[string]string{"A": "1", "B": "2", "C": "3"})
	if len(got) != 3 {
		t.Fatalf("buildEnv returned %d entries, want 3", len(got))
	}
	sort.Strings(got)
	want := []string{"A=1", "B=2", "C=3"}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("got[%d] = %q, want %q", i, got[i], w)
		}
	}
}
