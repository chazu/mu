package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/plugincatalog"
)

// installFakePlugin creates <plugins>/<name>/bundle-<hash>/ with an entrypoint
// and sets its modification time.
func installFakePlugin(t *testing.T, pluginsDir, name, hash string, mtime time.Time) string {
	t.Helper()
	dir := filepath.Join(pluginsDir, name, "bundle-"+hash)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.sh"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mu-plugin.json"), []byte(`{"entrypoint":"plugin.sh"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(dir, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return dir
}

func exists(path string) bool { _, err := os.Stat(path); return err == nil }

// Legacy 12-hex entries are never versions: resolution ignores them (so a
// full version next to one is unambiguous) and clean deletes them.
func TestCacheCleanPrunesLegacyPluginEntries(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	plugins := filepath.Join(home, ".mu", "plugins")
	full := strings.Repeat("a", 64)
	now := time.Now()
	fullDir := installFakePlugin(t, plugins, "aws", full, now)
	legacyDir := installFakePlugin(t, plugins, "aws", "e6a95fab5a97", now)
	legacyFile := filepath.Join(plugins, "old", "plugin-00a54e046d93.sh")
	if err := os.MkdirAll(filepath.Dir(legacyFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyFile, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	if _, digest, err := resolveCachedPlugin(plugins, "aws", ""); err != nil || digest.Hash != full {
		t.Fatalf("resolution considered a legacy entry: %s, %v", digest, err)
	}
	if _, _, err := resolveCachedPlugin(plugins, "old", ""); err == nil {
		t.Fatal("a legacy-only plugin resolved")
	}

	if code := captureBuildCode(t, func() int { return runCacheClean([]string{"--dry-run"}) }); code != exitOK {
		t.Fatalf("dry run exit = %d", code)
	}
	if !exists(legacyDir) || !exists(legacyFile) {
		t.Fatal("--dry-run removed legacy entries")
	}
	if code := captureBuildCode(t, func() int { return runCacheClean(nil) }); code != exitOK {
		t.Fatalf("clean exit = %d", code)
	}
	if exists(legacyDir) || exists(legacyFile) {
		t.Fatal("clean kept legacy entries")
	}
	if !exists(fullDir) {
		t.Fatal("clean removed a full-digest version outside any project")
	}
	if exists(filepath.Dir(legacyFile)) {
		t.Fatal("clean left an empty plugin directory")
	}
}

// In a project, clean drops the versions of its locked plugins it does not
// pin, leaves other plugins alone, and with --all-plugins keeps only their
// newest version.
func TestCacheCleanPrunesUnpinnedPluginVersions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "mu.cue"), []byte("package mu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	a, b, c, d := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64), strings.Repeat("d", 64)
	lock := plugincatalog.NewLock()
	lock.Upsert(plugincatalog.LockedPlugin{Name: "aws", Version: "1", SourceRevision: "r", AssetSHA256: a, BundleDigest: "sha256:" + a})
	if err := plugincatalog.WriteLock(filepath.Join(project, "mu.lock"), lock); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	plugins := filepath.Join(home, ".mu", "plugins")
	old := time.Now().Add(-time.Hour)
	pinned := installFakePlugin(t, plugins, "aws", a, old)
	unpinned := installFakePlugin(t, plugins, "aws", b, time.Now())
	otherOld := installFakePlugin(t, plugins, "other", c, old)
	otherNew := installFakePlugin(t, plugins, "other", d, time.Now())

	if code := captureBuildCode(t, func() int { return runCacheClean(nil) }); code != exitOK {
		t.Fatalf("clean exit = %d", code)
	}
	if !exists(pinned) || exists(unpinned) {
		t.Fatalf("pinned kept=%v, unpinned removed=%v", exists(pinned), !exists(unpinned))
	}
	if !exists(otherOld) || !exists(otherNew) {
		t.Fatal("clean touched a plugin the project does not use")
	}

	if code := captureBuildCode(t, func() int { return runCacheClean([]string{"--all-plugins"}) }); code != exitOK {
		t.Fatalf("clean --all-plugins exit = %d", code)
	}
	if exists(otherOld) || !exists(otherNew) || !exists(pinned) {
		t.Fatalf("--all-plugins: old=%v new=%v pinned=%v", exists(otherOld), exists(otherNew), exists(pinned))
	}
}

// clean takes the cache lock exclusively, so it waits for a build holding it
// shared, and finishes once the build releases it.
func TestCacheCleanWaitsForSharedLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	cacheDir := filepath.Join(home, ".mu", "cache")
	held, err := cas.LockShared(cacheDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	// A second shared holder never waits.
	waited := false
	other, err := cas.LockShared(cacheDir, func() { waited = true })
	if err != nil || waited {
		t.Fatalf("shared lock conflicted with shared lock: waited=%v err=%v", waited, err)
	}
	other.Release()

	done := make(chan int, 1)
	go func() { done <- captureBuildCode(t, func() int { return runCacheClean(nil) }) }()
	select {
	case code := <-done:
		t.Fatalf("clean finished (exit %d) while a build held the cache", code)
	case <-time.After(300 * time.Millisecond):
	}
	held.Release()
	select {
	case code := <-done:
		if code != exitOK {
			t.Fatalf("clean exit = %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("clean did not finish after the shared lock was released")
	}
}
