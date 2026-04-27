package oci_test

import (
	"bytes"
	"context"
	"io"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/oci"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
)

// testRegistry is a minimal in-memory OCI registry keyed by digest.
// Unlike oras-go's memory.Store, it resolves Fetch/Exists by digest alone,
// matching how real registries behave.
type testRegistry struct {
	mu      sync.Mutex
	blobs   map[godigest.Digest][]byte
	descs   map[godigest.Digest]ocispec.Descriptor
	tags    map[string]ocispec.Descriptor
	deleted []string
}

func newTestRegistry() *testRegistry {
	return &testRegistry{
		blobs: make(map[godigest.Digest][]byte),
		descs: make(map[godigest.Digest]ocispec.Descriptor),
		tags:  make(map[string]ocispec.Descriptor),
	}
}

func (r *testRegistry) Push(_ context.Context, expected ocispec.Descriptor, content io.Reader) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.blobs[expected.Digest]; exists {
		return errdef.ErrAlreadyExists
	}

	data, err := io.ReadAll(content)
	if err != nil {
		return err
	}
	r.blobs[expected.Digest] = data
	r.descs[expected.Digest] = expected
	return nil
}

func (r *testRegistry) Fetch(_ context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, ok := r.blobs[target.Digest]
	if !ok {
		return nil, errdef.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (r *testRegistry) Exists(_ context.Context, target ocispec.Descriptor) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	_, ok := r.blobs[target.Digest]
	return ok, nil
}

func (r *testRegistry) Delete(_ context.Context, target ocispec.Descriptor) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.blobs[target.Digest]; !ok {
		return errdef.ErrNotFound
	}
	delete(r.blobs, target.Digest)
	delete(r.descs, target.Digest)
	r.deleted = append(r.deleted, target.Digest.String())
	return nil
}

func (r *testRegistry) Tag(_ context.Context, desc ocispec.Descriptor, reference string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if _, ok := r.blobs[desc.Digest]; !ok {
		return errdef.ErrNotFound
	}
	r.tags[reference] = desc
	return nil
}

func (r *testRegistry) Resolve(_ context.Context, reference string) (ocispec.Descriptor, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	desc, ok := r.tags[reference]
	if !ok {
		return ocispec.Descriptor{}, errdef.ErrNotFound
	}
	return desc, nil
}

func (r *testRegistry) Tags(_ context.Context, last string, fn func([]string) error) error {
	r.mu.Lock()
	names := make([]string, 0, len(r.tags))
	for name := range r.tags {
		if last == "" || name > last {
			names = append(names, name)
		}
	}
	r.mu.Unlock()
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)
	return fn(names)
}

func newTestStore(t *testing.T) (*oci.OCIStore, *testRegistry) {
	t.Helper()
	reg := newTestRegistry()
	return oci.New(reg), reg
}

func TestPutGetRoundtrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	content := "round-trip content"

	dgst, err := s.Put(ctx, strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if dgst.Algorithm != "sha256" {
		t.Errorf("Algorithm = %q, want %q", dgst.Algorithm, "sha256")
	}
	if dgst.Hash == "" {
		t.Fatal("Hash is empty")
	}

	rc, err := s.Get(ctx, dgst)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != content {
		t.Errorf("Get returned %q, want %q", got, content)
	}
}

func TestHas(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	dgst, err := s.Put(ctx, strings.NewReader("exists"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	ok, err := s.Has(ctx, dgst)
	if err != nil {
		t.Fatalf("Has (existing): %v", err)
	}
	if !ok {
		t.Error("Has returned false for existing blob")
	}

	missing := cas.NewSHA256("0000000000000000000000000000000000000000000000000000000000000000")
	ok, err = s.Has(ctx, missing)
	if err != nil {
		t.Fatalf("Has (missing): %v", err)
	}
	if ok {
		t.Error("Has returned true for missing blob")
	}
}

func TestDelete(t *testing.T) {
	s, reg := newTestStore(t)
	ctx := context.Background()

	dgst, err := s.Put(ctx, strings.NewReader("to-delete"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.Delete(ctx, dgst); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if len(reg.deleted) != 1 {
		t.Errorf("expected 1 deletion, got %d", len(reg.deleted))
	}

	ok, err := s.Has(ctx, dgst)
	if err != nil {
		t.Fatalf("Has after Delete: %v", err)
	}
	if ok {
		t.Error("Has returned true after Delete")
	}
}

func TestPutIdempotent(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()
	content := "idempotent-content"

	d1, err := s.Put(ctx, strings.NewReader(content))
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}

	d2, err := s.Put(ctx, strings.NewReader(content))
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}

	if d1 != d2 {
		t.Errorf("digests differ: %s vs %s", d1, d2)
	}
}

func TestActionResultRoundtrip(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	key := cas.ActionKey{Digest: cas.NewSHA256("aabbccdd00112233445566778899aabbccdd00112233445566778899aabbccdd")}
	want := &cas.ActionResult{
		ExitCode: 0,
		Outputs: map[string]cas.Digest{
			"out": cas.NewSHA256("1122334455667788990011223344556677889900112233445566778899001122"),
		},
	}

	if err := s.PutActionResult(ctx, key, want); err != nil {
		t.Fatalf("PutActionResult: %v", err)
	}

	got, err := s.GetActionResult(ctx, key)
	if err != nil {
		t.Fatalf("GetActionResult: %v", err)
	}
	if got == nil {
		t.Fatal("GetActionResult returned nil")
	}
	if got.ExitCode != want.ExitCode {
		t.Errorf("ExitCode = %d, want %d", got.ExitCode, want.ExitCode)
	}
	if len(got.Outputs) != len(want.Outputs) {
		t.Fatalf("Outputs length = %d, want %d", len(got.Outputs), len(want.Outputs))
	}
	for k, v := range want.Outputs {
		if got.Outputs[k] != v {
			t.Errorf("Outputs[%q] = %s, want %s", k, got.Outputs[k], v)
		}
	}
}

func TestGetActionResultMiss(t *testing.T) {
	s, _ := newTestStore(t)
	ctx := context.Background()

	key := cas.ActionKey{Digest: cas.NewSHA256("ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00")}
	got, err := s.GetActionResult(ctx, key)
	if err != nil {
		t.Fatalf("GetActionResult: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result for missing key, got %+v", got)
	}
}

func TestStoreImplementsInterface(t *testing.T) {
	reg := newTestRegistry()
	var _ cas.Store = oci.New(reg)
}

// stubRegistryWithTags is a compile-time check that the Registry interface
// includes Tags. If the assertion below fails to compile, Tags is missing
// from the interface.
type stubRegistryWithTags struct{}

func (s *stubRegistryWithTags) Push(context.Context, ocispec.Descriptor, io.Reader) error {
	return nil
}
func (s *stubRegistryWithTags) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	return nil, nil
}
func (s *stubRegistryWithTags) Exists(context.Context, ocispec.Descriptor) (bool, error) {
	return false, nil
}
func (s *stubRegistryWithTags) Delete(context.Context, ocispec.Descriptor) error      { return nil }
func (s *stubRegistryWithTags) Tag(context.Context, ocispec.Descriptor, string) error { return nil }
func (s *stubRegistryWithTags) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return ocispec.Descriptor{}, nil
}
func (s *stubRegistryWithTags) Tags(context.Context, string, func([]string) error) error {
	return nil
}

// callTagsViaInterface ensures Tags is part of the Registry interface.
// This function will not compile if Registry does not declare Tags.
func callTagsViaInterface(r oci.Registry) error {
	return r.Tags(context.Background(), "", func([]string) error { return nil })
}

func TestRegistryInterfaceHasTags(t *testing.T) {
	var _ oci.Registry = (*stubRegistryWithTags)(nil)
	// Ensure the interface-based call compiles.
	_ = callTagsViaInterface
}
