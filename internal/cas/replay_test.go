package cas_test

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
)

type replayCheckingStore struct {
	cas.Store
	dir   string
	calls int
}

func (s *replayCheckingStore) Put(ctx context.Context, r io.Reader) (cas.Digest, error) {
	entries, err := filepath.Glob(filepath.Join(s.dir, "mu-cas-replay-*"))
	if err != nil || len(entries) != 1 {
		return cas.Digest{}, errors.New("payload was not spooled to a private replay file")
	}
	info, err := os.Stat(entries[0])
	if err != nil || info.Mode().Perm() != 0o600 {
		return cas.Digest{}, errors.New("replay file is not private")
	}
	s.calls++
	return cas.ComputeDigest(r)
}

func TestTieredFanoutReplaysFileAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	local := &replayCheckingStore{dir: dir}
	remote := &replayCheckingStore{dir: dir}
	tier := &cas.Tiered{Layers: []cas.Store{local, remote}, WriteThrough: true}
	if _, err := tier.Put(context.Background(), strings.NewReader(strings.Repeat("payload", 10000))); err != nil {
		t.Fatal(err)
	}
	if local.calls != 1 || remote.calls != 1 {
		t.Fatalf("fanout calls: %d, %d", local.calls, remote.calls)
	}
	assertNoReplayFiles(t, dir)
}

func TestTieredRepairFileLivesUntilCallerClose(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	remote := newFake()
	dgst, err := remote.Put(context.Background(), strings.NewReader("blob"))
	if err != nil {
		t.Fatal(err)
	}
	tier := &cas.Tiered{Layers: []cas.Store{newFake(), remote}, ReadRepair: true}
	rc, err := tier.Get(context.Background(), dgst)
	if err != nil {
		t.Fatal(err)
	}
	entries, _ := filepath.Glob(filepath.Join(dir, "mu-cas-replay-*"))
	if len(entries) != 1 {
		t.Fatal("caller does not own an on-disk replay")
	}
	got, err := io.ReadAll(rc)
	if err != nil || string(got) != "blob" {
		t.Fatalf("read replay: %q, %v", got, err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal(err)
	}
	if err := rc.Close(); err != nil {
		t.Fatal("Close is not idempotent", err)
	}
	assertNoReplayFiles(t, dir)
}

type cancellingReader struct {
	cancel context.CancelFunc
	done   bool
}

func (r *cancellingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	r.done = true
	r.cancel()
	return copy(p, "partial"), nil
}

func TestTieredCancelledSpoolCleansUp(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	local := newFake()
	tier := &cas.Tiered{Layers: []cas.Store{local, newFake()}, WriteThrough: true}
	_, err := tier.Put(ctx, &cancellingReader{cancel: cancel})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled put: %v", err)
	}
	if local.count() != 0 {
		t.Fatal("published partial payload")
	}
	assertNoReplayFiles(t, dir)
}

func TestTieredCorruptBlobCannotRepair(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TMPDIR", dir)
	remote := newFake()
	digest, err := remote.Put(context.Background(), strings.NewReader("expected"))
	if err != nil {
		t.Fatal(err)
	}
	remote.blobs[digest.String()] = []byte("corrupt")
	local := newFake()
	tier := &cas.Tiered{Layers: []cas.Store{local, remote}, ReadRepair: true}
	rc, err := tier.Get(context.Background(), digest)
	if rc != nil {
		rc.Close()
	}
	if err == nil {
		t.Fatal("accepted corrupt blob")
	}
	if local.count() != 0 {
		t.Fatal("repaired a corrupt blob")
	}
	assertNoReplayFiles(t, dir)
}

func assertNoReplayFiles(t *testing.T, dir string) {
	t.Helper()
	entries, err := filepath.Glob(filepath.Join(dir, "mu-cas-replay-*"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("left replay files: %v, %v", entries, err)
	}
}
