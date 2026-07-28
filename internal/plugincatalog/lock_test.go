package plugincatalog

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLockUpsertSortsAndRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mu.lock")
	lock := NewLock()
	lock.Catalog = LockedCatalog{URL: "https://example.test/catalog.json", Repository: "example/plugins", ReleaseTag: "v1"}
	lock.Upsert(LockedPlugin{Name: "zeta", Version: "1.0.0", SourceRevision: "v1", AssetSHA256: "a", BundleDigest: "sha256:z"})
	lock.Upsert(LockedPlugin{Name: "aws", Version: "1.0.0", SourceRevision: "v1", AssetSHA256: "b", BundleDigest: "sha256:a"})
	if err := WriteLock(path, lock); err != nil {
		t.Fatalf("WriteLock: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) == "" || string(data)[0] != '{' {
		t.Fatalf("lockfile is not JSON: %q", data)
	}
	loaded, err := LoadLock(path)
	if err != nil {
		t.Fatalf("LoadLock: %v", err)
	}
	if len(loaded.Plugins) != 2 || loaded.Plugins[0].Name != "aws" || loaded.Plugins[1].Name != "zeta" {
		t.Fatalf("plugins = %+v", loaded.Plugins)
	}
	loaded.Upsert(LockedPlugin{Name: "aws", Version: "1.1.0", SourceRevision: "v2", AssetSHA256: "c", BundleDigest: "sha256:c"})
	if got, ok := loaded.Find("aws"); !ok || got.Version != "1.1.0" {
		t.Fatalf("updated aws = %+v, found=%v", got, ok)
	}
}
