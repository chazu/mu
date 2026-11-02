package cas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync/atomic"
	"testing"
)

// fakeRemote is an in-memory Remote. blobs maps a digest to the bytes served
// for it (which need not hash to that digest); err, when set, fails every call.
type fakeRemote struct {
	blobs   map[Digest]string
	results map[ActionKey]*ActionResult
	err     error
	calls   atomic.Int32
}

func (f *fakeRemote) Get(ctx context.Context, d Digest) (io.ReadCloser, error) {
	f.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	data, ok := f.blobs[d]
	if !ok {
		return nil, fmt.Errorf("fake: %s: %w", d, ErrNotFound)
	}
	return io.NopCloser(strings.NewReader(data)), nil
}

func (f *fakeRemote) GetActionResult(ctx context.Context, key ActionKey) (*ActionResult, error) {
	f.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	return f.results[key], nil
}

func digestOf(s string) Digest {
	sum := sha256.Sum256([]byte(s))
	return NewSHA256(hex.EncodeToString(sum[:]))
}

func newTestCache(t *testing.T, remote *fakeRemote) (*Cache, *[]error) {
	t.Helper()
	var warnings []error
	return &Cache{
		Local:  mustDiskStore(t, t.TempDir()),
		Remote: remote,
		Warn:   func(err error) { warnings = append(warnings, err) },
	}, &warnings
}

func readAll(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestCacheRemoteBlobHitIsRepairedIntoLocal(t *testing.T) {
	ctx := context.Background()
	d := digestOf("remote bytes")
	remote := &fakeRemote{blobs: map[Digest]string{d: "remote bytes"}}
	c, warnings := newTestCache(t, remote)

	rc, err := c.Get(ctx, d)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got := readAll(t, rc); got != "remote bytes" {
		t.Fatalf("Get = %q", got)
	}
	if ok, _ := c.Local.Has(ctx, d); !ok {
		t.Fatal("remote hit was not written into the local store")
	}
	remote.calls.Store(0)
	rc, err = c.Get(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	readAll(t, rc)
	if remote.calls.Load() != 0 {
		t.Fatal("second read went to the remote")
	}
	if len(*warnings) != 0 {
		t.Fatalf("unexpected warnings: %v", *warnings)
	}
}

func TestCacheRemoteActionResultRepairsOutputsAndReceipt(t *testing.T) {
	ctx := context.Background()
	a, b := digestOf("out a"), digestOf("out b")
	key := ActionKey{Digest: digestOf("key")}
	want := &ActionResult{Outputs: map[string]Digest{"a": a, "b": b}}
	remote := &fakeRemote{
		blobs:   map[Digest]string{a: "out a", b: "out b"},
		results: map[ActionKey]*ActionResult{key: want},
	}
	c, _ := newTestCache(t, remote)

	got, err := c.GetActionResult(ctx, key)
	if err != nil || got == nil || got.Outputs["a"] != a || got.Outputs["b"] != b {
		t.Fatalf("GetActionResult = %+v, %v", got, err)
	}
	for _, d := range []Digest{a, b} {
		if ok, _ := c.Local.Has(ctx, d); !ok {
			t.Fatalf("output %s not repaired into local", d)
		}
	}
	if local, _ := c.Local.GetActionResult(ctx, key); local == nil {
		t.Fatal("receipt not recorded locally")
	}
}

func TestCacheCorruptRemoteBlobIsRejectedAndNotStored(t *testing.T) {
	ctx := context.Background()
	d := digestOf("genuine")
	remote := &fakeRemote{blobs: map[Digest]string{d: "tampered"}}
	c, _ := newTestCache(t, remote)

	if _, err := c.Get(ctx, d); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("Get corrupt = %v, want ErrDigestMismatch", err)
	}
	for _, check := range []Digest{d, digestOf("tampered")} {
		if ok, _ := c.Local.Has(ctx, check); ok {
			t.Fatalf("corrupt remote content stored locally as %s", check)
		}
	}

	// A remote receipt whose output is corrupt is a miss, not a restore.
	key := ActionKey{Digest: digestOf("key")}
	remote.results = map[ActionKey]*ActionResult{key: {Outputs: map[string]Digest{"out": d}}}
	got, err := c.GetActionResult(ctx, key)
	if err != nil || got != nil {
		t.Fatalf("GetActionResult with corrupt output = %+v, %v; want miss", got, err)
	}
	if local, _ := c.Local.GetActionResult(ctx, key); local != nil {
		t.Fatal("receipt with corrupt output recorded locally")
	}
}

func TestCacheRemoteDownWarnsOnceAndContinuesLocally(t *testing.T) {
	ctx := context.Background()
	remote := &fakeRemote{err: errors.New("connection refused")}
	c, warnings := newTestCache(t, remote)

	for i := 0; i < 3; i++ {
		got, err := c.GetActionResult(ctx, ActionKey{Digest: digestOf(fmt.Sprint("key", i))})
		if err != nil || got != nil {
			t.Fatalf("GetActionResult with remote down = %+v, %v; want miss", got, err)
		}
	}
	missing := digestOf("nowhere")
	if _, err := c.Get(ctx, missing); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Get with remote down = %v, want local not-exist", err)
	}
	if remote.calls.Load() != 1 {
		t.Fatalf("remote contacted %d times after failing, want 1", remote.calls.Load())
	}
	if len(*warnings) != 1 {
		t.Fatalf("got %d warnings, want 1: %v", len(*warnings), *warnings)
	}

	// Local work is unaffected.
	d, err := c.Put(ctx, strings.NewReader("local"))
	if err != nil {
		t.Fatal(err)
	}
	rc, err := c.Get(ctx, d)
	if err != nil {
		t.Fatal(err)
	}
	if readAll(t, rc) != "local" {
		t.Fatal("local blob not served")
	}
}

func TestCacheCallerCancellationDoesNotDisableRemote(t *testing.T) {
	d := digestOf("later")
	remote := &fakeRemote{blobs: map[Digest]string{d: "later"}}
	c, warnings := newTestCache(t, remote)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Get(ctx, d); !errors.Is(err, context.Canceled) {
		t.Fatalf("Get with cancelled ctx = %v", err)
	}
	if _, err := c.GetActionResult(ctx, ActionKey{Digest: digestOf("k")}); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetActionResult with cancelled ctx = %v", err)
	}
	rc, err := c.Get(context.Background(), d)
	if err != nil {
		t.Fatalf("remote disabled by caller cancellation: %v", err)
	}
	readAll(t, rc)
	if len(*warnings) != 0 {
		t.Fatalf("cancellation reported as outage: %v", *warnings)
	}
}

func TestCacheWritesStayLocal(t *testing.T) {
	ctx := context.Background()
	remote := &fakeRemote{}
	c, _ := newTestCache(t, remote)
	d, err := c.Put(ctx, strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	key := ActionKey{Digest: digestOf("k")}
	if err := c.PutActionResult(ctx, key, &ActionResult{Outputs: map[string]Digest{"x": d}}); err != nil {
		t.Fatal(err)
	}
	if got, err := c.GetActionResult(ctx, key); err != nil || got == nil {
		t.Fatalf("local receipt = %+v, %v", got, err)
	}
	if remote.calls.Load() != 0 {
		t.Fatal("local hits or writes contacted the remote")
	}
}
