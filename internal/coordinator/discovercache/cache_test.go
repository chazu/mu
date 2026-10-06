package discovercache_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/coordinator/discovercache"
	"github.com/chazu/mu/internal/plugin"
)

func TestIndependentWritersPublishCompleteSnapshots(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := range 32 {
		// Independent cache instances model independent mu processes: their
		// mutexes do not serialize writes to this shared path.
		c := discovercache.Open(path)
		wg.Go(func() {
			<-start
			if err := c.Put(cas.NewSHA256(fmt.Sprintf("writer-%d", i)), sampleResp()); err != nil {
				t.Error(err)
			}
		})
	}
	close(start)
	wg.Wait()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Entries map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatalf("corrupt snapshot: %v", err)
	}
	if len(snapshot.Entries) != 1 {
		t.Fatalf("expected one complete winning snapshot, got %d entries", len(snapshot.Entries))
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "cache.json" {
		t.Fatalf("temporary files leaked: %v", entries)
	}
}

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
