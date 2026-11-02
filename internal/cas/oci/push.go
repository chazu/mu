package oci

import (
	"bytes"
	"context"
	"errors"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
)

// pushIfAbsent skips the push if desc already exists in repo. Real registries
// return errdef.ErrAlreadyExists on duplicate pushes; pre-checking avoids the
// extra round-trip.
func pushIfAbsent(ctx context.Context, repo Registry, desc ocispec.Descriptor, content []byte) error {
	ok, err := repo.Exists(ctx, desc)
	if err != nil {
		return err
	}
	if ok {
		return nil
	}
	// Race window: another writer may have pushed between our Exists check
	// and Push. Real registries return errdef.ErrAlreadyExists; treat that
	// as success so publishing remains idempotent under concurrent callers.
	if err := repo.Push(ctx, desc, bytes.NewReader(content)); err != nil && !errors.Is(err, errdef.ErrAlreadyExists) {
		return err
	}
	return nil
}
