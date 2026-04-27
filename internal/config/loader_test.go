package config

import (
	"fmt"
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

func TestFindProjectRoot_Direct(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "mu.cue"), minimalCueRoot("//app"))

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
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//app"))

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
	dir := t.TempDir() // no mu.cue here
	_, err := FindProjectRoot(dir)
	if err == nil {
		t.Fatal("expected error when mu.cue is absent")
	}
}

// targetCue returns a CUE struct literal for one target with the given
// fields. Sources are rendered as a CUE list.
func targetCue(name, toolchain string, sources []string) string {
	return fmt.Sprintf(`{target: %q, toolchain: %q, sources: %s}`, name, toolchain, cueStringList(sources))
}

func cueStringList(xs []string) string {
	if len(xs) == 0 {
		return "[]"
	}
	parts := make([]string, len(xs))
	for i, x := range xs {
		parts[i] = fmt.Sprintf("%q", x)
	}
	return "[" + strings.Join(parts, ", ") + "]"
}

func TestLoad_Basic(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), `
toolchains: [{toolchain: "go", from: "scratch"}]
plugins: [{name: "go", command: ["mu-plugin-go"]}]
targets: [{target: "//app", toolchain: "go", sources: ["*.go"]}]
`)

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

func TestLoad_MergeSubdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root-target"))

	sub := filepath.Join(root, "pkg", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "mu.cue"), `
toolchains: [{toolchain: "go", from: "scratch"}]
targets: [{target: "server", toolchain: "go", sources: ["*.go"]}]
`)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(cfg.Targets))
	}

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

func TestLoad_MultipleSubdir(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root-target"))

	for _, pkg := range []string{"alpha", "beta", "gamma"} {
		dir := filepath.Join(root, pkg)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "mu.cue"), `
toolchains: [{toolchain: "go", from: "scratch"}]
targets: [{target: "lib", toolchain: "go", sources: ["*.go"]}]
`)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Root target + 3 subdir targets.
	if len(cfg.Targets) != 4 {
		t.Fatalf("expected 4 targets, got %d", len(cfg.Targets))
	}
}

func TestLoad_AlreadyPrefixedTarget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))

	sub := filepath.Join(root, "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "mu.cue"), `
toolchains: [{toolchain: "go", from: "scratch"}]
targets: [{target: "//explicit/name", toolchain: "go", sources: ["*.go"]}]
`)

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	var found bool
	for _, tgt := range cfg.Targets {
		if tgt.Name == "//explicit/name" {
			found = true
		}
	}
	if !found {
		t.Fatalf("already-prefixed target should not be modified, targets=%+v", cfg.Targets)
	}
}

func TestLoad_SkipsSymlinks(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))

	real := filepath.Join(root, "real")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(real, "mu.cue"), `
toolchains: [{toolchain: "go", from: "scratch"}]
targets: [{target: "lib", toolchain: "go", sources: ["*.go"]}]
`)

	symDir := filepath.Join(root, "linked")
	if err := os.Symlink(real, symDir); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// Root target + lib in real (symlinked dir is skipped).
	if len(cfg.Targets) != 2 {
		t.Fatalf("expected 2 targets (symlinked dir should be skipped), got %d", len(cfg.Targets))
	}
}

func TestLoad_SkipsSymlinkedFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))

	outside := t.TempDir()
	writeFile(t, filepath.Join(outside, "mu.cue"), `
toolchains: [{toolchain: "go", from: "scratch"}]
targets: [{target: "injected", toolchain: "go", sources: ["*.go"]}]
`)

	sub := filepath.Join(root, "sub")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "mu.cue"), filepath.Join(sub, "mu.cue")); err != nil {
		t.Skipf("symlinks not supported: %v", err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("expected 1 target (symlinked file should be skipped), got %d", len(cfg.Targets))
	}
}

func TestLoad_SkipsHiddenAndTestdata(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "mu.cue"), minimalCueRoot("//root"))

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
