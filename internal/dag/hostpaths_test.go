package dag_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/dag"
	"github.com/chazu/mu/internal/sandbox"
)

// A host tool outside the sandbox runs only when its directory is granted
// with host_paths; kernel isolation denies it otherwise.
func TestSandboxedHostPathGrant(t *testing.T) {
	level := requireHostShellSandbox(t)
	tools := t.TempDir()
	helper := filepath.Join(tools, "helper.sh")
	if err := os.WriteFile(helper, []byte("#!/bin/sh\necho granted > out.txt\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	run := func(grant map[string]string) *dag.ExecuteResult {
		project := t.TempDir()
		a := &dag.Action{
			ID: "host-tool", Command: []string{helper}, HostPaths: grant,
			Outputs: []string{"out.txt"}, Env: map[string]string{}, ProjectRoot: project, WorkDir: project,
		}
		return runGraph(t, &dag.Executor{Store: newStore(t), Workers: 1}, a)
	}
	requireSucceeded(t, run(map[string]string{tools: "dir"}))
	if level == sandbox.IsolationCopy {
		t.Log("copy isolation confines nothing; skipping the ungranted case")
		return
	}
	if result := run(nil); len(result.Failed) != 1 {
		t.Fatalf("ungranted host tool ran: %+v", result)
	}
}
