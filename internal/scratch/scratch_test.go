package scratch

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/registryhttp"
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
		hdr := &zip.FileHeader{Name: name, Method: zip.Deflate}
		hdr.SetMode(0o755)
		w, err := zw.CreateHeader(hdr)
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

	b, _ := newTestBuilder(t)

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

	b, _ := newTestBuilder(t)

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

	b, _ := newTestBuilder(t)

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

	b, _ := newTestBuilder(t)

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
		"data.txt":    "some data",
	})
	hash := sha256Hex(archive)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(archive)
	}))
	defer srv.Close()

	b, _ := newTestBuilder(t)

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
	if err := extract(context.Background(), archivePath, extractDir, "prefix", "tool"); err != nil {
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
	if err := extract(context.Background(), archivePath, extractDir, "", "tool"); err != nil {
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

func TestVerifiedLocalArchiveRebuildsMissingManifestWithoutDownload(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"bin/tool": "#!/bin/sh\nexit 0\n"})
	hash := sha256.Sum256(archive)
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "downloads"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "downloads", "tool-1.0.tar.gz"), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := oci.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	builder := &Builder{Store: store, Registry: coordinator.NewToolchainRegistry(store), CacheDir: dir}
	cfg := &config.ProjectConfig{Toolchains: []config.Toolchain{{Name: "tool", From: "scratch", Config: config.ToolchainConfig{Version: "1.0", URL: "https://unavailable.invalid/tool.tar.gz", SHA256: hex.EncodeToString(hash[:])}}}}
	if err := builder.Build(registryhttp.WithOffline(context.Background()), cfg); err != nil {
		t.Fatalf("verified archive required remote source: %v", err)
	}
	if builder.Registry.Get("tool") == nil {
		t.Fatal("did not rebuild manifest")
	}
}

// A tree left by an earlier (or crashed) extraction must never contribute
// files to a new manifest, and extraction leaves no staging behind.
func TestBuildIgnoresStaleExtractTreesAndCleansStaging(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"bin/fresh": "#!/bin/sh\necho fresh 1.0\n"})
	hash := sha256Hex(archive)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive) }))
	defer srv.Close()

	b, _ := newTestBuilder(t)
	legacy := filepath.Join(b.CacheDir, "extract", "fresh-1.0", "bin")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "stale"), []byte("from an older archive"), 0o755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.ProjectConfig{Toolchains: []config.Toolchain{{
		Name: "fresh", From: "scratch",
		Config: config.ToolchainConfig{Version: "1.0", URL: srv.URL + "/fresh-1.0.tar.gz", SHA256: hash},
	}}}
	if err := b.Build(context.Background(), cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}
	manifest, err := b.Registry.Lookup(context.Background(), "fresh", "1.0")
	if err != nil || manifest == nil {
		t.Fatalf("Lookup = %v, %v", manifest, err)
	}
	if _, ok := manifest.Artifacts["bin/stale"]; ok {
		t.Fatalf("stale file from a previous extraction entered the manifest: %v", manifest.Artifacts)
	}
	if _, ok := manifest.Artifacts["bin/fresh"]; !ok || len(manifest.Artifacts) != 1 {
		t.Fatalf("artifacts = %v, want only bin/fresh", manifest.Artifacts)
	}
	entries, _ := os.ReadDir(filepath.Join(b.CacheDir, "extract"))
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".extract-") {
			t.Fatalf("staging directory left behind: %s", e.Name())
		}
	}
}

// A non-archive download (like jq) is installed as bin/<toolchain> and
// verified, independent of the staging directory's name.
func TestBuild_RawBinaryInstalledUnderToolchainName(t *testing.T) {
	script := []byte("#!/bin/sh\necho jq-1.7.1\n")
	hash := sha256Hex(script)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(script) }))
	defer srv.Close()

	b, _ := newTestBuilder(t)
	cfg := &config.ProjectConfig{Toolchains: []config.Toolchain{{
		Name: "jq", From: "scratch",
		Config: config.ToolchainConfig{Version: "1.7.1", URL: srv.URL + "/jq-macos-arm64", SHA256: hash},
	}}}
	if err := b.Build(context.Background(), cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}
	manifest, err := b.Registry.Lookup(context.Background(), "jq", "1.7.1")
	if err != nil || manifest == nil {
		t.Fatalf("Lookup = %v, %v", manifest, err)
	}
	if _, ok := manifest.Artifacts["bin/jq"]; !ok || len(manifest.Artifacts) != 1 {
		t.Fatalf("artifacts = %v, want only bin/jq", manifest.Artifacts)
	}
}

// Toolchain archives may carry in-tree symlinks, including the toolchain
// binary itself (bin/x -> ../libexec/x). They are recorded as links, never
// read into CAS, and file modes are recorded alongside the digests.
func TestBuild_InTreeSymlinksAndModesRecorded(t *testing.T) {
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	body := "#!/bin/sh\necho linked 1.0\n"
	tw.WriteHeader(&tar.Header{Name: "libexec/linked", Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg})
	tw.Write([]byte(body))
	tw.WriteHeader(&tar.Header{Name: "share/README", Mode: 0o644, Size: 2, Typeflag: tar.TypeReg})
	tw.Write([]byte("hi"))
	tw.WriteHeader(&tar.Header{Name: "bin/linked", Linkname: "../libexec/linked", Typeflag: tar.TypeSymlink})
	tw.Close()
	gw.Close()
	archive := buf.Bytes()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write(archive) }))
	defer srv.Close()

	b, _ := newTestBuilder(t)
	var warnings bytes.Buffer
	b.Warnings = &warnings
	cfg := &config.ProjectConfig{Toolchains: []config.Toolchain{{
		Name: "linked", From: "scratch",
		Config: config.ToolchainConfig{Version: "1.0", URL: srv.URL + "/linked-1.0.tar.gz", SHA256: sha256Hex(archive)},
	}}}
	if err := b.Build(context.Background(), cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}
	manifest, err := b.Registry.Lookup(context.Background(), "linked", "1.0")
	if err != nil || manifest == nil {
		t.Fatalf("Lookup = %v, %v", manifest, err)
	}
	if _, ok := manifest.Artifacts["bin/linked"]; ok || len(manifest.Artifacts) != 2 {
		t.Fatalf("artifacts = %v, want libexec/linked and share/README", manifest.Artifacts)
	}
	if manifest.Symlinks["bin/linked"] != "../libexec/linked" {
		t.Fatalf("symlinks = %v", manifest.Symlinks)
	}
	if manifest.Modes["libexec/linked"] != 0o755 || manifest.Modes["share/README"] != 0o644 {
		t.Fatalf("modes = %v", manifest.Modes)
	}
	if warnings.Len() != 0 {
		t.Fatalf("unexpected warning: %q", warnings.String())
	}
}

// A symlink to a file outside the tree must never have its target's content
// stored in CAS (from where write-through or cache push could upload it).
func TestStoreArtifactsDoesNotReadThroughSymlinks(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(secret, []byte("PRIVATE KEY"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin", "tool"), []byte("tool"), 0o755)
	if err := os.Symlink(secret, filepath.Join(dir, "bin", "leak")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("bin", filepath.Join(dir, "bindir")); err != nil {
		t.Fatal(err)
	}
	b, _ := newTestBuilder(t)
	tree, err := b.storeArtifacts(context.Background(), dir)
	if err != nil {
		t.Fatalf("storeArtifacts: %v", err)
	}
	if len(tree.artifacts) != 1 || tree.artifacts["bin/tool"] == "" {
		t.Fatalf("artifacts = %v, want only bin/tool", tree.artifacts)
	}
	if strings.Join(tree.skipped, ",") != "bin/leak" {
		t.Fatalf("skipped = %v", tree.skipped)
	}
	if len(tree.links) != 1 || tree.links["bindir"] != "bin" {
		t.Fatalf("links = %v, want the in-tree bindir -> bin", tree.links)
	}
	if has, _ := b.Store.Has(context.Background(), cas.NewSHA256(sha256Hex([]byte("PRIVATE KEY")))); has {
		t.Fatal("symlink target content was stored in CAS")
	}
}

// diskRemote serves a DiskStore as a cas.Remote, withholding the blobs in
// hide: a shared cache that holds a toolchain receipt and manifest but not
// the toolchain's files.
type diskRemote struct {
	store *cas.DiskStore
	hide  map[cas.Digest]bool
}

func (r *diskRemote) Get(ctx context.Context, d cas.Digest) (io.ReadCloser, error) {
	if r.hide[d] {
		return nil, cas.ErrNotFound
	}
	rc, err := r.store.Get(ctx, d)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, cas.ErrNotFound
	}
	return rc, err
}

func (r *diskRemote) GetActionResult(ctx context.Context, key cas.ActionKey) (*cas.ActionResult, error) {
	return r.store.GetActionResult(ctx, key)
}

// A fresh local cache whose remote supplies the toolchain receipt but not the
// artifacts it names must rebuild the toolchain, not report it complete
// (mu-zl9.8). Afterwards the binary is in the local store.
func TestBuild_RemoteReceiptWithoutArtifactsRebuilds(t *testing.T) {
	archive := makeTarGz(t, map[string]string{"bin/tool": "#!/bin/sh\necho tool 1.0\n"})
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		w.Write(archive)
	}))
	defer srv.Close()
	cfg := &config.ProjectConfig{Toolchains: []config.Toolchain{{
		Name: "tool", From: "scratch",
		Config: config.ToolchainConfig{Version: "1.0", URL: srv.URL + "/tool.tar.gz", SHA256: sha256Hex(archive)},
	}}}
	ctx := context.Background()

	// Populate the "remote" by building there, then withhold the binary.
	remoteDisk, err := cas.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	seed := &Builder{Store: remoteDisk, Registry: coordinator.NewToolchainRegistry(remoteDisk), CacheDir: t.TempDir()}
	if err := seed.Build(ctx, cfg); err != nil {
		t.Fatalf("seed build: %v", err)
	}
	bin, err := cas.ParseFullDigest(seed.Registry.Get("tool").Artifacts["bin/tool"])
	if err != nil {
		t.Fatal(err)
	}
	remote := &diskRemote{store: remoteDisk, hide: map[cas.Digest]bool{bin: true}}

	newBuilder := func() *Builder {
		local, err := cas.NewDiskStore(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		store := &cas.Cache{Local: local, Remote: remote}
		return &Builder{Store: store, Registry: coordinator.NewToolchainRegistry(store), CacheDir: t.TempDir()}
	}

	b := newBuilder()
	fetches.Store(0)
	if err := b.Build(ctx, cfg); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if n := fetches.Load(); n != 1 {
		t.Fatalf("archive fetches = %d, want 1 (the remote receipt's binary is unavailable)", n)
	}
	local := b.Store.(*cas.Cache).Local
	if ok, _ := local.Has(ctx, bin); !ok {
		t.Fatal("toolchain reported complete but its binary is not in the local store")
	}

	// When the remote does hold the artifacts, they are fetched and verified
	// into the local store instead of rebuilding.
	remote.hide = nil
	b = newBuilder()
	fetches.Store(0)
	if err := b.Build(ctx, cfg); err != nil {
		t.Fatalf("Build with complete remote: %v", err)
	}
	if n := fetches.Load(); n != 0 {
		t.Fatalf("archive fetches = %d, want 0 (the remote has every artifact)", n)
	}
	if ok, _ := b.Store.(*cas.Cache).Local.Has(ctx, bin); !ok {
		t.Fatal("remote artifact was not fetched into the local store")
	}
}
