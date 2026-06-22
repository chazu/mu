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

// putEwe stores an ewe program in the CAS and returns its digest.
func putEwe(t *testing.T, store cas.Store, src string) cas.Digest {
	t.Helper()
	d, err := store.Put(context.Background(), strings.NewReader(src))
	if err != nil {
		t.Fatalf("store.Put: %v", err)
	}
	return d
}

// Test 2: the ewe arm runs a program that writes a declared output; the output
// is staged in $MU_OUT and copied to WorkDir.
func TestExecutorEweArmWritesOutput(t *testing.T) {
	store := newStore(t)
	workDir := t.TempDir()

	prog := `import "op"
_env: op.#Env & { args: [] }
w: op.#WriteFile & { args: ["\(_env.result.MU_OUT)/out.json", [{name: "hi"}]] }
`
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "//m:fetch",
		EweRef:  putEwe(t, store, prog),
		Outputs: []string{"out.json"},
		WorkDir: workDir,
		Impure:  true,
	})

	exec := &dag.Executor{Store: store, Workers: 1}
	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("action failed: %v", res.Failed[0].Err)
	}

	data, err := os.ReadFile(filepath.Join(workDir, "out.json"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(data), `"name":"hi"`) {
		t.Errorf("output = %s, want a record with name=hi", data)
	}
}

// Test 3: #Env exposes MU_OUT but NOT sealed-input values — secrets never enter
// the CUE layer (reveal happens only in sinks).
func TestExecutorEweEnvExcludesSecrets(t *testing.T) {
	store := newStore(t)
	workDir := t.TempDir()

	prog := `import "op"
_env: op.#Env & { args: [] }
w: op.#WriteFile & { args: ["\(_env.result.MU_OUT)/env.json", _env.result] }
`
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:           "//m:fetch",
		EweRef:       putEwe(t, store, prog),
		Outputs:      []string{"env.json"},
		WorkDir:      workDir,
		SealedInputs: map[string]string{"TOK": "env:TOK"},
		Impure:       true,
	})

	exec := &dag.Executor{
		Store:           store,
		Workers:         1,
		ResolvedSecrets: map[string]map[string]string{"//m:fetch": {"TOK": "supersecret"}},
	}
	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("action failed: %v", res.Failed[0].Err)
	}

	data, err := os.ReadFile(filepath.Join(workDir, "env.json"))
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if strings.Contains(string(data), "supersecret") {
		t.Errorf("#Env leaked a sealed-input value: %s", data)
	}
	if !strings.Contains(string(data), "MU_OUT") {
		t.Errorf("#Env did not expose MU_OUT: %s", data)
	}
}

// Test 5: the EweRef digest is part of the cache key (and sealed-input values
// are not — regression over the actionkey rules).
func TestEweRefInCacheKey(t *testing.T) {
	base := &dag.Action{ID: "//m:fetch", EweRef: cas.NewSHA256("aaa"), Impure: true}
	other := &dag.Action{ID: "//m:fetch", EweRef: cas.NewSHA256("bbb"), Impure: true}
	none := &dag.Action{ID: "//m:fetch", Impure: true}

	kBase := dag.ComputeActionKey(base)
	if kBase == dag.ComputeActionKey(other) {
		t.Error("different EweRef digests produced the same cache key")
	}
	if kBase == dag.ComputeActionKey(none) {
		t.Error("EweRef did not contribute to the cache key")
	}

	// Sealed-input VALUES must not affect the key; refs do.
	withVal := &dag.Action{
		ID: "//m:fetch", EweRef: cas.NewSHA256("aaa"), Impure: true,
		SealedInputs: map[string]string{"TOK": "env:TOK"},
	}
	again := &dag.Action{
		ID: "//m:fetch", EweRef: cas.NewSHA256("aaa"), Impure: true,
		SealedInputs: map[string]string{"TOK": "env:TOK"},
	}
	if dag.ComputeActionKey(withVal) != dag.ComputeActionKey(again) {
		t.Error("identical sealed-input refs produced different keys")
	}
}
