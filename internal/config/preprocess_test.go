package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeScript creates an executable shell script at path with the given body.
// On Windows this is a no-op skip.
func writeScript(t *testing.T, path, body string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell scripts not supported on Windows")
	}
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestPreprocess_Success(t *testing.T) {
	dir := t.TempDir()

	// A script that cats its argument (a JSON file).
	script := filepath.Join(dir, "pp.sh")
	writeScript(t, script, `cat "$1"`)

	// Write a BUILD.star file that is actually JSON.
	buildFile := filepath.Join(dir, "BUILD.star")
	writeJSON(t, buildFile, ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"*.go"}},
		},
	})

	pp := &Preprocessor{
		Extension: "star",
		Command:   []string{script},
	}

	cfg, err := Preprocess(pp, buildFile)
	if err != nil {
		t.Fatalf("Preprocess: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(cfg.Targets))
	}
	if cfg.Targets[0].Name != "//app" {
		t.Fatalf("expected target //app, got %s", cfg.Targets[0].Name)
	}
}

func TestPreprocess_NonZeroExit(t *testing.T) {
	dir := t.TempDir()

	script := filepath.Join(dir, "fail.sh")
	writeScript(t, script, `echo "something went wrong" >&2; exit 1`)

	pp := &Preprocessor{
		Extension: "star",
		Command:   []string{script},
	}

	_, err := Preprocess(pp, "/dev/null")
	if err == nil {
		t.Fatal("expected error for non-zero exit")
	}
	if !strings.Contains(err.Error(), "exited with status 1") {
		t.Fatalf("expected exit status in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "something went wrong") {
		t.Fatalf("expected stderr in error, got: %v", err)
	}
}

func TestPreprocess_CommandNotFound(t *testing.T) {
	pp := &Preprocessor{
		Extension: "star",
		Command:   []string{"/nonexistent/command/that/does/not/exist"},
	}

	_, err := Preprocess(pp, "/dev/null")
	if err == nil {
		t.Fatal("expected error for missing command")
	}
	if !strings.Contains(err.Error(), "preprocessor:") {
		t.Fatalf("expected preprocessor prefix in error, got: %v", err)
	}
}

func TestPreprocess_InvalidJSON(t *testing.T) {
	dir := t.TempDir()

	script := filepath.Join(dir, "bad.sh")
	writeScript(t, script, `echo "not valid json"`)

	pp := &Preprocessor{
		Extension: "star",
		Command:   []string{script},
	}

	_, err := Preprocess(pp, "/dev/null")
	if err == nil {
		t.Fatal("expected error for invalid JSON output")
	}
	if !strings.Contains(err.Error(), "invalid JSON output") {
		t.Fatalf("expected 'invalid JSON output' in error, got: %v", err)
	}
}

func TestPreprocess_EmptyCommand(t *testing.T) {
	pp := &Preprocessor{
		Extension: "star",
		Command:   []string{},
	}

	_, err := Preprocess(pp, "/dev/null")
	if err == nil {
		t.Fatal("expected error for empty command")
	}
}

// TestLoad_WithPreprocessor verifies the full integration: mu.json declares a
// preprocessor, and Load finds BUILD.<ext> files instead of BUILD.json.
func TestLoad_WithPreprocessor(t *testing.T) {
	root := t.TempDir()

	// Create a preprocessor script that cats its input.
	script := filepath.Join(root, "pp.sh")
	writeScript(t, script, `cat "$1"`)

	// mu.json with preprocessor configured.
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{
		Preprocessor: &Preprocessor{
			Extension: "star",
			Command:   []string{script},
		},
	})

	// A BUILD.star in a subdirectory (should be found).
	sub := filepath.Join(root, "pkg", "lib")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(sub, "BUILD.star"), ProjectConfig{
		Targets: []Target{
			{Name: "mylib", Toolchain: "go", Sources: []string{"*.go"}},
		},
	})

	// A BUILD.json in a different subdirectory (should be ignored when
	// preprocessor is active).
	other := filepath.Join(root, "pkg", "other")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(other, "BUILD.json"), ProjectConfig{
		Targets: []Target{
			{Name: "ignored", Toolchain: "go", Sources: []string{"*.go"}},
		},
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}

	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target (from BUILD.star), got %d", len(cfg.Targets))
	}
	if cfg.Targets[0].Name != "//pkg/lib/mylib" {
		t.Fatalf("expected //pkg/lib/mylib, got %s", cfg.Targets[0].Name)
	}
}

// TestLoad_NoPreprocessor_FallsThrough ensures that without a preprocessor,
// Load still finds BUILD.json as before.
func TestLoad_NoPreprocessor_FallsThrough(t *testing.T) {
	root := t.TempDir()

	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{})

	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(sub, "BUILD.json"), ProjectConfig{
		Targets: []Target{
			{Name: "lib", Toolchain: "go", Sources: []string{"*.go"}},
		},
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d", len(cfg.Targets))
	}
}

// TestLoad_PreprocessorError verifies that preprocessor failures propagate.
func TestLoad_PreprocessorError(t *testing.T) {
	root := t.TempDir()

	script := filepath.Join(root, "fail.sh")
	writeScript(t, script, `echo "build failed" >&2; exit 1`)

	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{
		Preprocessor: &Preprocessor{
			Extension: "star",
			Command:   []string{script},
		},
	})

	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "BUILD.star"), []byte("dummy"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Load(root)
	if err == nil {
		t.Fatal("expected error when preprocessor fails")
	}
	if !strings.Contains(err.Error(), "build failed") {
		t.Fatalf("expected stderr in error, got: %v", err)
	}
}
