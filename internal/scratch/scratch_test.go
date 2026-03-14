package scratch

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chau/mu/internal/cas/oci"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

// makeTarGz creates a .tar.gz archive in memory with the given files.
// files is a map of path -> content.
func makeTarGz(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	for name, content := range files {
		hdr := &tar.Header{
			Name: name,
			Mode: 0o755,
			Size: int64(len(content)),
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatalf("tar write header %s: %v", name, err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatalf("tar write %s: %v", name, err)
		}
	}

	if err := tw.Close(); err != nil {
		t.Fatalf("tar close: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	return buf.Bytes()
}

// makeZip creates a .zip archive in memory with the given files.
func makeZip(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)

	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("zip create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatalf("zip write %s: %v", name, err)
		}
	}

	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// fakeVerifyBinary creates a script at extractDir/bin/<name> that exits 0
// when called with --version.
func fakeVerifyBinary(t *testing.T, extractDir, name string) {
	t.Helper()
	binDir := filepath.Join(extractDir, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}

	var script string
	if runtime.GOOS == "windows" {
		script = "@echo off\necho fake 1.0.0\n"
	} else {
		script = "#!/bin/sh\necho fake 1.0.0\n"
	}

	binPath := filepath.Join(binDir, name)
	if err := os.WriteFile(binPath, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func newTestBuilder(t *testing.T) (*Builder, string) {
	t.Helper()
	dir := t.TempDir()
	casDir := filepath.Join(dir, "cas")
	store, err := oci.NewLocal(casDir)
	if err != nil {
		t.Fatal(err)
	}
	registry := coordinator.NewToolchainRegistry(store)
	return &Builder{
		Store:    store,
		Registry: registry,
		CacheDir: filepath.Join(dir, "cache"),
	}, dir
}

func TestBuild_FetchExtractTarGzWithStripPrefix(t *testing.T) {
	// Create a tar.gz with a top-level directory to strip.
	archive := makeTarGz(t, map[string]string{
		"mygo-1.0/bin/go":       "#!/bin/sh\necho fake 1.0.0\n",
		"mygo-1.0/lib/stuff.so": "shared-lib-content",
	})
	hash := sha256Hex(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	b, dir := newTestBuilder(t)

	cfg := &config.ProjectConfig{
		Toolchains: []config.Toolchain{{
			Name: "go",
			From: "go-plugin",
			Config: config.ToolchainConfig{
				Version:     "1.0",
				URL:         srv.URL + "/go-1.0.tar.gz",
				SHA256:      hash,
				StripPrefix: "mygo-1.0",
			},
		}},
	}

	// Create a real verify binary since tar extraction will extract the script.
	// Actually, the extracted script won't have execute permission correctly on
	// all platforms from tar, so pre-create it.
	extractDir := filepath.Join(dir, "cache", "extract", "go-1.0")
	fakeVerifyBinary(t, extractDir, "go")

	err := b.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Verify manifest was registered.
	manifest, err := b.Registry.Lookup(context.Background(), "go", "1.0")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if manifest == nil {
		t.Fatal("expected manifest, got nil")
	}
	if manifest.Name != "go" {
		t.Errorf("manifest name = %q, want %q", manifest.Name, "go")
	}
	if manifest.Version != "1.0" {
		t.Errorf("manifest version = %q, want %q", manifest.Version, "1.0")
	}
	if len(manifest.Artifacts) == 0 {
		t.Error("expected artifacts, got none")
	}
}

func TestBuild_ZipExtraction(t *testing.T) {
	archive := makeZip(t, map[string]string{
		"bin/mytool":  "#!/bin/sh\necho mytool 2.0\n",
		"lib/lib.txt": "library",
	})
	hash := sha256Hex(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	b, dir := newTestBuilder(t)

	cfg := &config.ProjectConfig{
		Toolchains: []config.Toolchain{{
			Name: "mytool",
			From: "mytool-plugin",
			Config: config.ToolchainConfig{
				Version: "2.0",
				URL:     srv.URL + "/mytool-2.0.zip",
				SHA256:  hash,
			},
		}},
	}

	// Create verify binary.
	extractDir := filepath.Join(dir, "cache", "extract", "mytool-2.0")
	fakeVerifyBinary(t, extractDir, "mytool")

	err := b.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	manifest, err := b.Registry.Lookup(context.Background(), "mytool", "2.0")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if manifest == nil {
		t.Fatal("expected manifest, got nil")
	}
	if _, ok := manifest.Artifacts["lib/lib.txt"]; !ok {
		t.Error("expected lib/lib.txt in artifacts")
	}
}

func TestBuild_SHA256VerificationFail(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"bin/tool": "content",
	})
	wrongHash := sha256Hex([]byte("not the archive"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	b, _ := newTestBuilder(t)

	cfg := &config.ProjectConfig{
		Toolchains: []config.Toolchain{{
			Name: "tool",
			From: "plugin",
			Config: config.ToolchainConfig{
				Version: "1.0",
				URL:     srv.URL + "/tool.tar.gz",
				SHA256:  wrongHash,
			},
		}},
	}

	err := b.Build(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected error for SHA256 mismatch")
	}
	if !strings.Contains(err.Error(), "mismatch") {
		t.Errorf("expected error to mention 'mismatch', got: %v", err)
	}
}

func TestBuild_CacheHitSkipsFetch(t *testing.T) {
	var fetchCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		w.Write([]byte("should not be called"))
	}))
	defer srv.Close()

	b, _ := newTestBuilder(t)

	// Pre-register a manifest so it's a cache hit.
	manifest := &coordinator.ToolchainManifest{
		Name:    "cached",
		Version: "3.0",
		Artifacts: map[string]string{
			"bin/cached": "sha256:abc123",
		},
	}
	if err := b.Registry.Register(context.Background(), manifest); err != nil {
		t.Fatalf("pre-register: %v", err)
	}

	cfg := &config.ProjectConfig{
		Toolchains: []config.Toolchain{{
			Name: "cached",
			From: "plugin",
			Config: config.ToolchainConfig{
				Version: "3.0",
				URL:     srv.URL + "/cached.tar.gz",
				SHA256:  "doesntmatter",
			},
		}},
	}

	err := b.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if c := fetchCount.Load(); c != 0 {
		t.Errorf("expected 0 fetches (cache hit), got %d", c)
	}
}

func TestBuild_NetworkRetry(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"bin/retrytool": "#!/bin/sh\necho retrytool 1.0\n",
	})
	hash := sha256Hex(archive)

	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(archive)
	}))
	defer srv.Close()

	b, dir := newTestBuilder(t)

	cfg := &config.ProjectConfig{
		Toolchains: []config.Toolchain{{
			Name: "retrytool",
			From: "plugin",
			Config: config.ToolchainConfig{
				Version: "1.0",
				URL:     srv.URL + "/retrytool.tar.gz",
				SHA256:  hash,
			},
		}},
	}

	extractDir := filepath.Join(dir, "cache", "extract", "retrytool-1.0")
	fakeVerifyBinary(t, extractDir, "retrytool")

	err := b.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if c := count.Load(); c < 2 {
		t.Errorf("expected at least 2 requests (1 fail + 1 success), got %d", c)
	}
}

func TestBuild_VersionChangeTriggersFetch(t *testing.T) {
	archiveV1 := makeTarGz(t, map[string]string{
		"bin/tool": "#!/bin/sh\necho v1\n",
	})
	archiveV2 := makeTarGz(t, map[string]string{
		"bin/tool":   "#!/bin/sh\necho v2\n",
		"lib/new.so": "new-in-v2",
	})

	b, dir := newTestBuilder(t)

	// Pre-register v1 manifest.
	v1Manifest := &coordinator.ToolchainManifest{
		Name:    "tool",
		Version: "1.0",
		Artifacts: map[string]string{
			"bin/tool": "sha256:olddigest",
		},
	}
	if err := b.Registry.Register(context.Background(), v1Manifest); err != nil {
		t.Fatalf("pre-register v1: %v", err)
	}

	// Serve v2 archive.
	hashV2 := sha256Hex(archiveV2)
	var fetchCount atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetchCount.Add(1)
		w.Write(archiveV2)
	}))
	defer srv.Close()

	cfg := &config.ProjectConfig{
		Toolchains: []config.Toolchain{{
			Name: "tool",
			From: "plugin",
			Config: config.ToolchainConfig{
				Version: "2.0", // different version -> should re-fetch
				URL:     srv.URL + "/tool-2.0.tar.gz",
				SHA256:  hashV2,
			},
		}},
	}

	extractDir := filepath.Join(dir, "cache", "extract", "tool-2.0")
	fakeVerifyBinary(t, extractDir, "tool")

	err := b.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if c := fetchCount.Load(); c == 0 {
		t.Error("expected fetch for new version, but none occurred")
	}

	manifest, err := b.Registry.Lookup(context.Background(), "tool", "2.0")
	if err != nil {
		t.Fatalf("Lookup v2: %v", err)
	}
	if manifest == nil {
		t.Fatal("expected v2 manifest, got nil")
	}
	if manifest.Version != "2.0" {
		t.Errorf("version = %q, want %q", manifest.Version, "2.0")
	}

	_ = archiveV1 // used for symmetry; v1 was pre-registered, not fetched
}

func TestBuild_VerifyFailure(t *testing.T) {
	// Archive where the "binary" is not executable / doesn't work.
	archive := makeTarGz(t, map[string]string{
		"bin/badtool": "this is not a real binary",
	})
	hash := sha256Hex(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	b, _ := newTestBuilder(t)

	cfg := &config.ProjectConfig{
		Toolchains: []config.Toolchain{{
			Name: "badtool",
			From: "plugin",
			Config: config.ToolchainConfig{
				Version: "1.0",
				URL:     srv.URL + "/badtool.tar.gz",
				SHA256:  hash,
			},
		}},
	}

	// Don't create a fake binary -- let it fail verification.
	err := b.Build(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected verification error")
	}
	if !strings.Contains(err.Error(), "verify") {
		t.Errorf("expected error to mention 'verify', got: %v", err)
	}
}

func TestBuild_ManifestPersistsToCAS(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"bin/persist": "#!/bin/sh\necho persist 1.0\n",
		"data.txt":   "some data",
	})
	hash := sha256Hex(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	b, dir := newTestBuilder(t)

	cfg := &config.ProjectConfig{
		Toolchains: []config.Toolchain{{
			Name: "persist",
			From: "plugin",
			Config: config.ToolchainConfig{
				Version: "1.0",
				URL:     srv.URL + "/persist.tar.gz",
				SHA256:  hash,
			},
		}},
	}

	extractDir := filepath.Join(dir, "cache", "extract", "persist-1.0")
	fakeVerifyBinary(t, extractDir, "persist")

	err := b.Build(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	// Create a fresh registry backed by the same CAS to confirm persistence.
	freshRegistry := coordinator.NewToolchainRegistry(b.Store)
	manifest, err := freshRegistry.Lookup(context.Background(), "persist", "1.0")
	if err != nil {
		t.Fatalf("fresh Lookup: %v", err)
	}
	if manifest == nil {
		t.Fatal("manifest not persisted to CAS")
	}
	if manifest.Name != "persist" || manifest.Version != "1.0" {
		t.Errorf("unexpected manifest: %+v", manifest)
	}
	if len(manifest.Artifacts) == 0 {
		t.Error("expected persisted artifacts")
	}
}

func TestStripPrefixFromPath(t *testing.T) {
	tests := []struct {
		path, prefix, want string
	}{
		{"mygo-1.0/bin/go", "mygo-1.0", "bin/go"},
		{"mygo-1.0/", "mygo-1.0", ""},
		{"mygo-1.0", "mygo-1.0", ""},
		{"other/file", "mygo-1.0", "other/file"},
		{"bin/go", "", "bin/go"},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s_%s", tt.path, tt.prefix), func(t *testing.T) {
			got := stripPrefixFromPath(tt.path, tt.prefix)
			if got != tt.want {
				t.Errorf("stripPrefixFromPath(%q, %q) = %q, want %q", tt.path, tt.prefix, got, tt.want)
			}
		})
	}
}

func TestExtractTarGz(t *testing.T) {
	archive := makeTarGz(t, map[string]string{
		"prefix/file1.txt": "hello",
		"prefix/sub/f2.go": "package main",
	})

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "test.tar.gz")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}

	extractDir := filepath.Join(dir, "out")
	if err := extractTarGz(archivePath, extractDir, "prefix"); err != nil {
		t.Fatal(err)
	}

	// Check extracted files.
	content, err := os.ReadFile(filepath.Join(extractDir, "file1.txt"))
	if err != nil {
		t.Fatalf("read file1.txt: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("file1.txt = %q, want %q", content, "hello")
	}

	content, err = os.ReadFile(filepath.Join(extractDir, "sub", "f2.go"))
	if err != nil {
		t.Fatalf("read sub/f2.go: %v", err)
	}
	if string(content) != "package main" {
		t.Errorf("sub/f2.go = %q, want %q", content, "package main")
	}
}

func TestExtractZip(t *testing.T) {
	archive := makeZip(t, map[string]string{
		"file1.txt":  "zip content",
		"sub/f2.txt": "nested",
	})

	dir := t.TempDir()
	archivePath := filepath.Join(dir, "test.zip")
	if err := os.WriteFile(archivePath, archive, 0o644); err != nil {
		t.Fatal(err)
	}

	extractDir := filepath.Join(dir, "out")
	if err := extractZip(archivePath, extractDir, ""); err != nil {
		t.Fatal(err)
	}

	content, err := os.ReadFile(filepath.Join(extractDir, "file1.txt"))
	if err != nil {
		t.Fatalf("read file1.txt: %v", err)
	}
	if string(content) != "zip content" {
		t.Errorf("file1.txt = %q, want %q", content, "zip content")
	}
}
