package plugincatalog

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
)

func TestLoadSearchAndSelect(t *testing.T) {
	data := `{
  "schema_version": 1,
  "repository": "example/plugins",
  "release_tag": "catalog-v1",
  "plugins": [
    {"name":"zeta","version":"1.0.0","description":"Zed","asset_url":"https://example.test/zeta.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","path":"plugins/zeta","entrypoint":"plugin.bb"},
    {"name":"demo","version":"1.0.0","description":"Demo observer","asset_url":"https://example.test/demo-1.tar.gz","sha256":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","path":"plugins/demo","entrypoint":"plugin.bb"},
    {"name":"demo","version":"1.1.0","description":"Demo observer newer","asset_url":"https://example.test/demo-2.tar.gz","sha256":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","path":"plugins/demo","entrypoint":"plugin.bb"}
  ]
}`
	catalog, err := Load(strings.NewReader(data))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	items := catalog.Search("observer")
	if len(items) != 2 || items[0].Version != "1.1.0" {
		t.Fatalf("Search(observer) = %+v", items)
	}
	selected, err := catalog.Select("demo", "")
	if err != nil {
		t.Fatalf("Select: %v", err)
	}
	if selected.Version != "1.1.0" {
		t.Errorf("selected version = %q, want 1.1.0", selected.Version)
	}
	selected, err = catalog.Select("demo", "v1.0.0")
	if err != nil {
		t.Fatalf("Select exact: %v", err)
	}
	if selected.Version != "1.0.0" {
		t.Errorf("exact selected version = %q, want 1.0.0", selected.Version)
	}
}

func TestLoadRejectsUnsafePackagePath(t *testing.T) {
	data := `{"schema_version":1,"repository":"example/plugins","release_tag":"v1","plugins":[{"name":"bad","version":"1.0.0","asset_url":"https://example.test/bad.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","path":"../bad","entrypoint":"plugin.bb"}]}`
	if _, err := Load(strings.NewReader(data)); err == nil {
		t.Fatal("expected unsafe path error")
	}
}

func TestFetchFileCatalog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	data := `{"schema_version":1,"repository":"example/plugins","release_tag":"v1","plugins":[{"name":"demo","version":"1.0.0","asset_url":"https://example.test/demo.tar.gz","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","path":"plugins/demo","entrypoint":"plugin.bb"}]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	catalog, err := Fetch(context.Background(), "file://"+path)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if len(catalog.Plugins) != 1 || catalog.Plugins[0].Name != "demo" {
		t.Fatalf("catalog = %+v", catalog)
	}
}

func TestDownloadAssetVerifiesHash(t *testing.T) {
	body := []byte("package bytes")
	hash := sha256.Sum256(body)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(body)
	}))
	defer server.Close()
	p := Plugin{Name: "demo", AssetURL: server.URL + "/demo.tar.gz", SHA256: hex.EncodeToString(hash[:])}
	dest := filepath.Join(t.TempDir(), "asset.tar.gz")
	if err := DownloadAsset(context.Background(), p, dest); err != nil {
		t.Fatalf("DownloadAsset: %v", err)
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("asset bytes = %q, want %q", got, body)
	}
}

func TestExtractArchiveRejectsTraversal(t *testing.T) {
	archivePath := filepath.Join(t.TempDir(), "bad.tar.gz")
	file, err := os.Create(archivePath)
	if err != nil {
		t.Fatal(err)
	}
	gz := gzip.NewWriter(file)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "../escape", Mode: 0o644, Size: 1}); err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(tw, "x")
	_ = tw.Close()
	_ = gz.Close()
	_ = file.Close()
	if err := ExtractArchive(archivePath, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("expected traversal rejection")
	}
}
