package staging

import (
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

var now = time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

func mkfile(t *testing.T, path string, size int, mtime time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func age(t *testing.T, path string, mtime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func exists(path string) bool { _, err := os.Lstat(path); return err == nil }

func TestSweepRemovesOnlyInactiveMatches(t *testing.T) {
	root := t.TempDir()
	old := now.Add(-48 * time.Hour)
	recent := now.Add(-time.Hour)

	staleDir := filepath.Join(root, "plugins", "go", ".bundle-111")
	mkfile(t, filepath.Join(staleDir, "plugin.bb"), 10, old)
	age(t, staleDir, old)

	// A long-running writer: the directory itself is old, but a file deep
	// inside was written recently, so the tree is still owned.
	liveDir := filepath.Join(root, "plugins", "go", ".bundle-222")
	mkfile(t, filepath.Join(liveDir, "a", "b", "big.bin"), 10, recent)
	age(t, filepath.Join(liveDir, "a", "b"), old)
	age(t, filepath.Join(liveDir, "a"), old)
	age(t, liveDir, old)

	published := filepath.Join(root, "plugins", "go", "bundle-333")
	mkfile(t, filepath.Join(published, "plugin.bb"), 10, old)
	age(t, published, old)

	rules := []Rule{{Dir: filepath.Join(root, "plugins"), Prefixes: []string{".bundle-"}, Depth: 1}}
	removed, err := Sweep(rules, MinAge, now, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(removed) != 1 || removed[0].Path != staleDir || removed[0].Bytes != 10 {
		t.Fatalf("removed = %+v", removed)
	}
	if exists(staleDir) || !exists(liveDir) || !exists(published) {
		t.Fatalf("stale=%v live=%v published=%v", exists(staleDir), exists(liveDir), exists(published))
	}
}

func TestSweepDryRunKeepsFiles(t *testing.T) {
	root := t.TempDir()
	p := filepath.Join(root, "mu-cas-replay-1")
	mkfile(t, p, 5, now.Add(-48*time.Hour))
	removed, err := Sweep([]Rule{{Dir: root, Prefixes: []string{"mu-cas-replay-"}}}, MinAge, now, true)
	if err != nil || len(removed) != 1 || !exists(p) {
		t.Fatalf("dry run removed=%+v err=%v exists=%v", removed, err, exists(p))
	}
}

func TestSweepRespectsDepthAndPrefixes(t *testing.T) {
	root := t.TempDir()
	old := now.Add(-48 * time.Hour)
	shallow := filepath.Join(root, ".tmp-a")
	deep := filepath.Join(root, "x", "y", ".tmp-b")
	tooDeep := filepath.Join(root, "x", "y", "z", ".tmp-c")
	other := filepath.Join(root, "x", "keep.txt")
	for _, p := range []string{shallow, deep, tooDeep, other} {
		mkfile(t, p, 1, old)
	}
	removed, err := Sweep([]Rule{{Dir: root, Prefixes: []string{".tmp-"}, Depth: 2}}, MinAge, now, false)
	if err != nil {
		t.Fatal(err)
	}
	var paths []string
	for _, r := range removed {
		paths = append(paths, r.Path)
	}
	sort.Strings(paths)
	if len(paths) != 2 || paths[0] != shallow || paths[1] != deep {
		t.Fatalf("removed %v", paths)
	}
	if !exists(tooDeep) || !exists(other) {
		t.Fatal("sweep exceeded its depth or prefix scope")
	}
}

func TestSweepRemovesReadOnlyTrees(t *testing.T) {
	root := t.TempDir()
	old := now.Add(-48 * time.Hour)
	dir := filepath.Join(root, "mu-sandbox-1")
	mkfile(t, filepath.Join(dir, "ro", "f"), 1, old)
	if err := os.Chmod(filepath.Join(dir, "ro"), 0o555); err != nil {
		t.Fatal(err)
	}
	age(t, filepath.Join(dir, "ro"), old)
	age(t, dir, old)
	t.Cleanup(func() { os.Chmod(filepath.Join(dir, "ro"), 0o755) })
	if _, err := Sweep([]Rule{{Dir: root, Prefixes: []string{"mu-sandbox-"}}}, MinAge, now, false); err != nil {
		t.Fatal(err)
	}
	if exists(dir) {
		t.Fatal("read-only staging tree not reclaimed")
	}
}

func TestSweepIgnoresMissingDirs(t *testing.T) {
	removed, err := Sweep(DefaultRules(filepath.Join(t.TempDir(), "nohome"), filepath.Join(t.TempDir(), "notmp")), MinAge, now, false)
	if err != nil || len(removed) != 0 {
		t.Fatalf("removed=%v err=%v", removed, err)
	}
}

func TestMaybeSweepIsRateLimited(t *testing.T) {
	home := t.TempDir()
	extract := filepath.Join(home, ".mu", "cache", "extract")
	first := filepath.Join(extract, ".extract-a")
	mkfile(t, filepath.Join(first, "f"), 1, now.Add(-48*time.Hour))
	age(t, first, now.Add(-48*time.Hour))

	MaybeSweep(home, now)
	if exists(first) {
		t.Fatal("first sweep did not run")
	}

	second := filepath.Join(extract, ".extract-b")
	mkfile(t, filepath.Join(second, "f"), 1, now.Add(-48*time.Hour))
	age(t, second, now.Add(-48*time.Hour))
	MaybeSweep(home, now.Add(10*time.Minute))
	if !exists(second) {
		t.Fatal("sweep ran again inside the rate-limit interval")
	}
	MaybeSweep(home, now.Add(Interval+time.Minute))
	if exists(second) {
		t.Fatal("sweep did not run after the interval elapsed")
	}
}
