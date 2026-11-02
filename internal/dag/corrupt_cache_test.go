package dag_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/dag"
)

// A cached output whose blob rotted on disk must not be restored: the store's
// verified read fails, and the action re-executes instead.
func TestCorruptCachedBlobReexecutes(t *testing.T) {
	store, err := cas.NewDiskStore(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	out := filepath.Join(workDir, "out.txt")
	run := func() dag.ActionStatus {
		t.Helper()
		g := dag.NewGraph()
		if err := g.AddAction(&dag.Action{
			ID: "A", Command: []string{"sh", "-c", "printf fresh > " + out},
			Outputs: []string{out}, Env: map[string]string{}, WorkDir: workDir,
		}); err != nil {
			t.Fatal(err)
		}
		res, err := (&dag.Executor{Store: store, Workers: 1}).Execute(context.Background(), g)
		if err != nil || len(res.Failed) > 0 || len(res.Completed) != 1 {
			t.Fatalf("execute: %+v, %v", res, err)
		}
		return res.Completed[0]
	}

	first := run()
	if first.Cached {
		t.Fatal("cold build hit cache")
	}
	blob, err := store.BlobPath(first.Outputs[out])
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blob, []byte("rotten"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(out); err != nil {
		t.Fatal(err)
	}

	second := run()
	if second.Cached {
		t.Fatal("restored from a corrupt blob instead of re-executing")
	}
	if got, err := os.ReadFile(out); err != nil || string(got) != "fresh" {
		t.Fatalf("output after re-execute: %q, %v", got, err)
	}
	if !run().Cached {
		t.Fatal("cache not repaired by the re-execution")
	}
}
