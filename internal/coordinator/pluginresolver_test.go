package coordinator

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
)

func TestExtractDirFromCAS_RejectsIncompleteAndUnsupportedBundles(t *testing.T) {
	for _, kind := range []string{"truncated", "symlink", "duplicate", "traversal"} {
		t.Run(kind, func(t *testing.T) {
			var buf bytes.Buffer
			tw := tar.NewWriter(&buf)
			if err := tw.WriteHeader(&tar.Header{Name: "ok.sh", Size: 2, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("ok")); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "truncated":
				if err := tw.WriteHeader(&tar.Header{Name: "partial", Size: 100}); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := tw.WriteHeader(&tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "outside"}); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				if err := tw.WriteHeader(&tar.Header{Name: "./ok.sh"}); err != nil {
					t.Fatal(err)
				}
			case "traversal":
				if err := tw.WriteHeader(&tar.Header{Name: "../escape"}); err != nil {
					t.Fatal(err)
				}
			}
			if kind != "truncated" {
				if err := tw.Close(); err != nil {
					t.Fatal(err)
				}
			}
			store := newTestStore(t)
			digest, err := store.Put(context.Background(), bytes.NewReader(buf.Bytes()))
			if err != nil {
				t.Fatal(err)
			}
			r := &PluginResolver{Store: store, CacheDir: t.TempDir()}
			for range 2 {
				if _, err := r.extractDirFromCAS(context.Background(), "test", digest); err == nil {
					t.Fatal("invalid bundle accepted")
				}
			}
			entries, err := os.ReadDir(filepath.Join(r.CacheDir, "test"))
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatalf("failed extraction left cache entries: %v", entries)
			}
		})
	}
}

func TestExtractDirFromCAS_ConcurrentPublicationPreservesOldBundles(t *testing.T) {
	store := newTestStore(t)
	r := &PluginResolver{Store: store, CacheDir: t.TempDir()}
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: "name..sh", Size: 2, Mode: 0o755}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write([]byte("ok")); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	digest, err := store.Put(context.Background(), &buf)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(r.CacheDir, "test", "bundle-old", "in-use")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			dir, err := r.extractDirFromCAS(context.Background(), "test", digest)
			if err != nil {
				t.Error(err)
				return
			}
			if !strings.HasSuffix(dir, digest.Hash) {
				t.Error("cache path does not use full digest")
			}
			data, err := os.ReadFile(filepath.Join(dir, "name..sh"))
			if err != nil || string(data) != "ok" {
				t.Errorf("incomplete published bundle: %q, %v", data, err)
			}
		})
	}
	wg.Wait()
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("removed in-use bundle: %v", err)
	}
}

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
	if err := os.WriteFile(filepath.Join(pluginDir, "mu.cue"), []byte(`
plugin: {entrypoint: "run.sh"}
`), 0o644); err != nil {
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

func TestResolveDigestDirectoryBundle(t *testing.T) {
	store := newTestStore(t)
	projectRoot := t.TempDir()
	cacheDir := t.TempDir()
	pluginDir := filepath.Join(projectRoot, "plugins", "bundle")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "mu.cue"), []byte(`plugin: {entrypoint: "plugin.bb"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginDir, "plugin.bb"), []byte("#!/usr/bin/env bb\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := &PluginResolver{Store: store, ProjectRoot: projectRoot, CacheDir: cacheDir}
	local, err := r.resolveOne(context.Background(), config.PluginDef{Name: "bundle", Script: "plugins/bundle"})
	if err != nil {
		t.Fatalf("resolve local bundle: %v", err)
	}
	digest, err := r.resolveOne(context.Background(), config.PluginDef{Name: "bundle", Digest: local.Digest.String()})
	if err != nil {
		t.Fatalf("resolve digest bundle: %v", err)
	}
	if digest.Def.WorkDir == "" {
		t.Fatal("digest directory plugin should retain WorkDir")
	}
	if filepath.Base(digest.Def.Script) != "plugin.bb" {
		t.Fatalf("digest entrypoint = %q, want plugin.bb", digest.Def.Script)
	}
}

func TestResolveLocalDir_MissingManifest(t *testing.T) {
	store := newTestStore(t)
	cacheDir := t.TempDir()
	projectRoot := t.TempDir()

	// Directory without mu.cue.
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
	if err := os.WriteFile(filepath.Join(pluginDir, "mu.cue"), []byte(`
plugin: {entrypoint: "nonexistent.sh"}
`), 0o644); err != nil {
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

	d1, err := r.bundleDir(context.Background(), dir, nil)
	if err != nil {
		t.Fatalf("first bundle: %v", err)
	}
	d2, err := r.bundleDir(context.Background(), dir, nil)
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

	dgst, err := r.bundleDir(context.Background(), dir, nil)
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

	dgst, err := r.bundleDir(context.Background(), dir, nil)
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

func TestSingleFilePublicationUsesFullDigestAndRetainsOldVersions(t *testing.T) {
	store := newTestStore(t)
	resolver := &PluginResolver{Store: store, CacheDir: t.TempDir()}
	first, err := store.Put(context.Background(), strings.NewReader("#!/bin/sh\nprintf first\n"))
	if err != nil {
		t.Fatal(err)
	}
	path, err := resolver.ExtractFile(context.Background(), "one", first, "https://example.invalid/plugin.sh?revision=1")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "plugin-"+first.Hash+".sh" {
		t.Fatalf("not full digest: %s", path)
	}
	second, err := store.Put(context.Background(), strings.NewReader("#!/bin/sh\nprintf second\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.ExtractFile(context.Background(), "one", second, "plugin.sh"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("removed an in-use old version", err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 16)
	for i := 0; i < 16; i++ {
		wg.Go(func() {
			_, err := resolver.ExtractFile(context.Background(), "concurrent", first, "plugin.sh")
			failures <- err
		})
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(resolver.CacheDir, "concurrent"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("incomplete publication: %v, %v", entries, err)
	}
}

type failedExtractionStore struct {
	cas.Store
	closeError bool
}

func (s failedExtractionStore) Get(context.Context, cas.Digest) (io.ReadCloser, error) {
	return failedExtractionReader{Reader: strings.NewReader("partial"), closeError: s.closeError}, nil
}

type failedExtractionReader struct {
	io.Reader
	closeError bool
}

func (r failedExtractionReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && !r.closeError {
		return n, fmt.Errorf("interrupted transfer")
	}
	return n, err
}
func (r failedExtractionReader) Close() error {
	if r.closeError {
		return fmt.Errorf("close failure")
	}
	return nil
}

func TestSingleFilePublicationDoesNotAdvertiseFailedTransfers(t *testing.T) {
	for _, closeError := range []bool{false, true} {
		resolver := &PluginResolver{Store: failedExtractionStore{closeError: closeError}, CacheDir: t.TempDir()}
		digest, err := cas.ComputeDigest(strings.NewReader("partial"))
		if err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			if _, err := resolver.ExtractFile(context.Background(), "failed", digest, "plugin.sh"); err == nil {
				t.Fatal("accepted failed extraction")
			}
			entries, err := os.ReadDir(filepath.Join(resolver.CacheDir, "failed"))
			if err != nil || len(entries) != 0 {
				t.Fatalf("left partial cache entries: %v, %v", entries, err)
			}
		}
	}
}
