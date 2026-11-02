package coordinator

import (
	"context"
	"slices"
	"testing"

	"github.com/chazu/mu/internal/plugin"
)

func TestResolveEnvPassthrough(t *testing.T) {
	dir := t.TempDir()
	spec := plugin.ActionSpec{ID: "a", Command: []string{"true"}, EnvPassthrough: []string{"HOME", "GOCACHE"}}
	actions, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, dir, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(actions[0].EnvPassthrough, []string{"HOME", "GOCACHE"}) {
		t.Fatalf("EnvPassthrough = %v", actions[0].EnvPassthrough)
	}

	for _, bad := range [][]string{{""}, {"A=B"}, {"HOME", "HOME"}} {
		spec.EnvPassthrough = bad
		if _, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, dir, nil, nil); err == nil {
			t.Errorf("env_passthrough %q: expected error", bad)
		}
	}
}
