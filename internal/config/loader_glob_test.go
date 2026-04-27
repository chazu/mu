package config

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// writeFile writes data to path, creating parent dirs as needed.
func writeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

// loadSources writes a mu.cue declaring a single target with the given
// sources, runs Load, and returns the resulting (expanded) sources slice.
func loadSources(t *testing.T, root string, sources []string) []string {
	t.Helper()
	cue := fmt.Sprintf(`
toolchains: [{toolchain: "noop", from: "scratch"}]
targets: [{target: "t", toolchain: "noop", sources: %s}]
`, cueStringList(sources))
	writeFile(t, filepath.Join(root, "mu.cue"), cue)
	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.Targets) != 1 {
		t.Fatalf("want 1 target, got %d", len(cfg.Targets))
	}
	out := append([]string(nil), cfg.Targets[0].Sources...)
	sort.Strings(out)
	return out
}

func TestExpandSourceGlobs_DoubleStarNested(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "src", "a.go"), "")
	writeFile(t, filepath.Join(root, "src", "sub", "b.go"), "")
	writeFile(t, filepath.Join(root, "src", "sub", "deep", "c.go"), "")
	writeFile(t, filepath.Join(root, "src", "sub", "not.txt"), "")

	got := loadSources(t, root, []string{"src/**/*.go"})
	want := []string{
		filepath.Join("src", "a.go"),
		filepath.Join("src", "sub", "b.go"),
		filepath.Join("src", "sub", "deep", "c.go"),
	}
	sort.Strings(want)
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandSourceGlobs_SingleStar(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "a.go"), "")
	writeFile(t, filepath.Join(root, "b.go"), "")
	writeFile(t, filepath.Join(root, "nested", "c.go"), "")

	got := loadSources(t, root, []string{"*.go"})
	want := []string{"a.go", "b.go"}
	sort.Strings(want)
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandSourceGlobs_UnmatchedLiteral(t *testing.T) {
	root := t.TempDir()
	got := loadSources(t, root, []string{"missing/*.go"})
	want := []string{"missing/*.go"}
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestExpandSourceGlobs_HiddenDirsExcluded(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "src", "a.go"), "")
	writeFile(t, filepath.Join(root, ".git", "hooks.go"), "")
	writeFile(t, filepath.Join(root, "src", ".cache", "stale.go"), "")

	got := loadSources(t, root, []string{"**/*.go"})
	want := []string{filepath.Join("src", "a.go")}
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
