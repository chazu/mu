package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/dag"
)

// TestPlan_ActionKeyIndependentOfCheckoutPath encodes mu-bei.5: the same
// project checked out at two different paths plans identical action keys, so
// the checkouts (or machines) share cache entries.
func TestPlan_ActionKeyIndependentOfCheckoutPath(t *testing.T) {
	key := func(root string) string {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(root, "pkg"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "pkg", "in.txt"), []byte("same"), 0o644); err != nil {
			t.Fatal(err)
		}
		c := &Coordinator{
			ProjectRoot: root,
			Config: &config.ProjectConfig{Targets: []config.Target{{
				Name: "//pkg", Toolchain: "shell", Sources: []string{"pkg/in.txt"},
				Config: map[string]any{
					"command":  []any{"sh", "-c", "cp in.txt out.txt"},
					"outputs":  []any{"out.txt"},
					"work_dir": "pkg",
					"impure":   false,
				},
			}}},
			Store:   newTestStore(t),
			Workers: 1,
		}
		plan, err := c.Plan(context.Background(), []string{"//pkg"})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		actions := plan.Graph.Actions()
		if len(actions) != 1 {
			t.Fatalf("planned %d actions, want 1", len(actions))
		}
		return dag.ComputeActionKey(actions[0]).Digest.String()
	}
	if first, second := key(t.TempDir()), key(t.TempDir()); first != second {
		t.Fatalf("checkouts at different paths planned different keys: %s vs %s", first, second)
	}
}
