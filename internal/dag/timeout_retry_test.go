package dag_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/chau/mu/internal/dag"
)

// flakyScript writes a shell script at path that fails for the first
// failuresBefore attempts and then succeeds. It uses a counter file
// co-located next to the script so attempts are deterministic across
// invocations.
func flakyScript(t *testing.T, dir string, failuresBefore int) (scriptPath, counterPath string) {
	t.Helper()
	scriptPath = filepath.Join(dir, "flaky.sh")
	counterPath = filepath.Join(dir, "counter")
	body := fmt.Sprintf(`#!/bin/sh
c=$(cat %[1]q 2>/dev/null || echo 0)
c=$((c + 1))
echo $c > %[1]q
if [ "$c" -le %[2]d ]; then exit 17; fi
exit 0
`, counterPath, failuresBefore)
	if err := os.WriteFile(scriptPath, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
	return
}

func TestExecutor_RetrySucceedsAfterTransientFailures(t *testing.T) {
	dir := t.TempDir()
	script, _ := flakyScript(t, dir, 2)

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:             "flaky",
		Command:        []string{"sh", script},
		Network:        true,
		Retries:        3,
		RetryBackoffMs: 1,
	})

	exec := &dag.Executor{}
	result, err := exec.Execute(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) > 0 {
		t.Fatalf("expected success after retries, got failures: %+v", result.Failed)
	}
	if len(result.Completed) != 1 {
		t.Fatalf("expected 1 completed, got %d", len(result.Completed))
	}
	got := result.Completed[0]
	if got.Attempts != 3 {
		t.Errorf("Attempts = %d, want 3", got.Attempts)
	}
}

func TestExecutor_RetryExhaustedReportsFailure(t *testing.T) {
	dir := t.TempDir()
	script, _ := flakyScript(t, dir, 100)

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:             "doomed",
		Command:        []string{"sh", script},
		Network:        true,
		Retries:        2,
		RetryBackoffMs: 1,
	})

	exec := &dag.Executor{}
	result, _ := exec.Execute(context.Background(), g)
	if len(result.Failed) != 1 {
		t.Fatalf("expected failure after retries, got %+v", result)
	}
	if got := result.Failed[0].Attempts; got != 3 {
		t.Errorf("Attempts = %d, want 3 (1 initial + 2 retries)", got)
	}
}

func TestExecutor_NonNetworkActionIgnoresRetries(t *testing.T) {
	dir := t.TempDir()
	script, _ := flakyScript(t, dir, 100)

	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:      "pure",
		Command: []string{"sh", script},
		Network: false, // retries ignored
		Retries: 5,
	})

	exec := &dag.Executor{}
	result, _ := exec.Execute(context.Background(), g)
	if len(result.Failed) != 1 {
		t.Fatalf("expected single failure, got %+v", result)
	}
	if got := result.Failed[0].Attempts; got != 1 {
		t.Errorf("non-network action must not retry; Attempts = %d", got)
	}
}

func TestExecutor_TimeoutKillsSlowAction(t *testing.T) {
	g := dag.NewGraph()
	_ = g.AddAction(&dag.Action{
		ID:       "slow",
		Command:  []string{"sleep", "30"},
		TimeoutS: 1,
	})

	exec := &dag.Executor{}
	start := time.Now()
	result, _ := exec.Execute(context.Background(), g)
	elapsed := time.Since(start)

	if len(result.Failed) != 1 {
		t.Fatalf("expected timeout to fail the action, got %+v", result)
	}
	if elapsed > 5*time.Second {
		t.Errorf("timeout should kill within ~1s, took %v", elapsed)
	}
}

func TestExecutor_CacheKeyIgnoresTimeoutRetryFields(t *testing.T) {
	base := &dag.Action{
		ID:      "x",
		Command: []string{"true"},
	}
	tweaked := *base
	tweaked.TimeoutS = 60
	tweaked.Retries = 3
	tweaked.RetryBackoffMs = 500

	if dag.ComputeActionKey(base) != dag.ComputeActionKey(&tweaked) {
		t.Fatal("cache key must be stable across timeout/retry tweaks")
	}
}
