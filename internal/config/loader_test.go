package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestFindProjectRoot_Direct(t *testing.T) {
	dir := t.TempDir()
	writeJSON(t, filepath.Join(dir, "mu.json"), ProjectConfig{})

	root, err := FindProjectRoot(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if root != dir {
		t.Fatalf("expected %s, got %s", dir, root)
	}
}

func TestFindProjectRoot_WalkUp(t *testing.T) {
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{})

	child := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}

	found, err := FindProjectRoot(child)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found != root {
		t.Fatalf("expected %s, got %s", root, found)
	}
}

func TestFindProjectRoot_NotFound(t *testing.T) {
	dir := t.TempDir() // no mu.json here
	_, err := FindProjectRoot(dir)
	if err == nil {
		t.Fatal("expected error when mu.json is absent")
	}
}

func TestLoad_BasicMuJSON(t *testing.T) {
	root := t.TempDir()

	proj := ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"*.go"}},
		},
		Plugins: []PluginDef{
			{Name: "go", Command: []string{"mu-plugin-go"}},
		},
	}
	writeJSON(t, filepath.Join(root, "mu.json"), proj)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(cfg.Targets))
	}
	if cfg.Targets[0].Name != "//app" {
		t.Fatalf("expected target name //app, got %s", cfg.Targets[0].Name)
	}
	if len(cfg.Plugins) != 1 {
		t.Fatalf("expected 1 plugin, got %d", len(cfg.Plugins))
	}
}

func TestLoad_MergeBUILDJSON(t *testing.T) {
	root := t.TempDir()

	// Root mu.json with one target.
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{
		Targets: []Target{
			{Name: "//root-target", Toolchain: "go", Sources: []string{"*.go"}},
		},
	})

	// BUILD.json in a subdirectory adds another target.
	sub := filepath.Join(root, "pkg", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(sub, "BUILD.json"), ProjectConfig{
		Targets: []Target{
			{Name: "server", Toolchain: "go", Sources: []string{"*.go"}},
		},
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(cfg.Targets))
	}

	// The BUILD.json target should have been prefixed.
	var found bool
	for _, tgt := range cfg.Targets {
		if tgt.Name == "//pkg/web/server" {
			found = true
		}
	}
	if !found {
		names := make([]string, len(cfg.Targets))
		for i, tgt := range cfg.Targets {
			names[i] = tgt.Name
		}
		t.Fatalf("expected target //pkg/web/server, got targets: %v", names)
	}
}

func TestLoad_MergeBUILDJSON_Services(t *testing.T) {
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{})

	sub := filepath.Join(root, "infra")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(sub, "BUILD.json"), ProjectConfig{
		Services: []Service{
			{Name: "db", Runtime: "docker", Config: ServiceConfig{Image: "postgres:16"}},
		},
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Services) != 1 {
		t.Fatalf("expected 1 service, got %d", len(cfg.Services))
	}
	if cfg.Services[0].Name != "db" {
		t.Fatalf("expected service name db, got %s", cfg.Services[0].Name)
	}
}

func TestLoad_MultipleBUILDJSON(t *testing.T) {
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{})

	for _, pkg := range []string{"alpha", "beta", "gamma"} {
		dir := filepath.Join(root, pkg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(dir, "BUILD.json"), ProjectConfig{
			Targets: []Target{
				{Name: "lib", Toolchain: "go", Sources: []string{"*.go"}},
			},
		})
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 3 {
		t.Fatalf("expected 3 targets, got %d", len(cfg.Targets))
	}
}

func TestLoad_AlreadyPrefixedTarget(t *testing.T) {
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{})

	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(sub, "BUILD.json"), ProjectConfig{
		Targets: []Target{
			{Name: "//explicit/name", Toolchain: "go", Sources: []string{"*.go"}},
		},
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Targets[0].Name != "//explicit/name" {
		t.Fatalf("already-prefixed target should not be modified, got %s", cfg.Targets[0].Name)
	}
}

// writeJSON is a test helper that marshals v to path.
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}
