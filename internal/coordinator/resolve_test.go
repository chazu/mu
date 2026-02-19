package coordinator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/plugin"
)

func TestResolveFileInputs(t *testing.T) {
	dir := t.TempDir()

	content := []byte("hello world\n")
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	specs := []plugin.ActionSpec{
		{
			ID:      "build",
			Command: []string{"cat", "input.txt"},
			Inputs:  map[string]string{"src": "input.txt"},
			Outputs: []string{"out.txt"},
		},
	}

	actions, err := Resolve(specs, dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}

	a := actions[0]
	if a.ID != "build" {
		t.Errorf("ID = %q, want %q", a.ID, "build")
	}
	if a.WorkDir != dir {
		t.Errorf("WorkDir = %q, want %q", a.WorkDir, dir)
	}

	dgst, ok := a.Inputs["src"]
	if !ok {
		t.Fatal("missing input key 'src'")
	}
	if dgst.IsZero() {
		t.Fatal("expected non-zero digest for file input")
	}
	if dgst.Algorithm != "sha256" {
		t.Errorf("Algorithm = %q, want %q", dgst.Algorithm, "sha256")
	}

	// Verify the digest matches a fresh computation.
	f, _ := os.Open(filepath.Join(dir, "input.txt"))
	defer f.Close()
	want, _ := cas.ComputeDigest(f)
	if dgst != want {
		t.Errorf("digest = %v, want %v", dgst, want)
	}
}

func TestResolveActionRef(t *testing.T) {
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "link",
			Command: []string{"ld", "-o", "out"},
			Inputs:  map[string]string{"obj": "{action:compile}"},
			Outputs: []string{"out"},
		},
	}

	actions, err := Resolve(specs, dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}

	dgst := actions[0].Inputs["obj"]
	if !dgst.IsZero() {
		t.Errorf("expected zero digest for action ref, got %v", dgst)
	}
}

func TestResolveMixedInputs(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	specs := []plugin.ActionSpec{
		{
			ID:      "compile",
			Command: []string{"go", "build"},
			Inputs: map[string]string{
				"source": "main.go",
				"stdlib": "{action:toolchain}",
			},
			Outputs:   []string{"main"},
			DependsOn: []string{"toolchain"},
			Env:       map[string]string{"GOOS": "linux"},
			Network:   true,
		},
	}

	actions, err := Resolve(specs, dir)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	a := actions[0]

	// File input should have a real digest.
	if a.Inputs["source"].IsZero() {
		t.Error("expected non-zero digest for file input 'source'")
	}
	// Action ref should have a zero digest.
	if !a.Inputs["stdlib"].IsZero() {
		t.Errorf("expected zero digest for action ref 'stdlib', got %v", a.Inputs["stdlib"])
	}

	// Verify other fields are copied.
	if len(a.DependsOn) != 1 || a.DependsOn[0] != "toolchain" {
		t.Errorf("DependsOn = %v, want [toolchain]", a.DependsOn)
	}
	if a.Env["GOOS"] != "linux" {
		t.Errorf("Env[GOOS] = %q, want %q", a.Env["GOOS"], "linux")
	}
	if !a.Network {
		t.Error("Network = false, want true")
	}
}

func TestResolveMissingFile(t *testing.T) {
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "build",
			Command: []string{"cat"},
			Inputs:  map[string]string{"src": "nonexistent.txt"},
		},
	}

	_, err := Resolve(specs, dir)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}

	// Error should mention the action ID and input name.
	errMsg := err.Error()
	if !contains(errMsg, "build") || !contains(errMsg, "src") {
		t.Errorf("error missing context, got: %s", errMsg)
	}
}

func TestResolvePathTraversal(t *testing.T) {
	dir := t.TempDir()

	cases := []string{
		"../../../etc/passwd",
		"../outside",
		"subdir/../../outside",
	}

	for _, input := range cases {
		specs := []plugin.ActionSpec{
			{
				ID:      "build",
				Command: []string{"cat"},
				Inputs:  map[string]string{"src": input},
			},
		}

		_, err := Resolve(specs, dir)
		if err == nil {
			t.Errorf("expected error for traversal input %q, got nil", input)
			continue
		}
		if !contains(err.Error(), "escapes project root") {
			t.Errorf("expected 'escapes project root' error for %q, got: %s", input, err.Error())
		}
	}
}

func TestResolveEmptySpecs(t *testing.T) {
	actions, err := Resolve(nil, t.TempDir())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("got %d actions, want 0", len(actions))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
