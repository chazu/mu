package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// withDeprecationCapture swaps deprecationWriter for a buffer and resets
// the dedupe once, restoring both at test end.
func withDeprecationCapture(t *testing.T) *bytes.Buffer {
	t.Helper()
	prev := deprecationWriter
	buf := &bytes.Buffer{}
	deprecationWriter = buf
	resetDeprecationOnce()
	t.Cleanup(func() {
		deprecationWriter = prev
		resetDeprecationOnce()
	})
	return buf
}

// writeJSONProject writes a minimal but valid mu.json into a tempdir and
// returns the project root path.
func writeJSONProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	body := `{"targets":[],"toolchains":[],"plugins":[]}`
	if err := os.WriteFile(filepath.Join(dir, "mu.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("writing mu.json: %v", err)
	}
	return dir
}

func TestDeprecationWarning_EmittedOnJSONLoad(t *testing.T) {
	buf := withDeprecationCapture(t)
	t.Setenv(suppressDeprecationEnv, "")

	root := writeJSONProject(t)
	if _, err := Load(root); err != nil {
		t.Fatalf("Load: %v", err)
	}

	got := buf.String()
	if !strings.Contains(got, deprecationMessage) {
		t.Fatalf("expected stderr to contain %q, got %q", deprecationMessage, got)
	}
	// Single line, single newline.
	if strings.Count(got, "\n") != 1 {
		t.Fatalf("expected exactly one line of output, got %q", got)
	}
}

func TestDeprecationWarning_SuppressedByEnv(t *testing.T) {
	buf := withDeprecationCapture(t)
	t.Setenv(suppressDeprecationEnv, "1")

	root := writeJSONProject(t)
	if _, err := Load(root); err != nil {
		t.Fatalf("Load: %v", err)
	}

	if got := buf.String(); got != "" {
		t.Fatalf("expected no output when %s=1, got %q", suppressDeprecationEnv, got)
	}
}

func TestDeprecationWarning_OncePerProcess(t *testing.T) {
	buf := withDeprecationCapture(t)
	t.Setenv(suppressDeprecationEnv, "")

	root := writeJSONProject(t)
	if _, err := Load(root); err != nil {
		t.Fatalf("Load #1: %v", err)
	}
	if _, err := Load(root); err != nil {
		t.Fatalf("Load #2: %v", err)
	}

	got := buf.String()
	if c := strings.Count(got, deprecationMessage); c != 1 {
		t.Fatalf("expected warning exactly once across two loads, got %d in %q", c, got)
	}
}
