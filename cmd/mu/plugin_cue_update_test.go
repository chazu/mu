package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chau/mu/internal/config"
)

// TestUpdateMuCuePlugin_AddNew adds a plugin entry to a mu.cue that has
// no `plugins` field at all.
func TestUpdateMuCuePlugin_AddNew(t *testing.T) {
	dir := t.TempDir()
	src := `toolchains: [{toolchain: "go", from: "scratch"}]
targets: [{target: "//x", toolchain: "go", sources: ["main.go"]}]
`
	mustWrite(t, filepath.Join(dir, "mu.cue"), src)

	if err := updateMuCuePlugin(dir, "myplug", "sha256:abc"); err != nil {
		t.Fatalf("updateMuCuePlugin: %v", err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load after update: %v", err)
	}
	got := findPlugin(cfg.Plugins, "myplug")
	if got == nil {
		t.Fatalf("plugin not added; plugins=%+v", cfg.Plugins)
	}
	if got.Digest != "sha256:abc" {
		t.Fatalf("digest=%q, want sha256:abc", got.Digest)
	}
}

// TestUpdateMuCuePlugin_AppendToExisting adds a new entry to an existing
// `plugins` list, leaving prior entries intact.
func TestUpdateMuCuePlugin_AppendToExisting(t *testing.T) {
	dir := t.TempDir()
	src := `toolchains: [{toolchain: "go", from: "scratch"}]
plugins: [
	{name: "go", script: "plugins/go"},
]
targets: [{target: "//x", toolchain: "go", sources: ["main.go"]}]
`
	mustWrite(t, filepath.Join(dir, "mu.cue"), src)

	if err := updateMuCuePlugin(dir, "lint", "sha256:def"); err != nil {
		t.Fatalf("updateMuCuePlugin: %v", err)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if findPlugin(cfg.Plugins, "go") == nil {
		t.Errorf("existing 'go' plugin lost")
	}
	got := findPlugin(cfg.Plugins, "lint")
	if got == nil || got.Digest != "sha256:def" {
		t.Fatalf("lint plugin not appended correctly: %+v", got)
	}
}

// TestUpdateMuCuePlugin_ReplaceDigest updates the digest on an entry
// whose name already matches.
func TestUpdateMuCuePlugin_ReplaceDigest(t *testing.T) {
	dir := t.TempDir()
	src := `toolchains: [{toolchain: "go", from: "scratch"}]
plugins: [
	{name: "go", digest: "sha256:old"},
]
targets: [{target: "//x", toolchain: "go", sources: ["main.go"]}]
`
	mustWrite(t, filepath.Join(dir, "mu.cue"), src)

	if err := updateMuCuePlugin(dir, "go", "sha256:new"); err != nil {
		t.Fatalf("updateMuCuePlugin: %v", err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "mu.cue"))
	if strings.Contains(string(out), "sha256:old") {
		t.Errorf("old digest still present:\n%s", out)
	}
	cfg, err := config.Load(dir)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	got := findPlugin(cfg.Plugins, "go")
	if got == nil || got.Digest != "sha256:new" {
		t.Fatalf("digest not replaced: %+v", got)
	}
}

func findPlugin(ps []config.PluginDef, name string) *config.PluginDef {
	for i := range ps {
		if ps[i].Name == name {
			return &ps[i]
		}
	}
	return nil
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
