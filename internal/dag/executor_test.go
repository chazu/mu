package dag_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
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

func TestSealedInputsInjectedIntoEnv(t *testing.T) {
	workDir := t.TempDir()
	outFile := filepath.Join(workDir, "secret.txt")

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "deploy",
		Command: []string{"sh", "-c", "echo $SECRET_TOKEN > " + outFile},
		Outputs: []string{outFile},
		WorkDir: workDir,
		Env:     map[string]string{"OTHER": "value"},
		SealedInputs: map[string]string{
			"SECRET_TOKEN": "pass:deploy/token",
		},
	})

	exec := &dag.Executor{
		Store:   newStore(t),
		Workers: 1,
		ResolvedSecrets: map[string]map[string]string{
			"deploy": {"SECRET_TOKEN": "s3cr3t-value"},
		},
	}

	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("failed: %v", res.Failed)
	}

	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read output: %v", err)
	}
	if !strings.Contains(string(data), "s3cr3t-value") {
		t.Errorf("output = %q, want it to contain s3cr3t-value", string(data))
	}
}

func TestSealedInputsExcludedFromCacheKey(t *testing.T) {
	workDir := t.TempDir()
	outFile := filepath.Join(workDir, "out.txt")

	store := newStore(t)

	// Run action with one secret value.
	g1 := dag.NewGraph()
	_ = g1.AddAction(&dag.Action{
		ID:      "A",
		Command: []string{"sh", "-c", "echo hello > " + outFile},
		Outputs: []string{outFile},
		WorkDir: workDir,
		SealedInputs: map[string]string{
			"TOKEN": "pass:deploy/token",
		},
	})

	exec1 := &dag.Executor{
		Store:   store,
		Workers: 1,
		ResolvedSecrets: map[string]map[string]string{
			"A": {"TOKEN": "value-one"},
		},
	}
	res1, err := exec1.Execute(context.Background(), g1)
	if err != nil {
		t.Fatalf("first Execute: %v", err)
	}
	if res1.Completed[0].Cached {
		t.Error("first run should not be cached")
	}

	// Run same action with a DIFFERENT secret value — should still cache hit
	// because sealed inputs are excluded from the cache key.
	g2 := dag.NewGraph()
	_ = g2.AddAction(&dag.Action{
		ID:      "A",
		Command: []string{"sh", "-c", "echo hello > " + outFile},
		Outputs: []string{outFile},
		WorkDir: workDir,
		SealedInputs: map[string]string{
			"TOKEN": "pass:deploy/token",
		},
	})

	exec2 := &dag.Executor{
		Store:   store,
		Workers: 1,
		ResolvedSecrets: map[string]map[string]string{
			"A": {"TOKEN": "value-two-different"},
		},
	}
	res2, err := exec2.Execute(context.Background(), g2)
	if err != nil {
		t.Fatalf("second Execute: %v", err)
	}
	if !res2.Completed[0].Cached {
		t.Error("second run should be cached — sealed inputs must not affect cache key")
	}
}

func TestSealedInputsFileMode(t *testing.T) {
	// In file mode, $SECRET_KEY should hold a path to a 0600 file whose
	// contents are the resolved secret value.
	workDir := t.TempDir()
	outFile := filepath.Join(workDir, "captured")

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID: "deploy",
		// Capture: file path, file contents, file mode.
		Command: []string{"sh", "-c",
			`stat -f '%Lp' "$SECRET_KEY" > ` + outFile + `; ` +
				`echo --- >> ` + outFile + `; ` +
				`cat "$SECRET_KEY" >> ` + outFile},
		WorkDir: workDir,
		SealedInputs: map[string]string{
			"SECRET_KEY": "pass:raw:hosts/key",
		},
		SealedInputModes: map[string]string{
			"SECRET_KEY": "file",
		},
	})

	exec := &dag.Executor{
		Store:   newStore(t),
		Workers: 1,
		ResolvedSecrets: map[string]map[string]string{
			"deploy": {"SECRET_KEY": "-----BEGIN OPENSSH PRIVATE KEY-----\nstuff\n-----END OPENSSH PRIVATE KEY-----"},
		},
	}

	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("failed: %v", res.Failed)
	}

	got, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read captured: %v", err)
	}
	parts := strings.SplitN(strings.TrimSpace(string(got)), "---", 2)
	if len(parts) != 2 {
		t.Fatalf("expected stat---contents format, got %q", got)
	}
	mode := strings.TrimSpace(parts[0])
	contents := strings.TrimSpace(parts[1])

	if mode != "600" {
		t.Errorf("file mode = %q, want 600", mode)
	}
	if !strings.Contains(contents, "BEGIN OPENSSH PRIVATE KEY") {
		t.Errorf("file contents = %q, want it to include the key", contents)
	}
}

func TestSealedInputsFileModeDirCleanup(t *testing.T) {
	// The temp file path should not exist after Execute returns — the
	// per-action sealed-input dir is removed unconditionally.
	workDir := t.TempDir()
	pathOut := filepath.Join(workDir, "path")

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "deploy",
		Command: []string{"sh", "-c", `printf '%s' "$KEY" > ` + pathOut},
		WorkDir: workDir,
		SealedInputs: map[string]string{
			"KEY": "pass:raw:x",
		},
		SealedInputModes: map[string]string{
			"KEY": "file",
		},
	})

	exec := &dag.Executor{
		Store:   newStore(t),
		Workers: 1,
		ResolvedSecrets: map[string]map[string]string{
			"deploy": {"KEY": "secret-bytes"},
		},
	}

	if _, err := exec.Execute(context.Background(), g); err != nil {
		t.Fatalf("Execute: %v", err)
	}

	pathBytes, err := os.ReadFile(pathOut)
	if err != nil {
		t.Fatalf("read path: %v", err)
	}
	leakPath := strings.TrimSpace(string(pathBytes))
	if leakPath == "" {
		t.Fatal("action did not record the sealed-input file path")
	}
	if _, err := os.Stat(leakPath); !os.IsNotExist(err) {
		t.Errorf("sealed-input file %q still exists after Execute (err=%v) — it must be cleaned up", leakPath, err)
	}
}

func TestSealedInputsUnknownModeFails(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "x",
		Command: []string{"true"},
		SealedInputs: map[string]string{
			"K": "pass:x",
		},
		SealedInputModes: map[string]string{
			"K": "telepathy",
		},
	})

	exec := &dag.Executor{
		Store:           newStore(t),
		Workers:         1,
		ResolvedSecrets: map[string]map[string]string{"x": {"K": "v"}},
	}

	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(res.Failed))
	}
	if !strings.Contains(res.Failed[0].Err.Error(), "unknown mode") {
		t.Errorf("error = %v, want it to mention unknown mode", res.Failed[0].Err)
	}
}

func TestSealedInputsEnvModeStillWorks(t *testing.T) {
	// Default (empty mode) should behave exactly as before — value
	// exported as the env var. This protects backwards compatibility.
	workDir := t.TempDir()
	outFile := filepath.Join(workDir, "out.txt")

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "deploy",
		Command: []string{"sh", "-c", `printf '%s' "$T" > ` + outFile},
		WorkDir: workDir,
		SealedInputs: map[string]string{
			"T": "pass:t",
		},
		// No SealedInputModes — defaults to env.
	})

	exec := &dag.Executor{
		Store:           newStore(t),
		Workers:         1,
		ResolvedSecrets: map[string]map[string]string{"deploy": {"T": "abc123"}},
	}

	if _, err := exec.Execute(context.Background(), g); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	b, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(b) != "abc123" {
		t.Errorf("got %q, want abc123", string(b))
	}
}

func TestSealedInputsModeAffectsActionKey(t *testing.T) {
	a1 := &dag.Action{
		Command:      []string{"true"},
		SealedInputs: map[string]string{"K": "pass:x/y"},
		// no SealedInputModes — default env
	}
	a2 := *a1
	a2.SealedInputModes = map[string]string{"K": "file"}

	if dag.ComputeActionKey(a1) == dag.ComputeActionKey(&a2) {
		t.Error("changing sealed_input mode should change the action key")
	}
}

func TestSealedInputsRefAffectsActionKey(t *testing.T) {
	a1 := &dag.Action{
		Command:      []string{"true"},
		SealedInputs: map[string]string{"K": "pass:x/y"},
	}
	a2 := *a1
	a2.SealedInputs = map[string]string{"K": "pass:x/z"}

	if dag.ComputeActionKey(a1) == dag.ComputeActionKey(&a2) {
		t.Error("changing sealed_input ref should change the action key")
	}
}

func TestSealedOutputsCapturedAndRouted(t *testing.T) {
	workDir := t.TempDir()

	// Action writes "hunter2" to $MU_SEALED_OUT_DIR/ADMIN_PASS, expecting
	// the executor to capture and route it via SealedOutputWriter.
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "mint",
		Command: []string{"sh", "-c", `printf hunter2 > "$MU_SEALED_OUT_DIR/ADMIN_PASS"`},
		WorkDir: workDir,
		SealedOutputs: map[string]string{
			"ADMIN_PASS": "pass:registry/admin",
		},
	})

	type stored struct {
		ref, value string
	}
	var captured []stored

	exec := &dag.Executor{
		Store:   newStore(t),
		Workers: 1,
		SealedOutputWriter: func(_ context.Context, ref, value, mode string) error {
			captured = append(captured, stored{ref, value})
			_ = mode
			return nil
		},
	}

	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("failed: %v", res.Failed)
	}
	if len(captured) != 1 {
		t.Fatalf("captured = %d, want 1", len(captured))
	}
	if captured[0].ref != "pass:registry/admin" {
		t.Errorf("captured[0].ref = %q, want pass:registry/admin", captured[0].ref)
	}
	if captured[0].value != "hunter2" {
		t.Errorf("captured[0].value = %q, want hunter2", captured[0].value)
	}
}

func TestSealedOutputsMissingFileFails(t *testing.T) {
	workDir := t.TempDir()

	// Action does NOT write the declared sealed-output file. The executor
	// must surface this as a failure rather than silently dropping the
	// expected value.
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "mint",
		Command: []string{"sh", "-c", "true"},
		WorkDir: workDir,
		SealedOutputs: map[string]string{
			"ADMIN_PASS": "pass:registry/admin",
		},
	})

	exec := &dag.Executor{
		Store:   newStore(t),
		Workers: 1,
		SealedOutputWriter: func(_ context.Context, _, _, _ string) error {
			t.Fatal("writer should not be called when the file is missing")
			return nil
		},
	}

	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(res.Failed))
	}
	if !strings.Contains(res.Failed[0].Err.Error(), "ADMIN_PASS") {
		t.Errorf("error = %v, want it to mention ADMIN_PASS", res.Failed[0].Err)
	}
}

func TestSealedOutputsNoWriterFails(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "mint",
		Command: []string{"sh", "-c", "true"},
		SealedOutputs: map[string]string{
			"ADMIN_PASS": "pass:x/y",
		},
	})

	exec := &dag.Executor{Store: newStore(t), Workers: 1}
	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 1 {
		t.Fatalf("failed = %d, want 1", len(res.Failed))
	}
	if !strings.Contains(res.Failed[0].Err.Error(), "no SealedOutputWriter") {
		t.Errorf("error = %v, want it to mention SealedOutputWriter", res.Failed[0].Err)
	}
}

func TestSealedOutputsModePropagatedToWriter(t *testing.T) {
	workDir := t.TempDir()

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "mint",
		Command: []string{"sh", "-c", `printf hunter2 > "$MU_SEALED_OUT_DIR/V"`},
		WorkDir: workDir,
		SealedOutputs: map[string]string{
			"V": "pass:x/y",
		},
		SealedOutputModes: map[string]string{
			"V": "create_if_absent",
		},
	})

	var gotMode string
	exec := &dag.Executor{
		Store:   newStore(t),
		Workers: 1,
		SealedOutputWriter: func(_ context.Context, _, _, mode string) error {
			gotMode = mode
			return nil
		},
	}

	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 0 {
		t.Fatalf("failed: %v", res.Failed)
	}
	if gotMode != "create_if_absent" {
		t.Errorf("mode passed to writer = %q, want create_if_absent", gotMode)
	}
}

func TestSealedOutputsRefAffectsActionKey(t *testing.T) {
	// Sealed-output destination refs are part of the cache key — changing
	// the destination must invalidate any cached result.
	a1 := &dag.Action{
		Command:       []string{"true"},
		SealedOutputs: map[string]string{"X": "pass:a/b"},
	}
	a2 := *a1
	a2.SealedOutputs = map[string]string{"X": "pass:a/c"}

	if dag.ComputeActionKey(a1) == dag.ComputeActionKey(&a2) {
		t.Error("changing a sealed_output ref should change the action key")
	}
}

func TestSealedInputsNoResolvedSecretsIsNoOp(t *testing.T) {
	workDir := t.TempDir()
	outFile := filepath.Join(workDir, "out.txt")

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "A",
		Command: []string{"sh", "-c", "echo ok > " + outFile},
		Outputs: []string{outFile},
		WorkDir: workDir,
	})

	// No ResolvedSecrets set — should work fine for actions without sealed inputs.
	exec := &dag.Executor{Store: newStore(t), Workers: 1}
	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Completed) != 1 {
		t.Fatalf("completed %d, want 1", len(res.Completed))
	}
}

func TestPithBodyResultPropagates(t *testing.T) {
	g := dag.NewGraph()

	// Transform action: pushes {"count": 42} onto stack.
	_ = g.AddAction(&dag.Action{
		ID:     "//t:_transform",
		Body:   []any{map[string]any{"count": float64(42)}},
		Impure: true,
	})

	// Downstream action: reads transform result via target/output.
	// Program: push target name, call target/output, get "_result" key,
	// then get "count" key. Result should be 42.
	_ = g.AddAction(&dag.Action{
		ID:        "//t:check",
		Body:      []any{"'//t", "target/output", "'_result", "get", "'count", "get"},
		DependsOn: []string{"//t:_transform"},
		Impure:    true,
	})

	exec := &dag.Executor{Store: newStore(t), Workers: 1}
	res, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) != 0 {
		for _, f := range res.Failed {
			t.Errorf("failed: %s: %v", f.ID, f.Err)
		}
		t.FailNow()
	}
	if len(res.Completed) != 2 {
		t.Fatalf("completed %d, want 2", len(res.Completed))
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
