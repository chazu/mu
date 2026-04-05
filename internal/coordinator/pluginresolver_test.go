package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/chau/mu/internal/config"
)

func TestResolveLocalFile(t *testing.T) {
	store := newTestStore(t)
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	// Write a single-file plugin.
	scriptPath := filepath.Join(projectRoot, "plugin.sh")
	if err := os.WriteFile(scriptPath, []byte("#!/bin/bash\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &PluginResolver{Store: store, ProjectRoot: projectRoot, CacheDir: cacheDir}
	rp, err := r.resolveOne(context.Background(), config.PluginDef{
		Name:   "test",
		Script: "plugin.sh",
	})
	if err != nil {
		t.Fatalf("resolveOne: %v", err)
	}

	if rp.Def.Name != "test" {
		t.Errorf("Name = %q, want test", rp.Def.Name)
	}
	if rp.Def.WorkDir != "" {
		t.Errorf("WorkDir = %q, want empty for single-file plugin", rp.Def.WorkDir)
	}
	if rp.Digest.Hash == "" {
		t.Error("expected non-empty digest")
	}
	// Verify the extracted file exists.
	if _, err := os.Stat(rp.Def.Script); err != nil {
		t.Errorf("extracted script not found: %v", err)
	}
}

func TestResolveLocalDir(t *testing.T) {
	store := newTestStore(t)
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	// Create a plugin directory with manifest and files.
	pluginDir := filepath.Join(projectRoot, "plugins", "myplug")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "mu.json"), []byte(`{
		"plugin": {"entrypoint": "run.sh", "runtime": "none"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/bash\necho ok"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "helper.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &PluginResolver{Store: store, ProjectRoot: projectRoot, CacheDir: cacheDir}
	rp, err := r.resolveOne(context.Background(), config.PluginDef{
		Name:   "myplug",
		Script: "plugins/myplug",
	})
	if err != nil {
		t.Fatalf("resolveOne: %v", err)
	}

	if rp.Def.Name != "myplug" {
		t.Errorf("Name = %q, want myplug", rp.Def.Name)
	}
	if rp.Def.WorkDir == "" {
		t.Fatal("WorkDir should be set for directory plugin")
	}
	// Verify entrypoint exists in extracted dir.
	if _, err := os.Stat(rp.Def.Script); err != nil {
		t.Errorf("entrypoint not found: %v", err)
	}
	// Verify sibling file exists in extracted dir.
	helperPath := filepath.Join(rp.Def.WorkDir, "helper.txt")
	if _, err := os.Stat(helperPath); err != nil {
		t.Errorf("sibling helper.txt not found: %v", err)
	}
	data, _ := os.ReadFile(helperPath)
	if string(data) != "hello" {
		t.Errorf("helper.txt content = %q, want hello", string(data))
	}
	if rp.Def.Toolchain != "" {
		t.Errorf("Toolchain = %q, want empty for direct-execution plugin", rp.Def.Toolchain)
	}
}

func TestResolveLocalDir_MissingManifest(t *testing.T) {
	store := newTestStore(t)
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	// Directory without mu.json.
	pluginDir := filepath.Join(projectRoot, "plugins", "bad")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "run.sh"), []byte("#!/bin/bash"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &PluginResolver{Store: store, ProjectRoot: projectRoot, CacheDir: cacheDir}
	_, err := r.resolveOne(context.Background(), config.PluginDef{
		Name:   "bad",
		Script: "plugins/bad",
	})
	if err == nil {
		t.Fatal("expected error for missing manifest")
	}
}

func TestResolveLocalDir_MissingEntrypoint(t *testing.T) {
	store := newTestStore(t)
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	pluginDir := filepath.Join(projectRoot, "plugins", "bad")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "mu.json"), []byte(`{
		"plugin": {"entrypoint": "nonexistent.sh"}
	}`), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &PluginResolver{Store: store, ProjectRoot: projectRoot, CacheDir: cacheDir}
	_, err := r.resolveOne(context.Background(), config.PluginDef{
		Name:   "bad",
		Script: "plugins/bad",
	})
	if err == nil {
		t.Fatal("expected error for missing entrypoint")
	}
}

func TestBundleDir_Deterministic(t *testing.T) {
	store := newTestStore(t)
	projectRoot := t.TempDir()

	// Create a directory with some files.
	dir := filepath.Join(projectRoot, "plug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaa"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bbb"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &PluginResolver{Store: store, ProjectRoot: projectRoot, CacheDir: t.TempDir()}

	d1, err := r.bundleDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("first bundle: %v", err)
	}
	d2, err := r.bundleDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("second bundle: %v", err)
	}

	if d1 != d2 {
		t.Errorf("non-deterministic tar: %s != %s", d1, d2)
	}
}

func TestBundleDir_SkipsHiddenDirs(t *testing.T) {
	store := newTestStore(t)
	projectRoot := t.TempDir()

	dir := filepath.Join(projectRoot, "plug")
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte("gitconfig"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.sh"), []byte("#!/bin/bash"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &PluginResolver{Store: store, ProjectRoot: projectRoot, CacheDir: t.TempDir()}

	dgst, err := r.bundleDir(context.Background(), dir)
	if err != nil {
		t.Fatalf("bundle: %v", err)
	}

	// Extract and verify .git/config is NOT present.
	extractDir, err := r.extractDirFromCAS(context.Background(), "test", dgst)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}

	if _, err := os.Stat(filepath.Join(extractDir, ".git", "config")); err == nil {
		t.Error(".git/config should not be in the bundle")
	}
	if _, err := os.Stat(filepath.Join(extractDir, "plugin.sh")); err != nil {
		t.Error("plugin.sh should be in the bundle")
	}
}

func TestExtractDirFromCAS_Idempotent(t *testing.T) {
	store := newTestStore(t)
	projectRoot := t.TempDir()

	dir := filepath.Join(projectRoot, "plug")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "file.txt"), []byte("content"), 0o644); err != nil {
		t.Fatal(err)
	}

	r := &PluginResolver{Store: store, ProjectRoot: projectRoot, CacheDir: t.TempDir()}

	dgst, err := r.bundleDir(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}

	dir1, err := r.extractDirFromCAS(context.Background(), "test", dgst)
	if err != nil {
		t.Fatal(err)
	}
	dir2, err := r.extractDirFromCAS(context.Background(), "test", dgst)
	if err != nil {
		t.Fatal(err)
	}

	if dir1 != dir2 {
		t.Errorf("extract paths differ: %s != %s", dir1, dir2)
	}
}
