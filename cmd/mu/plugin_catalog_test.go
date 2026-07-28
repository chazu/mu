package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/plugincatalog"
)

func TestSplitPluginRef(t *testing.T) {
	name, version, err := splitPluginRef("aws@0.1.0")
	if err != nil || name != "aws" || version != "0.1.0" {
		t.Fatalf("splitPluginRef = %q, %q, %v", name, version, err)
	}
	if _, _, err := splitPluginRef("../aws"); err == nil {
		t.Fatal("expected path separator rejection")
	}
}

func TestInstallCatalogPluginUpdatesProjectAndLock(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "mu.cue"), []byte("package mu\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	archive := testPluginArchive(t)
	hash := sha256.Sum256(archive)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(archive)
	}))
	defer server.Close()

	catalog := &plugincatalog.Catalog{
		SchemaVersion: 1,
		Repository:    "example/plugins",
		ReleaseTag:    "catalog-v1",
		Plugins: []plugincatalog.Plugin{{
			Name:       "demo",
			Version:    "1.0.0",
			AssetURL:   server.URL + "/demo.tar.gz",
			SHA256:     hex.EncodeToString(hash[:]),
			Path:       "plugins/demo",
			Entrypoint: "run.sh",
		}},
	}
	store, err := oci.NewLocal(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	cli := &cliContext{ProjectRoot: project, Store: store}
	entry, err := installCatalogPlugin(context.Background(), cli, server.URL+"/catalog.json", catalog, catalog.Plugins[0])
	if err != nil {
		t.Fatalf("installCatalogPlugin: %v", err)
	}
	if entry.BundleDigest == "" {
		t.Fatal("missing bundle digest")
	}
	muCue, err := os.ReadFile(filepath.Join(project, "mu.cue"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(muCue), "sha256:") || !strings.Contains(string(muCue), "demo") {
		t.Fatalf("mu.cue was not updated:\n%s", muCue)
	}
	lock, err := plugincatalog.LoadLock(filepath.Join(project, "mu.lock"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := lock.Find("demo"); !ok || got.BundleDigest != entry.BundleDigest {
		t.Fatalf("lock entry = %+v, want %s", got, entry.BundleDigest)
	}
	if _, err := os.Stat(filepath.Join(home, ".mu", "plugins", "demo")); err != nil {
		t.Fatalf("plugin was not extracted into cache: %v", err)
	}
	metadata, err := os.ReadFile(filepath.Join(home, ".mu", "plugins", "demo", "bundle-"+strings.TrimPrefix(entry.BundleDigest, "sha256:")[:12], "mu-plugin.json"))
	if err != nil {
		t.Fatalf("installed plugin metadata missing: %v", err)
	}
	if !strings.Contains(string(metadata), `"name": "demo"`) {
		t.Fatalf("installed plugin metadata = %s", metadata)
	}
}

func testPluginArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	files := map[string][]byte{
		"plugins/demo/mu.cue": []byte("plugin: {entrypoint: \"run.sh\"}\n"),
		"plugins/demo/run.sh": []byte("#!/bin/sh\necho ok\n"),
	}
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(body))}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(tw, bytes.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
