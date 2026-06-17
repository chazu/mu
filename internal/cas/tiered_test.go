package cas_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"testing"

	"github.com/chazu/mu/internal/cas"
)

// fakeStore is an in-memory cas.Store with injectable failure hooks.
type fakeStore struct {
	mu         sync.Mutex
	blobs      map[string][]byte           // "alg:hash" -> bytes
	actions    map[string]cas.ActionResult // "alg:hash" (from ActionKey) -> result
	FailGet    error
	FailPut    error
	FailHas    error
	FailAction error // applies to both Get/PutActionResult
}

func newFake() *fakeStore {
	return &fakeStore{
		blobs:   map[string][]byte{},
		actions: map[string]cas.ActionResult{},
	}
}

func (s *fakeStore) Has(_ context.Context, d cas.Digest) (bool, error) {
	if s.FailHas != nil {
		return false, s.FailHas
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.blobs[d.String()]
	return ok, nil
}

func (s *fakeStore) Get(_ context.Context, d cas.Digest) (io.ReadCloser, error) {
	if s.FailGet != nil {
		return nil, s.FailGet
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.blobs[d.String()]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (s *fakeStore) Put(_ context.Context, r io.Reader) (cas.Digest, error) {
	if s.FailPut != nil {
		return cas.Digest{}, s.FailPut
	}
	buf, err := io.ReadAll(r)
	if err != nil {
		return cas.Digest{}, err
	}
	d, err := cas.ComputeDigest(bytes.NewReader(buf))
	if err != nil {
		return cas.Digest{}, err
	}
	s.mu.Lock()
	s.blobs[d.String()] = buf
	s.mu.Unlock()
	return d, nil
}

func (s *fakeStore) Delete(_ context.Context, d cas.Digest) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.blobs, d.String())
	return nil
}

func (s *fakeStore) GetActionResult(_ context.Context, k cas.ActionKey) (*cas.ActionResult, error) {
	if s.FailAction != nil {
		return nil, s.FailAction
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.actions[k.Digest.String()]
	if !ok {
		return nil, nil
	}
	copy := r
	return &copy, nil
}

func (s *fakeStore) PutActionResult(_ context.Context, k cas.ActionKey, r *cas.ActionResult) error {
	if s.FailAction != nil {
		return s.FailAction
	}
	s.mu.Lock()
	s.actions[k.Digest.String()] = *r
	s.mu.Unlock()
	return nil
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.blobs)
}

// --- Tests ---

func TestTiered_GetMissThenHitWithReadRepair(t *testing.T) {
	l0, l1 := newFake(), newFake()
	// Seed L1 only.
	d, _ := l1.Put(context.Background(), bytes.NewReader([]byte("hello")))

	tier := &cas.Tiered{
		Layers:     []cas.Store{l0, l1},
		ReadRepair: true,
	}

	rc, err := tier.Get(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "hello" {
		t.Fatalf("got %q want hello", got)
	}

	// L0 should have been back-filled.
	if l0.count() != 1 {
		t.Fatalf("expected read-repair to back-fill L0; got %d blobs", l0.count())
	}
}

func TestTiered_NoRepairWhenDisabled(t *testing.T) {
	l0, l1 := newFake(), newFake()
	d, _ := l1.Put(context.Background(), bytes.NewReader([]byte("hello")))

	tier := &cas.Tiered{
		Layers:     []cas.Store{l0, l1},
		ReadRepair: false,
	}
	rc, err := tier.Get(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	rc.Close()
	if l0.count() != 0 {
		t.Fatal("expected no back-fill when ReadRepair=false")
	}
}

func TestTiered_PutWriteThroughFansOut(t *testing.T) {
	l0, l1 := newFake(), newFake()
	tier := &cas.Tiered{
		Layers:       []cas.Store{l0, l1},
		WriteThrough: true,
	}
	_, err := tier.Put(context.Background(), bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	if l0.count() != 1 || l1.count() != 1 {
		t.Fatalf("expected both layers populated; got l0=%d l1=%d", l0.count(), l1.count())
	}
}

func TestTiered_PutWithoutWriteThroughOnlyL0(t *testing.T) {
	l0, l1 := newFake(), newFake()
	tier := &cas.Tiered{Layers: []cas.Store{l0, l1}}
	_, err := tier.Put(context.Background(), bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	if l0.count() != 1 || l1.count() != 0 {
		t.Fatalf("expected only L0 populated; got l0=%d l1=%d", l0.count(), l1.count())
	}
}

func TestTiered_GetTolerantToMidLayerError(t *testing.T) {
	l0, l1, l2 := newFake(), newFake(), newFake()
	d, _ := l2.Put(context.Background(), bytes.NewReader([]byte("deep")))
	l1.FailGet = errors.New("L1 offline")

	var events []cas.TieredEvent
	tier := &cas.Tiered{
		Layers:   []cas.Store{l0, l1, l2},
		Observer: func(e cas.TieredEvent) { events = append(events, e) },
	}
	rc, err := tier.Get(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if string(got) != "deep" {
		t.Fatalf("got %q want deep", got)
	}

	// There should be an error event for L1.
	sawErr := false
	for _, e := range events {
		if e.Op == "get" && e.Layer == 1 && e.Outcome == "error" {
			sawErr = true
		}
	}
	if !sawErr {
		t.Fatal("expected error event for failing L1")
	}
}

func TestTiered_PutTolerantToUpperLayerError(t *testing.T) {
	l0, l1 := newFake(), newFake()
	l1.FailPut = errors.New("remote push broken")

	var errEvents int
	tier := &cas.Tiered{
		Layers:       []cas.Store{l0, l1},
		WriteThrough: true,
		Observer: func(e cas.TieredEvent) {
			if e.Op == "put" && e.Outcome == "error" {
				errEvents++
			}
		},
	}
	_, err := tier.Put(context.Background(), bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatalf("Put must not fail when only upper layer fails: %v", err)
	}
	if l0.count() != 1 {
		t.Fatal("L0 must still hold the blob")
	}
	if errEvents == 0 {
		t.Fatal("expected an error event for L1 failure")
	}
}

func TestTiered_PutFailsIfL0Fails(t *testing.T) {
	l0, l1 := newFake(), newFake()
	l0.FailPut = errors.New("disk full")
	tier := &cas.Tiered{
		Layers:       []cas.Store{l0, l1},
		WriteThrough: true,
	}
	if _, err := tier.Put(context.Background(), bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("expected Put to fail when L0 fails")
	}
}

func TestTiered_ActionResultRepair(t *testing.T) {
	l0, l1 := newFake(), newFake()
	// Seed an action result and its output blob on L1.
	outDigest, _ := l1.Put(context.Background(), bytes.NewReader([]byte("out")))
	key := cas.ActionKey{Digest: cas.NewSHA256("ak")}
	result := &cas.ActionResult{Outputs: map[string]cas.Digest{"bin": outDigest}}
	_ = l1.PutActionResult(context.Background(), key, result)

	tier := &cas.Tiered{
		Layers:     []cas.Store{l0, l1},
		ReadRepair: true,
	}
	got, err := tier.GetActionResult(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected action-result hit")
	}
	if l0.count() != 1 {
		t.Fatal("expected output blob to be eagerly repaired into L0")
	}
	if res, _ := l0.GetActionResult(context.Background(), key); res == nil {
		t.Fatal("expected action result to be repaired into L0")
	}
}

func TestTiered_ReadOnlyLayerSkippedOnPut(t *testing.T) {
	l0, l1 := newFake(), newFake()
	tier := &cas.Tiered{
		Layers:       []cas.Store{l0, l1},
		Policies:     []cas.LayerPolicy{cas.DefaultLayerPolicy(), {Read: true, Write: false}},
		WriteThrough: true,
	}
	_, err := tier.Put(context.Background(), bytes.NewReader([]byte("x")))
	if err != nil {
		t.Fatal(err)
	}
	if l1.count() != 0 {
		t.Fatal("write-disabled layer must not receive writes")
	}
}

func TestTiered_EmptyLayersErrors(t *testing.T) {
	tier := &cas.Tiered{}
	if _, err := tier.Put(context.Background(), bytes.NewReader([]byte("x"))); err == nil {
		t.Fatal("expected empty Layers to fail Put")
	}
}
