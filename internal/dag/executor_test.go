package dag_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/chau/mu/internal/dag"
)

func TestImpureActionSkipsCacheLookup(t *testing.T) {
	workDir := t.TempDir()
	outA := filepath.Join(workDir, "a.txt")

	store := newStore(t)
	makeGraph := func() *dag.Graph {
		g := dag.NewGraph()
		_ = g.AddAction(&dag.Action{
			ID:      "A",
			Command: []string{"sh", "-c", "echo impure > " + outA},
			Outputs: []string{outA},
			Impure:  true,
			WorkDir: workDir,
		})
		return g
	}

	exec := &dag.Executor{Store: store, Workers: 1}

	// First run.
	res1, err := exec.Execute(context.Background(), makeGraph())
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if len(res1.Completed) != 1 {
		t.Fatalf("first run: completed %d, want 1", len(res1.Completed))
	}
	if res1.Completed[0].Cached {
		t.Error("first run should not be cached")
	}

	// Second run: impure action must NOT be cached.
	res2, err := exec.Execute(context.Background(), makeGraph())
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if len(res2.Completed) != 1 {
		t.Fatalf("second run: completed %d, want 1", len(res2.Completed))
	}
	if res2.Completed[0].Cached {
		t.Error("impure action should never be cached")
	}
}

func TestImpureActionSkipsCacheStorage(t *testing.T) {
	workDir := t.TempDir()
	outA := filepath.Join(workDir, "a.txt")

	store := newStore(t)

	// Run an impure action.
	g1 := dag.NewGraph()
	_ = g1.AddAction(&dag.Action{
		ID:      "A",
		Command: []string{"sh", "-c", "echo impure > " + outA},
		Outputs: []string{outA},
		Impure:  true,
		WorkDir: workDir,
	})

	exec := &dag.Executor{Store: store, Workers: 1}
	res1, err := exec.Execute(context.Background(), g1)
	if err != nil {
		t.Fatalf("Execute impure: %v", err)
	}
	if len(res1.Completed) != 1 {
		t.Fatalf("completed %d, want 1", len(res1.Completed))
	}

	// Now run the same action as pure — it should NOT get a cache hit,
	// proving the impure run did not store a result.
	g2 := dag.NewGraph()
	_ = g2.AddAction(&dag.Action{
		ID:      "A",
		Command: []string{"sh", "-c", "echo impure > " + outA},
		Outputs: []string{outA},
		Impure:  false,
		WorkDir: workDir,
	})
	res2, err := exec.Execute(context.Background(), g2)
	if err != nil {
		t.Fatalf("Execute pure: %v", err)
	}
	if len(res2.Completed) != 1 {
		t.Fatalf("completed %d, want 1", len(res2.Completed))
	}
	if res2.Completed[0].Cached {
		t.Error("pure run after impure run should not be cached (impure should not store)")
	}
}

func TestPureActionStillCaches(t *testing.T) {
	workDir := t.TempDir()
	outA := filepath.Join(workDir, "a.txt")

	store := newStore(t)
	makeGraph := func() *dag.Graph {
		g := dag.NewGraph()
		_ = g.AddAction(&dag.Action{
			ID:      "A",
			Command: []string{"sh", "-c", "echo pure > " + outA},
			Outputs: []string{outA},
			Impure:  false,
			WorkDir: workDir,
		})
		return g
	}

	exec := &dag.Executor{Store: store, Workers: 1}

	// First run: execute.
	res1, err := exec.Execute(context.Background(), makeGraph())
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if res1.Completed[0].Cached {
		t.Error("first run should not be cached")
	}

	// Second run: cache hit.
	res2, err := exec.Execute(context.Background(), makeGraph())
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !res2.Completed[0].Cached {
		t.Error("second run of pure action should be cached")
	}
}
