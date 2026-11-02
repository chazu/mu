package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/plugin"
)

func TestResolveHostPaths(t *testing.T) {
	dir := t.TempDir()
	tool := filepath.Join(dir, "tool")
	if err := os.WriteFile(tool, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	spec := plugin.ActionSpec{ID: "a", Command: []string{"tool"}, HostPaths: []string{dir, tool}}
	actions, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, t.TempDir(), newTestStore(t), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := cas.ComputeDigest(strings.NewReader("#!/bin/sh\n"))
	got := actions[0].HostPaths
	if got[dir] != "dir" || got[tool] != "file:"+want.String() {
		t.Fatalf("HostPaths = %v", got)
	}
}

func TestResolveHostPathsRejects(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct{ path, want string }{
		{"relative/tool", "absolute, clean"},
		{dir + "/../x", "absolute, clean"},
		{dir + "/", "absolute, clean"},
		{"/", "disable sandbox confinement"},
		{filepath.Join(dir, "missing"), "no such file"},
	} {
		spec := plugin.ActionSpec{ID: "a", Command: []string{"true"}, HostPaths: []string{tc.path}}
		_, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, t.TempDir(), newTestStore(t), nil, nil)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("host_paths %q: err = %v, want %q", tc.path, err, tc.want)
		}
	}
	spec := plugin.ActionSpec{ID: "a", Command: []string{"true"}, HostPaths: []string{dir, dir}}
	if _, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, t.TempDir(), newTestStore(t), nil, nil); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Errorf("duplicate host path: err = %v", err)
	}
}
