package dag_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/dag"
)

// A toolchain whose bin/<name> is a symlink (bin/mytool -> ../libexec/mytool)
// runs through the recreated link, and recorded modes survive unpacking.
func TestSandboxedToolchainSymlinkAndModes(t *testing.T) {
	requireHostShellSandbox(t)
	store := newStore(t)
	tool, err := store.Put(context.Background(), bytes.NewReader([]byte("#!/bin/sh\nls -l \"$(dirname \"$0\")/../share/doc.txt\" | cut -c1-10 > \"$1\"\n")))
	if err != nil {
		t.Fatal(err)
	}
	doc, err := store.Put(context.Background(), bytes.NewReader([]byte("doc")))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	a := &dag.Action{
		ID: "tool", Command: []string{"mytool", "out.txt"},
		Toolchain:      map[string]cas.Digest{"libexec/mytool": tool, "share/doc.txt": doc},
		ToolchainModes: map[string]uint32{"libexec/mytool": 0o755, "share/doc.txt": 0o644},
		ToolchainLinks: map[string]string{"bin/mytool": "../libexec/mytool"},
		Outputs:        []string{"out.txt"}, Env: map[string]string{}, ProjectRoot: project, WorkDir: project,
	}
	requireSucceeded(t, runGraph(t, &dag.Executor{Store: store, Workers: 1}, a))
	got, err := os.ReadFile(filepath.Join(project, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(bytes.TrimSpace(got)) != "-rw-r--r--" {
		t.Fatalf("share/doc.txt mode in sandbox = %q, want -rw-r--r--", got)
	}
}
