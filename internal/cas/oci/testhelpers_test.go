package oci

import (
	"context"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

// memStore wraps oras-go's memory.Store to satisfy the Registry interface.
// memory.Store lacks Delete and Tags methods that real registries (and our
// interface) provide. The wrapper supplies stubs adequate for unit tests.
//
// IMPORTANT: Tags here returns the union of all tagged refs in the underlying
// store via Resolve, NOT a true tag index. For tests that exercise tag
// enumeration logic (Task 6+), prefer testRegistry from oci_test.go.
type memStore struct {
	*memory.Store
	tags []string // tags written via Tag, kept in insertion order, deduped
}

func newMemStore() *memStore {
	return &memStore{Store: memory.New()}
}

func (m *memStore) Delete(_ context.Context, _ ocispec.Descriptor) error {
	return nil // unused in current tests
}

func (m *memStore) Tag(ctx context.Context, desc ocispec.Descriptor, ref string) error {
	if err := m.Store.Tag(ctx, desc, ref); err != nil {
		return err
	}
	for _, t := range m.tags {
		if t == ref {
			return nil
		}
	}
	m.tags = append(m.tags, ref)
	return nil
}

func (m *memStore) Tags(_ context.Context, last string, fn func([]string) error) error {
	var out []string
	for _, t := range m.tags {
		if last == "" || t > last {
			out = append(out, t)
		}
	}
	if len(out) == 0 {
		return nil
	}
	return fn(out)
}
