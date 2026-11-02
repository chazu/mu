package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

func fakeDigest(s string) string {
	d, _ := cas.ComputeDigest(strings.NewReader(s))
	return d.String()
}

func staticToolchains(m map[string]map[string]string) ToolchainLookup {
	return func(_ context.Context, name string) (*ToolchainManifest, error) {
		if m[name] == nil {
			return nil, nil
		}
		return &ToolchainManifest{Name: name, Artifacts: m[name]}, nil
	}
}

func TestResolveToolchainsMergesArtifacts(t *testing.T) {
	lookup := staticToolchains(map[string]map[string]string{
		"go": {"bin/go": fakeDigest("go"), "pkg/tool/compile": fakeDigest("compile")},
		"bb": {"bb": fakeDigest("bb")}, // raw binary at the root
	})
	actions, err := Resolve(context.Background(), []plugin.ActionSpec{{
		ID: "build", Command: []string{"go", "build"}, Toolchains: []string{"go", "bb"},
	}}, t.TempDir(), nil, nil, lookup)
	if err != nil {
		t.Fatal(err)
	}
	got := actions[0].Toolchain
	if len(got) != 3 || got["bin/go"].String() != fakeDigest("go") || got["pkg/tool/compile"].IsZero() {
		t.Fatalf("toolchain = %v", got)
	}
	if got["bin/bb"].String() != fakeDigest("bb") {
		t.Fatalf("raw-binary toolchain not placed on PATH: %v", got)
	}
	if !actions[0].Sandboxed() {
		t.Fatal("an action naming toolchains must be sandboxed")
	}
}

// Modes and symlinks from the manifest reach the action; a raw binary's
// mode follows it to bin/<name>.
func TestResolveToolchainsModesAndSymlinks(t *testing.T) {
	lookup := func(_ context.Context, name string) (*ToolchainManifest, error) {
		switch name {
		case "tool":
			return &ToolchainManifest{
				Artifacts: map[string]string{"libexec/tool": fakeDigest("tool"), "share/doc": fakeDigest("doc")},
				Modes:     map[string]uint32{"libexec/tool": 0o755, "share/doc": 0o644},
				Symlinks:  map[string]string{"bin/tool": "../libexec/tool"},
			}, nil
		case "raw":
			return &ToolchainManifest{Artifacts: map[string]string{"raw": fakeDigest("raw")}, Modes: map[string]uint32{"raw": 0o755}}, nil
		}
		return nil, nil
	}
	actions, err := Resolve(context.Background(), []plugin.ActionSpec{{
		ID: "a", Command: []string{"tool"}, Toolchains: []string{"tool", "raw"},
	}}, t.TempDir(), nil, nil, lookup)
	if err != nil {
		t.Fatal(err)
	}
	a := actions[0]
	if a.ToolchainLinks["bin/tool"] != "../libexec/tool" || len(a.ToolchainLinks) != 1 {
		t.Fatalf("links = %v", a.ToolchainLinks)
	}
	if a.ToolchainModes["share/doc"] != 0o644 || a.ToolchainModes["bin/raw"] != 0o755 {
		t.Fatalf("modes = %v", a.ToolchainModes)
	}
	if _, ok := a.Toolchain["bin/tool"]; ok {
		t.Fatal("a symlink was resolved into a file artifact")
	}
}

func TestResolveToolchainsErrors(t *testing.T) {
	lookup := staticToolchains(map[string]map[string]string{
		"a": {"bin/x": fakeDigest("a")},
		"b": {"bin/x": fakeDigest("b")},
	})
	for name, tc := range map[string]struct {
		spec plugin.ActionSpec
		want string
	}{
		"unknown":  {plugin.ActionSpec{ID: "x", Command: []string{"x"}, Toolchains: []string{"nope"}}, `unknown toolchain "nope"`},
		"impure":   {plugin.ActionSpec{ID: "x", Command: []string{"x"}, Toolchains: []string{"a"}, Impure: true}, "is impure"},
		"conflict": {plugin.ActionSpec{ID: "x", Command: []string{"x"}, Toolchains: []string{"a", "b"}}, "different content"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve(context.Background(), []plugin.ActionSpec{tc.spec}, t.TempDir(), nil, nil, lookup)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want %q", err, tc.want)
			}
		})
	}
	// No lookup at all: any named toolchain is unknown.
	_, err := Resolve(context.Background(), []plugin.ActionSpec{{ID: "x", Command: []string{"x"}, Toolchains: []string{"a"}}}, t.TempDir(), nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "unknown toolchain") {
		t.Fatalf("nil lookup: err = %v", err)
	}
}

func TestResolveRecordsInputPaths(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "pkg"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pkg", "a.go"), []byte("package a"), 0o644); err != nil {
		t.Fatal(err)
	}
	actions, err := Resolve(context.Background(), []plugin.ActionSpec{{
		ID: "build", Command: []string{"true"},
		Inputs: map[string]string{
			"rel": "pkg/a.go", "abs": filepath.Join(dir, "pkg", "a.go"),
			"gen": "out/gen.txt", "all": "{action:other}",
		},
	}}, dir, nil, map[string]string{"out/gen.txt": "//gen:run"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	paths := actions[0].InputPaths
	if paths["rel"] != "pkg/a.go" || paths["abs"] != "pkg/a.go" || paths["gen"] != "out/gen.txt" {
		t.Fatalf("input paths = %v", paths)
	}
	if _, ok := paths["all"]; ok {
		t.Fatalf("whole-producer reference got a path: %v", paths)
	}
}

func TestToolchainLookupUsesRegistryAndConfig(t *testing.T) {
	store := newTestStore(t)
	registry := NewToolchainRegistry(store)
	manifest := &ToolchainManifest{Name: "go", Version: "1.0", Artifacts: map[string]string{"bin/go": fakeDigest("go")}}
	if err := registry.Register(context.Background(), manifest); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{Config: &config.ProjectConfig{Toolchains: []config.Toolchain{
		{Name: "go", From: "scratch", Config: config.ToolchainConfig{Version: "1.0"}},
		{Name: "zig", From: "scratch", Config: config.ToolchainConfig{Version: "0.1"}},
	}}}
	lookup := c.toolchainLookup(registry)
	if got, err := lookup(context.Background(), "go"); err != nil || got.Artifacts["bin/go"] != fakeDigest("go") {
		t.Fatalf("registered toolchain: %v, %v", got, err)
	}
	// A fresh registry finds the CAS receipt by configured version.
	if got, err := c.toolchainLookup(NewToolchainRegistry(store))(context.Background(), "go"); err != nil || got.Artifacts["bin/go"] == "" {
		t.Fatalf("receipt lookup: %v, %v", got, err)
	}
	if got, err := lookup(context.Background(), "nope"); err != nil || got != nil {
		t.Fatalf("unconfigured toolchain: %v, %v", got, err)
	}
	if _, err := lookup(context.Background(), "zig"); err == nil || !strings.Contains(err.Error(), "mu scratch") {
		t.Fatalf("configured but unbuilt toolchain without a builder: %v", err)
	}
}
