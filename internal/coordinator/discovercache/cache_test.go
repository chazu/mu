package discovercache_test

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/coordinator/discovercache"
	"github.com/chau/mu/internal/plugin"
)

func sampleResp() *plugin.DiscoverResponse {
	return &plugin.DiscoverResponse{
		Name:            "go",
		Version:         "0.1.0",
		ProtocolVersion: plugin.ProtocolVersion,
		Consumes:        []string{"source"},
		Produces:        []string{"binary"},
		Capabilities:    []string{"discover", "plan"},
	}
}

func TestRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := discovercache.Open(path)
	d := cas.NewSHA256("deadbeef")
	if err := c.Put(d, sampleResp()); err != nil {
		t.Fatal(err)
	}
	c2 := discovercache.Open(path)
	got, ok := c2.Get(d)
	if !ok {
		t.Fatal("expected cache hit after reopen")
	}
	if got.Name != "go" || got.Version != "0.1.0" {
		t.Fatalf("unexpected response: %+v", got)
	}
}

func TestCorruptFileTreatedAsEmpty(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	if err := os.WriteFile(path, []byte("not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := discovercache.Open(path)
	if _, ok := c.Get(cas.NewSHA256("abc")); ok {
		t.Fatal("expected miss on corrupt file")
	}
	// A Put after corrupt Open should still succeed and overwrite the file.
	if err := c.Put(cas.NewSHA256("abc"), sampleResp()); err != nil {
		t.Fatal(err)
	}
	c2 := discovercache.Open(path)
	if _, ok := c2.Get(cas.NewSHA256("abc")); !ok {
		t.Fatal("expected hit after Put overwrote corrupt file")
	}
}

func TestProtocolVersionMismatchTreatedAsMiss(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := discovercache.Open(path)
	d := cas.NewSHA256("aa")
	resp := sampleResp()
	resp.ProtocolVersion = plugin.ProtocolVersion + 99
	if err := c.Put(d, resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(d); ok {
		t.Fatal("expected miss when cached protocol version differs from runtime")
	}
}

func TestZeroDigestNotCached(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := discovercache.Open(path)
	var zero cas.Digest
	if err := c.Put(zero, sampleResp()); err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get(zero); ok {
		t.Fatal("zero digest must never cache-hit")
	}
}

func TestNilCacheSafe(t *testing.T) {
	var c *discovercache.Cache
	if _, ok := c.Get(cas.NewSHA256("x")); ok {
		t.Fatal("nil cache must miss")
	}
	if err := c.Put(cas.NewSHA256("x"), sampleResp()); err != nil {
		t.Fatalf("nil cache Put must be a no-op: %v", err)
	}
}

func TestConcurrentPutsDoNotCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.json")
	c := discovercache.Open(path)

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_ = c.Put(cas.NewSHA256(fmt.Sprintf("key-%d", i)), sampleResp())
		}(i)
	}
	wg.Wait()

	// Reopen and Put once more — the file must still be parseable.
	c2 := discovercache.Open(path)
	if err := c2.Put(cas.NewSHA256("check"), sampleResp()); err != nil {
		t.Fatalf("post-concurrent Put failed, file may be corrupt: %v", err)
	}
}
