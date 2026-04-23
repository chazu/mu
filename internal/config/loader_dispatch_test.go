package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// minimalCueRoot returns a minimal valid #ProjectConfig mu.cue body that
// satisfies schema.cue and yields the given target name.
func minimalCueRoot(targetName string) string {
	return `toolchains: [
  {toolchain: "go", from: "scratch"},
]
targets: [
  {target: "` + targetName + `", toolchain: "go", sources: ["main.go"]},
]
`
}

// TestLoad_DispatchCueOnly verifies AC (a): when only mu.cue is present in
// the project root, Load reads it via the CUE decoder.
func TestLoad_DispatchCueOnly(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//app"))

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "//app" {
		t.Fatalf("expected single //app target, got %#v", cfg.Targets)
	}
	if cfg.Targets[0].Toolchain != "go" {
		t.Fatalf("expected toolchain go, got %q", cfg.Targets[0].Toolchain)
	}
}

// TestLoad_DispatchJSONOnly verifies AC (b): when only mu.json is present,
// Load reads it via the JSON decoder (unchanged legacy path).
func TestLoad_DispatchJSONOnly(t *testing.T) {
	root := t.TempDir()
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"main.go"}},
		},
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 || cfg.Targets[0].Name != "//app" {
		t.Fatalf("expected single //app target, got %#v", cfg.Targets)
	}
}

// TestLoad_DispatchBothPresentErrors verifies AC (c): when both mu.cue
// and mu.json exist in the same directory, Load errors with a message
// referencing `mu migrate`.
func TestLoad_DispatchBothPresentErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//app"))
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{})

	_, err := Load(root)
	if err == nil {
		t.Fatal("expected error when both mu.cue and mu.json are present")
	}
	if !strings.Contains(err.Error(), "both mu.cue and mu.json") {
		t.Errorf("error %q does not mention both filenames", err.Error())
	}
	if !strings.Contains(err.Error(), "mu migrate") {
		t.Errorf("error %q does not point at `mu migrate`", err.Error())
	}
}

// TestFindProjectRoot_BothPresentErrors mirrors the Load-level check:
// walking up the tree from inside a directory that has both config files
// must surface the conflict error immediately.
func TestFindProjectRoot_BothPresentErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//app"))
	writeJSON(t, filepath.Join(root, "mu.json"), ProjectConfig{})

	child := filepath.Join(root, "sub")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	_, err := FindProjectRoot(child)
	if err == nil {
		t.Fatal("expected error when both mu.cue and mu.json present in ancestor")
	}
	if !strings.Contains(err.Error(), "mu migrate") {
		t.Errorf("error %q does not point at `mu migrate`", err.Error())
	}
}

// TestFindProjectRoot_FindsCue verifies FindProjectRoot locates a project
// whose root is marked only by mu.cue.
func TestFindProjectRoot_FindsCue(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//app"))

	child := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	got, err := FindProjectRoot(child)
	if err != nil {
		t.Fatalf("FindProjectRoot: %v", err)
	}
	if got != root {
		t.Fatalf("got %s, want %s", got, root)
	}
}

// TestLoad_MixedTreeCueRootJsonSubdir verifies AC (d): a mu.cue project
// root correctly merges a mu.json partial in a subdirectory, applying
// the same prefix/rebase rules as the all-JSON path.
func TestLoad_MixedTreeCueRootJsonSubdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root-target"))

	sub := filepath.Join(root, "pkg", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(sub, "mu.json"), ProjectConfig{
		Targets: []Target{
			{Name: "server", Toolchain: "go", Sources: []string{"main.go"}},
		},
	})

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 2 {
		names := make([]string, len(cfg.Targets))
		for i, t := range cfg.Targets {
			names[i] = t.Name
		}
		t.Fatalf("expected 2 targets, got %d: %v", len(cfg.Targets), names)
	}
	var sawPrefixed bool
	for _, tgt := range cfg.Targets {
		if tgt.Name == "//pkg/web/server" {
			sawPrefixed = true
		}
	}
	if !sawPrefixed {
		t.Fatalf("subdir mu.json target was not prefixed; targets=%+v", cfg.Targets)
	}
}

// TestLoad_MixedTreeBothInSubdirErrors verifies that the "both present"
// error is also raised when a subdirectory contains both files.
func TestLoad_MixedTreeBothInSubdirErrors(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root-target"))

	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "mu.cue"), `toolchains: [{toolchain: "go", from: "scratch"}]
targets: [{target: "lib", toolchain: "go", sources: ["x.go"]}]
`)
	writeJSON(t, filepath.Join(sub, "mu.json"), ProjectConfig{})

	_, err := Load(root)
	if err == nil {
		t.Fatal("expected error when subdir has both mu.cue and mu.json")
	}
	if !strings.Contains(err.Error(), "mu migrate") {
		t.Errorf("error %q does not mention `mu migrate`", err.Error())
	}
}

// TestLoad_CueRootSkipsHiddenAndTestdata verifies AC (e): the walker
// still skips hidden directories and testdata/ when the root is mu.cue.
// A mu.cue planted under .hidden/ or testdata/ must NOT be merged.
func TestLoad_CueRootSkipsHiddenAndTestdata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root-target"))

	// A subdir config that would fail schema if loaded. We use it as a
	// tripwire: if the walker descends into these, the error propagates
	// and the test fails with a descriptive message.
	bad := `targets: [{target: "!not-a-label!"}]`

	writeFile(t, filepath.Join(root, ".hidden", "mu.cue"), bad)
	writeFile(t, filepath.Join(root, "testdata", "mu.cue"), bad)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v — walker likely descended into hidden/testdata", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target (root only), got %d", len(cfg.Targets))
	}
}

// TestLoad_CueRootSkipsSymlinkedSubdirConfig verifies AC (e): a
// symlinked mu.cue or mu.json inside the tree is ignored (not followed),
// mirroring the existing JSON-root behavior.
func TestLoad_CueRootSkipsSymlinkedSubdirConfig(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root-target"))

	outside := t.TempDir()
	writeJSON(t, filepath.Join(outside, "mu.json"), ProjectConfig{
		Targets: []Target{
			{Name: "injected", Toolchain: "go", Sources: []string{"x.go"}},
		},
	})

	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "mu.json"), filepath.Join(sub, "mu.json")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target, got %d (symlinked mu.json should have been skipped)", len(cfg.Targets))
	}
}
