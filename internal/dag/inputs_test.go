package dag_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/dag"
)

// runProducerConsumer builds a producer -> consumer graph where the consumer's
// "in" input is a placeholder sourced from the producer, executes it, and
// returns the consumer's cached flag and resolved input digest.
func runProducerConsumer(t *testing.T, store cas.Store, workDir, content string, producerImpure bool, src dag.InputSource) (bool, cas.Digest) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(workDir, "seed.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	seed, err := cas.ComputeDigest(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	producer := &dag.Action{
		ID: "produce", Command: []string{"sh", "-c", "cp seed.txt mid.txt"},
		Inputs:     map[string]cas.Digest{"seed.txt": seed},
		InputPaths: map[string]string{"seed.txt": "seed.txt"},
		Outputs:    []string{"mid.txt"}, Env: map[string]string{}, WorkDir: workDir, Impure: producerImpure,
	}
	consumer := &dag.Action{
		ID: "consume", Command: []string{"sh", "-c", "cp mid.txt out.txt"},
		Inputs:       map[string]cas.Digest{"in": {}},
		InputSources: map[string]dag.InputSource{"in": src},
		Outputs:      []string{"out.txt"}, Env: map[string]string{}, WorkDir: workDir,
		DependsOn: []string{"produce"},
	}
	if src.OutputPath != "" {
		// Where the sandboxed consumer finds the named output.
		consumer.InputPaths = map[string]string{"in": src.OutputPath}
	}
	g := dag.NewGraph()
	if err := g.AddAction(producer); err != nil {
		t.Fatal(err)
	}
	if err := g.AddAction(consumer); err != nil {
		t.Fatal(err)
	}
	result, err := (&dag.Executor{Store: store, Workers: 1}).Execute(context.Background(), g)
	if err != nil || len(result.Failed) != 0 {
		t.Fatalf("execute: %+v, %v", result, err)
	}
	for _, s := range result.Completed {
		if s.ID == "consume" {
			return s.Cached, consumer.Inputs["in"]
		}
	}
	t.Fatal("consumer did not complete")
	return false, cas.Digest{}
}

func TestUpstreamInputsResolveBeforeCacheLookup(t *testing.T) {
	cases := map[string]struct {
		impure bool
		src    dag.InputSource
	}{
		"path from pure producer":         {src: dag.InputSource{ProducerID: "produce", OutputPath: "mid.txt"}},
		"path from impure producer":       {impure: true, src: dag.InputSource{ProducerID: "produce", OutputPath: "mid.txt"}},
		"action ref from pure producer":   {src: dag.InputSource{ProducerID: "produce"}},
		"action ref from impure producer": {impure: true, src: dag.InputSource{ProducerID: "produce"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			store, workDir := newStore(t), t.TempDir()
			cached, first := runProducerConsumer(t, store, workDir, "one", tc.impure, tc.src)
			if cached || first.IsZero() {
				t.Fatalf("first run: cached=%v digest=%v", cached, first)
			}
			cached, again := runProducerConsumer(t, store, workDir, "one", tc.impure, tc.src)
			if !cached || again != first {
				t.Fatalf("unchanged upstream: cached=%v digest %v, want hit with %v", cached, again, first)
			}
			cached, changed := runProducerConsumer(t, store, workDir, "two", tc.impure, tc.src)
			if cached || changed == first {
				t.Fatalf("changed upstream: cached=%v digest %v (first %v), want miss", cached, changed, first)
			}
			out, err := os.ReadFile(filepath.Join(workDir, "out.txt"))
			if err != nil || string(out) != "two" {
				t.Fatalf("out.txt = %q, %v; want two", out, err)
			}
		})
	}
}
