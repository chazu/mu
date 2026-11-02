package cas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func keyFor(s string) ActionKey {
	sum := sha256.Sum256([]byte(s))
	return ActionKey{Digest: NewSHA256(hex.EncodeToString(sum[:]))}
}

func mustDiskStore(t *testing.T, dir string) *DiskStore {
	t.Helper()
	s, err := NewDiskStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestDiskStoreRejectsEmptyPath(t *testing.T) {
	if _, err := NewDiskStore(""); err == nil {
		t.Fatal("expected error for empty path")
	}
	if _, err := BuildStore(BuildSpec{Backends: []ResolvedBackend{{BackendSpec: BackendSpec{Type: "disk"}, Read: true, Write: true}}}); err == nil {
		t.Fatal("disk backend factory accepted an empty path")
	}
}

func TestDiskStoreBlobRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := mustDiskStore(t, t.TempDir())

	d, err := s.Put(ctx, strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("hello"))
	if d != NewSHA256(hex.EncodeToString(sum[:])) {
		t.Fatalf("digest = %s", d)
	}
	// Re-putting identical bytes deduplicates.
	if d2, err := s.Put(ctx, strings.NewReader("hello")); err != nil || d2 != d {
		t.Fatalf("second put: %s, %v", d2, err)
	}
	if ok, err := s.Has(ctx, d); err != nil || !ok {
		t.Fatalf("Has = %v, %v", ok, err)
	}
	rc, err := s.Get(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "hello" {
		t.Fatalf("Get = %q", data)
	}
	if err := s.Delete(ctx, d); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Has(ctx, d); ok {
		t.Fatal("blob survived Delete")
	}
	if _, err := s.Get(ctx, d); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get after delete = %v, want ErrNotExist", err)
	}
	if _, err := s.Get(ctx, Digest{Algorithm: "sha256", Hash: "../../etc"}); !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("path-like digest = %v, want ErrInvalidDigest", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(s.Root(), "ingest")); len(entries) != 0 {
		t.Fatalf("ingest not cleaned: %v", entries)
	}
}

func TestDiskStoreActionResultMissAndRoundtrip(t *testing.T) {
	ctx := context.Background()
	s := mustDiskStore(t, t.TempDir())
	key := keyFor("k")
	if got, err := s.GetActionResult(ctx, key); got != nil || err != nil {
		t.Fatalf("miss = %+v, %v", got, err)
	}
	out, _ := s.Put(ctx, strings.NewReader("out"))
	want := &ActionResult{Version: ActionResultVersion, Outputs: map[string]Digest{"o": out}, OutputModes: map[string]uint32{"o": 0o755}, ExitCode: 3}
	if err := s.PutActionResult(ctx, key, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetActionResult(ctx, key)
	if err != nil || got == nil {
		t.Fatalf("hit = %+v, %v", got, err)
	}
	if got.Outputs["o"] != out || got.OutputModes["o"] != 0o755 || got.ExitCode != 3 || got.Version != ActionResultVersion {
		t.Fatalf("roundtrip lost data: %+v", got)
	}
	bad := &ActionResult{Outputs: map[string]Digest{"o": {Algorithm: "sha256", Hash: "short"}}}
	if err := s.PutActionResult(ctx, keyFor("bad"), bad); !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("invalid output accepted: %v", err)
	}
}

// Two store instances on one directory (as two mu processes would have) write
// different action results concurrently; a fresh instance sees every one.
func TestDiskStoreConcurrentInstancesKeepAllResults(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	stores := []*DiskStore{mustDiskStore(t, dir), mustDiskStore(t, dir)}

	const writersPerStore, perWriter = 4, 50
	var wg sync.WaitGroup
	errs := make(chan error, len(stores)*writersPerStore)
	for si, s := range stores {
		for w := 0; w < writersPerStore; w++ {
			wg.Add(1)
			go func(si, w int, s *DiskStore) {
				defer wg.Done()
				for i := 0; i < perWriter; i++ {
					// Shared content across writers exercises concurrent
					// identical blob puts as well.
					out, err := s.Put(ctx, strings.NewReader(fmt.Sprintf("blob-%d", i)))
					if err != nil {
						errs <- err
						return
					}
					name := fmt.Sprintf("%d-%d-%d", si, w, i)
					res := &ActionResult{Version: ActionResultVersion, Outputs: map[string]Digest{name: out}}
					if err := s.PutActionResult(ctx, keyFor(name), res); err != nil {
						errs <- err
						return
					}
				}
			}(si, w, s)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	reopened := mustDiskStore(t, dir)
	for si := range stores {
		for w := 0; w < writersPerStore; w++ {
			for i := 0; i < perWriter; i++ {
				name := fmt.Sprintf("%d-%d-%d", si, w, i)
				got, err := reopened.GetActionResult(ctx, keyFor(name))
				if err != nil || got == nil {
					t.Fatalf("lost result %s: %+v, %v", name, got, err)
				}
				if _, ok := got.Outputs[name]; !ok {
					t.Fatalf("result %s has wrong outputs: %+v", name, got.Outputs)
				}
			}
		}
	}
	count := 0
	if err := reopened.WalkActionResults(func(_ ActionKey, r *ActionResult, err error) error {
		if err != nil {
			return err
		}
		count++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if want := len(stores) * writersPerStore * perWriter; count != want {
		t.Fatalf("walk saw %d results, want %d", count, want)
	}
}

// A partially written temporary file (a crashed or in-flight writer) is never
// visible as a result, and readers racing a rewrite see old or new, never torn.
func TestDiskStorePartialWriteNeverVisible(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s := mustDiskStore(t, dir)
	key := keyFor("partial")

	// Simulate a writer that died mid-write: a truncated receipt in ingest.
	full, _ := json.Marshal(&ActionResult{Outputs: map[string]Digest{"o": keyFor("x").Digest}})
	if err := os.WriteFile(filepath.Join(dir, "ingest", "ac-crashed"), full[:len(full)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GetActionResult(ctx, key); got != nil || err != nil {
		t.Fatalf("partial temp visible: %+v, %v", got, err)
	}
	if err := s.WalkActionResults(func(k ActionKey, _ *ActionResult, _ error) error {
		return fmt.Errorf("unexpected entry %s", k.Digest)
	}); err != nil {
		t.Fatal(err)
	}

	// Large receipts make a torn read likely if writes were not atomic.
	const outputs = 2000
	mk := func(gen int) *ActionResult {
		r := &ActionResult{Version: ActionResultVersion, ExitCode: gen, Outputs: make(map[string]Digest, outputs)}
		for i := 0; i < outputs; i++ {
			r.Outputs[fmt.Sprintf("out-%04d", i)] = keyFor(fmt.Sprint(gen, i)).Digest
		}
		return r
	}
	other := mustDiskStore(t, dir)
	var wg sync.WaitGroup
	stop := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(stop)
		for gen := 0; gen < 40; gen++ {
			if err := other.PutActionResult(ctx, key, mk(gen)); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	for done := false; !done; {
		select {
		case <-stop:
			done = true
		default:
		}
		got, err := s.GetActionResult(ctx, key)
		if err != nil {
			t.Fatalf("torn read: %v", err)
		}
		if got != nil && len(got.Outputs) != outputs {
			t.Fatalf("torn read: %d outputs", len(got.Outputs))
		}
	}
	wg.Wait()
}

func TestDiskStoreWalkReportsCorruptEntries(t *testing.T) {
	dir := t.TempDir()
	s := mustDiskStore(t, dir)
	key := keyFor("corrupt")
	p, _ := s.ActionResultPath(key)
	if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Non-key names in ac/ are ignored.
	if err := os.WriteFile(filepath.Join(dir, "ac", "README"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	var seen []error
	if err := s.WalkActionResults(func(k ActionKey, r *ActionResult, err error) error {
		if k != key || r != nil {
			t.Fatalf("unexpected entry %s %+v", k.Digest, r)
		}
		seen = append(seen, err)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] == nil {
		t.Fatalf("corrupt entry not reported: %v", seen)
	}
	if _, err := s.GetActionResult(context.Background(), key); err == nil {
		t.Fatal("corrupt receipt read without error")
	}
}

// Caches written by earlier releases recorded action results in an OCI layout
// index.json. Opening the store migrates them to per-key files once.
func TestDiskStoreMigratesLegacyIndex(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	writeBlob := func(data []byte) string {
		sum := sha256.Sum256(data)
		h := hex.EncodeToString(sum[:])
		p := filepath.Join(dir, "blobs", "sha256", h)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return "sha256:" + h
	}
	out := writeBlob([]byte("output"))
	outD, _ := ParseFullDigest(out)
	config, _ := json.Marshal(&ActionResult{Version: ActionResultVersion, Outputs: map[string]Digest{"bin": outD}, ExitCode: 0})
	configDigest := writeBlob(config)
	manifest := fmt.Sprintf(`{"schemaVersion":2,"config":{"mediaType":"application/vnd.mu.action-result.v1+json","digest":%q,"size":%d},"layers":[]}`, configDigest, len(config))
	manifestDigest := writeBlob([]byte(manifest))

	key := keyFor("legacy")
	index := fmt.Sprintf(`{"schemaVersion":2,"manifests":[
		{"mediaType":"application/vnd.oci.image.manifest.v1+json","digest":%q,"annotations":{"org.opencontainers.image.ref.name":"action-sha256-%s"}},
		{"digest":"sha256:%s","annotations":{"org.opencontainers.image.ref.name":"action-sha256-%s"}},
		{"digest":%q,"annotations":{"org.opencontainers.image.ref.name":"not-an-action"}}
	]}`, manifestDigest, key.Digest.Hash, strings.Repeat("0", 64), keyFor("dangling").Digest.Hash, manifestDigest)
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(index), 0o644); err != nil {
		t.Fatal(err)
	}

	s := mustDiskStore(t, dir)
	got, err := s.GetActionResult(ctx, key)
	if err != nil || got == nil || got.Outputs["bin"] != outD {
		t.Fatalf("legacy result not migrated: %+v, %v", got, err)
	}
	if got, _ := s.GetActionResult(ctx, keyFor("dangling")); got != nil {
		t.Fatalf("dangling legacy entry produced a result: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("legacy index not retired: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.json.migrated")); err != nil {
		t.Fatalf("legacy index not kept for reference: %v", err)
	}
	// Reopening is a no-op and still sees the migrated result.
	if got, _ := mustDiskStore(t, dir).GetActionResult(ctx, key); got == nil {
		t.Fatal("migrated result lost on reopen")
	}
}

func TestDiskStoreTruncatedLegacyIndexIsAMiss(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.json"), []byte(`{"manifests":[{"dig`), 0o644); err != nil {
		t.Fatal(err)
	}
	s := mustDiskStore(t, dir)
	if got, err := s.GetActionResult(context.Background(), keyFor("x")); got != nil || err != nil {
		t.Fatalf("truncated index: %+v, %v", got, err)
	}
}
