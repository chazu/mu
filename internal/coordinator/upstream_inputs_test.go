package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
)

// TestBuild_DownstreamCacheKeyTracksUpstreamOutput encodes mu-bei.1: a pure
// action consuming another action's output must miss the cache when that
// output's content changes. Previously the upstream input hashed as a zero
// placeholder, so the second build served a stale out.txt from cache.
func TestBuild_DownstreamCacheKeyTracksUpstreamOutput(t *testing.T) {
	root := t.TempDir()
	store := newTestStore(t)
	newCoordinator := func() *Coordinator {
		return &Coordinator{
			ProjectRoot: root,
			Config: &config.ProjectConfig{
				Targets: []config.Target{
					{
						Name: "//a", Toolchain: "shell", Sources: []string{"in.txt"},
						Config: map[string]any{
							"command": []any{"sh", "-c", "cp in.txt mid.txt"},
							"outputs": []any{"mid.txt"},
							"impure":  false,
						},
					},
					{
						Name: "//b", Toolchain: "shell", Sources: []string{"mid.txt"}, Deps: []string{"//a"},
						Config: map[string]any{
							"command": []any{"sh", "-c", "cp mid.txt out.txt"},
							"outputs": []any{"out.txt"},
							"impure":  false,
						},
					},
				},
			},
			Store:   store,
			Workers: 1,
		}
	}
	build := func(input string) (string, *BuildResult) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, "in.txt"), []byte(input), 0o644); err != nil {
			t.Fatal(err)
		}
		result, err := newCoordinator().Build(context.Background(), []string{"//a", "//b"})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		if result.Failed != 0 {
			t.Fatalf("Build failed: %+v", result)
		}
		out, err := os.ReadFile(filepath.Join(root, "out.txt"))
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(out)), result
	}

	if got, _ := build("one\n"); got != "one" {
		t.Fatalf("first build out.txt = %q, want one", got)
	}
	got, result := build("two\n")
	if got != "two" {
		t.Fatalf("second build out.txt = %q, want two (stale cache hit; cached=%d)", got, result.Cached)
	}
	if result.Cached != 0 {
		t.Fatalf("second build cached %d actions, want 0", result.Cached)
	}
	// Unchanged input: both actions hit.
	if _, result := build("two\n"); result.Cached != 2 {
		t.Fatalf("third build cached %d actions, want 2", result.Cached)
	}
}
