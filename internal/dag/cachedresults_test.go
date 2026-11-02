package dag_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/dag"
)

// chainGraph plans produce -> consume, where consume's input is a placeholder
// for produce's output (its key is unknown until produce's digest is).
func chainGraph(t *testing.T, workDir string) *dag.Graph {
	t.Helper()
	g := dag.NewGraph()
	for _, a := range []*dag.Action{
		{ID: "produce", Command: []string{"sh", "-c", "printf mid > mid.txt"},
			Outputs: []string{"mid.txt"}, Env: map[string]string{}, WorkDir: workDir},
		{ID: "consume", Command: []string{"sh", "-c", "cp mid.txt out.txt"},
			Inputs:       map[string]cas.Digest{"in": {}},
			InputSources: map[string]dag.InputSource{"in": {ProducerID: "produce", OutputPath: "mid.txt"}},
			InputPaths:   map[string]string{"in": "mid.txt"}, // where the sandboxed consumer finds it
			Outputs:      []string{"out.txt"}, Env: map[string]string{}, WorkDir: workDir, DependsOn: []string{"produce"}},
		{ID: "side-effect", Command: []string{"true"}, Impure: true, Env: map[string]string{}, WorkDir: workDir},
	} {
		if err := g.AddAction(a); err != nil {
			t.Fatal(err)
		}
	}
	return g
}

func TestCachedResultsDerivesDownstreamKeysFromStoredResults(t *testing.T) {
	store, err := cas.NewDiskStore(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatal(err)
	}
	workDir := t.TempDir()
	res, err := (&dag.Executor{Store: store, Workers: 1}).Execute(context.Background(), chainGraph(t, workDir))
	if err != nil || len(res.Failed) != 0 {
		t.Fatalf("execute: %+v, %v", res, err)
	}

	// Each lookup is checked against what the build actually recorded.
	found, missing, err := dag.CachedResults(context.Background(), chainGraph(t, workDir), store.GetActionResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 || len(found) != 2 || found[0].ActionID != "produce" || found[1].ActionID != "consume" {
		t.Fatalf("found %+v, missing %v", found, missing)
	}
	for _, f := range found {
		stored, err := store.GetActionResult(context.Background(), f.Key)
		if err != nil || stored == nil {
			t.Fatalf("%s: key %s has no stored result", f.ActionID, f.Key.Digest)
		}
	}

	// Without the producer's result the consumer's key cannot be derived.
	producerKey := found[0].Key
	p, _ := store.ActionResultPath(producerKey)
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	found, missing, err = dag.CachedResults(context.Background(), chainGraph(t, workDir), store.GetActionResult)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 0 || len(missing) != 2 {
		t.Fatalf("after removing producer: found %+v, missing %v", found, missing)
	}
}
