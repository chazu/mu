package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadPluginManifest_Valid(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mu.cue"), []byte(`
plugin: {
	entrypoint: "plugin.bb"
	toolchain:  "bb"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPluginManifest(dir)
	if err != nil {
		t.Fatalf("LoadPluginManifest: %v", err)
	}
	if cfg.Plugin.Entrypoint != "plugin.bb" {
		t.Errorf("Entrypoint = %q, want plugin.bb", cfg.Plugin.Entrypoint)
	}
	if cfg.Plugin.Toolchain != "bb" {
		t.Errorf("Toolchain = %q, want bb", cfg.Plugin.Toolchain)
	}
}

func TestLoadPluginManifest_WithTargets(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mu.cue"), []byte(`
plugin: {entrypoint: "bin/myplugin"}
targets: [{target: "//cmd/myplugin", toolchain: "go", sources: ["main.go"]}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadPluginManifest(dir)
	if err != nil {
		t.Fatalf("LoadPluginManifest: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Errorf("expected 1 target, got %d", len(cfg.Targets))
	}
}

func TestLoadPluginManifest_NoPluginKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mu.cue"), []byte(`
targets: [{target: "//app", toolchain: "go", sources: ["main.go"]}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPluginManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing plugin key")
	}
}

func TestLoadPluginManifest_MissingEntrypoint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mu.cue"), []byte(`
plugin: {toolchain: "bb"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadPluginManifest(dir)
	if err == nil {
		t.Fatal("expected error for missing entrypoint")
	}
}

func TestLoadPluginManifest_NoFile(t *testing.T) {
	_, err := LoadPluginManifest(t.TempDir())
	if err == nil {
		t.Fatal("expected error for missing mu.cue")
	}
}
