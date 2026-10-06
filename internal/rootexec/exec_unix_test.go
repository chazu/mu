//go:build darwin || linux

package rootexec_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/rootexec"
)

func TestCommandUsesOpenedDirectoryAfterSymlinkSwap(t *testing.T) {
	inside, outside := t.TempDir(), t.TempDir()
	link := filepath.Join(t.TempDir(), "work")
	if err := os.Symlink(inside, link); err != nil {
		t.Fatal(err)
	}
	dir, err := os.Open(link)
	if err != nil {
		t.Fatal(err)
	}
	defer dir.Close()
	cmd, err := rootexec.Command(context.Background(), []string{"sh", "-c", "printf pinned > marker"}, []string{"PATH=/usr/bin:/bin"}, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("execute: %v, %s", err, stderr.String())
	}
	if data, err := os.ReadFile(filepath.Join(inside, "marker")); err != nil || string(data) != "pinned" {
		t.Fatalf("original directory: %q, %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "marker")); !os.IsNotExist(err) {
		t.Fatalf("execution escaped: %v", err)
	}
}
