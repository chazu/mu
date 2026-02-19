package dag_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/disk"
	"github.com/chau/mu/internal/dag"
)

// ---------------------------------------------------------------------------
// Graph tests
// ---------------------------------------------------------------------------

func TestAddAction(t *testing.T) {
	g := dag.NewGraph()
	a := &dag.Action{ID: "build", Command: []string{"go", "build"}}
	if err := g.AddAction(a); err != nil {
		t.Fatalf("AddAction: %v", err)
	}
	got := g.Action("build")
	if got == nil {
		t.Fatal("Action(\"build\") returned nil")
	}
	if got.ID != "build" {
		t.Errorf("got ID %q, want %q", got.ID, "build")
	}
	if g.Len() != 1 {
		t.Errorf("Len() = %d, want 1", g.Len())
	}
}

func TestAddActionDuplicate(t *testing.T) {
	g := dag.NewGraph()
	a := &dag.Action{ID: "dup"}
	if err := g.AddAction(a); err != nil {
		t.Fatalf("first AddAction: %v", err)
	}
	err := g.AddAction(&dag.Action{ID: "dup"})
	if err == nil {
		t.Fatal("expected error on duplicate ID, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("error %q does not mention 'duplicate'", err)
	}
}

func TestDependents(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{ID: "A"})
	_ = g.AddAction(&dag.Action{ID: "B", DependsOn: []string{"A"}})

	// Forward: B depends on A
	deps := g.DependsOn("B")
	if len(deps) != 1 || deps[0] != "A" {
		t.Errorf("DependsOn(B) = %v, want [A]", deps)
	}

	// Reverse: A's dependents include B
	rdeps := g.Dependents("A")
	if len(rdeps) != 1 || rdeps[0] != "B" {
		t.Errorf("Dependents(A) = %v, want [B]", rdeps)
	}
}

func TestTransitiveDependents(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{ID: "A"})
	_ = g.AddAction(&dag.Action{ID: "B", DependsOn: []string{"A"}})
	_ = g.AddAction(&dag.Action{ID: "C", DependsOn: []string{"B"}})

	trans := g.TransitiveDependents("A")
	got := make(map[string]bool)
	for _, id := range trans {
		got[id] = true
	}
	if !got["B"] || !got["C"] {
		t.Errorf("TransitiveDependents(A) = %v, want B and C", trans)
	}
	if len(trans) != 2 {
		t.Errorf("len = %d, want 2", len(trans))
	}
}

// ---------------------------------------------------------------------------
// TopoSort tests
// ---------------------------------------------------------------------------

func TestTopoSortLinearChain(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{ID: "A"})
	_ = g.AddAction(&dag.Action{ID: "B", DependsOn: []string{"A"}})
	_ = g.AddAction(&dag.Action{ID: "C", DependsOn: []string{"B"}})

	levels, err := dag.TopoSort(g)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("got %d levels, want 3", len(levels))
	}
	if levels[0][0].ID != "A" {
		t.Errorf("level 0: got %q, want A", levels[0][0].ID)
	}
	if levels[1][0].ID != "B" {
		t.Errorf("level 1: got %q, want B", levels[1][0].ID)
	}
	if levels[2][0].ID != "C" {
		t.Errorf("level 2: got %q, want C", levels[2][0].ID)
	}
}

func TestTopoSortDiamond(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{ID: "A"})
	_ = g.AddAction(&dag.Action{ID: "B", DependsOn: []string{"A"}})
	_ = g.AddAction(&dag.Action{ID: "C", DependsOn: []string{"A"}})
	_ = g.AddAction(&dag.Action{ID: "D", DependsOn: []string{"B", "C"}})

	levels, err := dag.TopoSort(g)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if len(levels) != 3 {
		t.Fatalf("got %d levels, want 3", len(levels))
	}
	// Level 0: A
	if len(levels[0]) != 1 || levels[0][0].ID != "A" {
		t.Errorf("level 0: got %v, want [A]", idsFromLevel(levels[0]))
	}
	// Level 1: B and C (either order)
	l1 := idsFromLevel(levels[1])
	if len(l1) != 2 {
		t.Errorf("level 1: got %v, want [B C]", l1)
	}
	l1Set := toSet(l1)
	if !l1Set["B"] || !l1Set["C"] {
		t.Errorf("level 1: got %v, want B and C", l1)
	}
	// Level 2: D
	if len(levels[2]) != 1 || levels[2][0].ID != "D" {
		t.Errorf("level 2: got %v, want [D]", idsFromLevel(levels[2]))
	}
}

func TestTopoSortIndependent(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{ID: "A"})
	_ = g.AddAction(&dag.Action{ID: "B"})

	levels, err := dag.TopoSort(g)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if len(levels) != 1 {
		t.Fatalf("got %d levels, want 1", len(levels))
	}
	ids := idsFromLevel(levels[0])
	if len(ids) != 2 {
		t.Errorf("level 0: got %v, want 2 actions", ids)
	}
}

func TestTopoSortCycleDetection(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{ID: "A", DependsOn: []string{"C"}})
	_ = g.AddAction(&dag.Action{ID: "B", DependsOn: []string{"A"}})
	_ = g.AddAction(&dag.Action{ID: "C", DependsOn: []string{"B"}})

	_, err := dag.TopoSort(g)
	if err == nil {
		t.Fatal("expected cycle error, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q does not contain 'cycle'", err)
	}
}

func TestTopoSortUnknownDep(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{ID: "A", DependsOn: []string{"ghost"}})

	_, err := dag.TopoSort(g)
	if err == nil {
		t.Fatal("expected error for unknown dep, got nil")
	}
	if !strings.Contains(err.Error(), "unknown") {
		t.Errorf("error %q does not contain 'unknown'", err)
	}
}

func TestTopoSortEmpty(t *testing.T) {
	g := dag.NewGraph()
	levels, err := dag.TopoSort(g)
	if err != nil {
		t.Fatalf("TopoSort: %v", err)
	}
	if levels != nil {
		t.Errorf("expected nil, got %v", levels)
	}
}

// ---------------------------------------------------------------------------
// ActionKey tests
// ---------------------------------------------------------------------------

func TestActionKeyDeterminism(t *testing.T) {
	a := &dag.Action{
		ID:      "build",
		Command: []string{"go", "build", "."},
		Env:     map[string]string{"GOOS": "linux"},
		Inputs:  map[string]cas.Digest{"main.go": cas.NewSHA256("aaa")},
	}
	k1 := dag.ComputeActionKey(a)
	k2 := dag.ComputeActionKey(a)
	if k1 != k2 {
		t.Errorf("same action produced different keys: %v vs %v", k1, k2)
	}
}

func TestActionKeyDiffers(t *testing.T) {
	a1 := &dag.Action{
		ID:      "build",
		Command: []string{"go", "build"},
		Inputs:  map[string]cas.Digest{"main.go": cas.NewSHA256("aaa")},
	}
	a2 := &dag.Action{
		ID:      "build",
		Command: []string{"go", "build"},
		Inputs:  map[string]cas.Digest{"main.go": cas.NewSHA256("bbb")},
	}
	k1 := dag.ComputeActionKey(a1)
	k2 := dag.ComputeActionKey(a2)
	if k1 == k2 {
		t.Error("different inputs produced the same key")
	}
}

func TestActionKeyEnvOrder(t *testing.T) {
	// Build two actions with env vars inserted in different order.
	a1 := &dag.Action{
		ID:      "x",
		Command: []string{"echo"},
		Env:     map[string]string{"A": "1", "B": "2", "C": "3"},
	}
	a2 := &dag.Action{
		ID:      "x",
		Command: []string{"echo"},
		Env:     map[string]string{"C": "3", "A": "1", "B": "2"},
	}
	k1 := dag.ComputeActionKey(a1)
	k2 := dag.ComputeActionKey(a2)
	if k1 != k2 {
		t.Errorf("env order affected key: %v vs %v", k1, k2)
	}
}

func TestActionKeyInputOrder(t *testing.T) {
	a1 := &dag.Action{
		ID:      "x",
		Command: []string{"echo"},
		Inputs: map[string]cas.Digest{
			"a.go": cas.NewSHA256("111"),
			"b.go": cas.NewSHA256("222"),
		},
	}
	a2 := &dag.Action{
		ID:      "x",
		Command: []string{"echo"},
		Inputs: map[string]cas.Digest{
			"b.go": cas.NewSHA256("222"),
			"a.go": cas.NewSHA256("111"),
		},
	}
	k1 := dag.ComputeActionKey(a1)
	k2 := dag.ComputeActionKey(a2)
	if k1 != k2 {
		t.Errorf("input order affected key: %v vs %v", k1, k2)
	}
}

// ---------------------------------------------------------------------------
// Executor tests
// ---------------------------------------------------------------------------

func newStore(t *testing.T) cas.Store {
	t.Helper()
	s, err := disk.New(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("disk.New: %v", err)
	}
	return s
}

func TestExecutorLinearChain(t *testing.T) {
	workDir := t.TempDir()
	outA := filepath.Join(workDir, "a.txt")
	outB := filepath.Join(workDir, "b.txt")

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "A",
		Command: []string{"sh", "-c", "echo hello > " + outA},
		Outputs: []string{outA},
		WorkDir: workDir,
	})
	_ = g.AddAction(&dag.Action{
		ID:        "B",
		Command:   []string{"sh", "-c", "cat " + outA + " > " + outB},
		Outputs:   []string{outB},
		DependsOn: []string{"A"},
		WorkDir:   workDir,
	})

	exec := &dag.Executor{Store: newStore(t), Workers: 2}
	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Errorf("failed: %v", res.Failed)
	}
	if len(res.Completed) != 2 {
		t.Errorf("completed %d, want 2", len(res.Completed))
	}
	// Verify output file exists.
	data, err := os.ReadFile(outB)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(data), "hello") {
		t.Errorf("output = %q, want 'hello'", string(data))
	}
}

func TestExecutorCacheHit(t *testing.T) {
	workDir := t.TempDir()
	outA := filepath.Join(workDir, "a.txt")

	store := newStore(t)
	makeGraph := func() *dag.Graph {
		g := dag.NewGraph()
		_ = g.AddAction(&dag.Action{
			ID:      "A",
			Command: []string{"sh", "-c", "echo cached > " + outA},
			Outputs: []string{outA},
			WorkDir: workDir,
		})
		return g
	}

	exec := &dag.Executor{Store: store, Workers: 1}

	// First run: should execute.
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

	// Second run: should be a cache hit.
	res2, err := exec.Execute(context.Background(), makeGraph())
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if len(res2.Completed) != 1 {
		t.Fatalf("second run: completed %d, want 1", len(res2.Completed))
	}
	if !res2.Completed[0].Cached {
		t.Error("second run should be cached")
	}
}

func TestExecutorFailureCancelsDownstream(t *testing.T) {
	workDir := t.TempDir()

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "A",
		Command: []string{"sh", "-c", "exit 1"},
		WorkDir: workDir,
	})
	_ = g.AddAction(&dag.Action{
		ID:        "B",
		Command:   []string{"sh", "-c", "echo ok"},
		DependsOn: []string{"A"},
		WorkDir:   workDir,
	})

	exec := &dag.Executor{Store: newStore(t), Workers: 2}
	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 1 || res.Failed[0].ID != "A" {
		t.Errorf("Failed = %v, want [A]", res.Failed)
	}
	if len(res.Cancelled) != 1 || res.Cancelled[0] != "B" {
		t.Errorf("Cancelled = %v, want [B]", res.Cancelled)
	}
}

func TestExecutorIndependentContinues(t *testing.T) {
	workDir := t.TempDir()

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "A",
		Command: []string{"sh", "-c", "exit 1"},
		WorkDir: workDir,
	})
	_ = g.AddAction(&dag.Action{
		ID:      "B",
		Command: []string{"sh", "-c", "echo ok"},
		WorkDir: workDir,
	})

	exec := &dag.Executor{Store: newStore(t), Workers: 2}
	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 1 || res.Failed[0].ID != "A" {
		t.Errorf("Failed = %v, want [A]", res.Failed)
	}
	// B should complete despite A failing.
	completedIDs := make(map[string]bool)
	for _, s := range res.Completed {
		completedIDs[s.ID] = true
	}
	if !completedIDs["B"] {
		t.Errorf("B should have completed; Completed = %v", res.Completed)
	}
}

func TestExecutorContextCancel(t *testing.T) {
	workDir := t.TempDir()

	g := dag.NewGraph()
	// Use sleep to keep the action alive long enough to cancel.
	_ = g.AddAction(&dag.Action{
		ID:      "slow",
		Command: []string{"sh", "-c", "sleep 30"},
		WorkDir: workDir,
	})

	ctx, cancel := context.WithCancel(context.Background())

	exec := &dag.Executor{Store: newStore(t), Workers: 1}

	done := make(chan error, 1)
	go func() {
		_, err := exec.Execute(ctx, g)
		done <- err
	}()

	// Give the executor a moment to start, then cancel.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err != context.Canceled {
			t.Errorf("got error %v, want context.Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for executor to respect cancellation")
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func idsFromLevel(level []*dag.Action) []string {
	ids := make([]string, len(level))
	for i, a := range level {
		ids[i] = a.ID
	}
	return ids
}

func toSet(ss []string) map[string]bool {
	m := make(map[string]bool, len(ss))
	for _, s := range ss {
		m[s] = true
	}
	return m
}
