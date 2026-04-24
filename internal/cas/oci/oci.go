// Package oci implements a content-addressable store backed by OCI.
//
// The same OCIStore type works with both local OCI layout directories (via
// NewLocal) and remote OCI registries (via New with a remote.Repository).
// Blobs map directly to OCI blobs. Action results are stored as OCI manifests
// tagged by the action key hash, with each output as a layer descriptor and the
// result metadata as the config blob. Standard OCI tools (crane, skopeo, oras)
// can inspect the cache in either form.
package oci

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"net/http"

	"github.com/chau/mu/internal/cas"
	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	ocilayout "oras.land/oras-go/v2/content/oci"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
)

const (
	MediaTypeMuBlob   = "application/vnd.mu.blob.v1"
	MediaTypeMuAction = "application/vnd.mu.action-result.v1+json"
)

// Registry abstracts the OCI operations needed by OCIStore.
// oras-go's oci.Store (local layout), remote.Repository, and memory.Store all
// satisfy this interface.
type Registry interface {
	Push(ctx context.Context, expected ocispec.Descriptor, content io.Reader) error
	Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error)
	Exists(ctx context.Context, target ocispec.Descriptor) (bool, error)
	Delete(ctx context.Context, target ocispec.Descriptor) error
	Tag(ctx context.Context, desc ocispec.Descriptor, reference string) error
	Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error)
	// Tags enumerates tag names in lexical order, calling fn for each page.
	// last is the tag to start after (empty = from the beginning). Backends
	// without /v2/<name>/tags/list support may return an error; callers
	// should treat that as "no plugins discoverable here," not as a fatal
	// failure of the whole list operation.
	Tags(ctx context.Context, last string, fn func(tags []string) error) error
}

// OCIStore is a CAS backend that stores blobs and action results in an OCI registry.
type OCIStore struct {
	repo Registry
}

// New creates an OCIStore backed by the given registry.
func New(repo Registry) *OCIStore {
	return &OCIStore{repo: repo}
}

// NewLocal creates an OCIStore backed by a local OCI layout directory.
// The directory is created if it does not exist.
func NewLocal(path string) (*OCIStore, error) {
	store, err := ocilayout.New(path)
	if err != nil {
		return nil, fmt.Errorf("oci: open local store %s: %w", path, err)
	}
	return &OCIStore{repo: store}, nil
}

// toOCIDigest converts a cas.Digest to an OCI digest.
func toOCIDigest(d cas.Digest) godigest.Digest {
	return godigest.NewDigestFromEncoded(godigest.Algorithm(d.Algorithm), d.Hash)
}

// fromOCIDigest converts an OCI digest to a cas.Digest.
func fromOCIDigest(d godigest.Digest) cas.Digest {
	return cas.Digest{
		Algorithm: string(d.Algorithm()),
		Hash:      d.Encoded(),
	}
}

// Has reports whether the blob identified by dgst exists in the registry.
func (s *OCIStore) Has(ctx context.Context, dgst cas.Digest) (bool, error) {
	desc := ocispec.Descriptor{
		MediaType: MediaTypeMuBlob,
		Digest:    toOCIDigest(dgst),
	}
	ok, err := s.repo.Exists(ctx, desc)
	if err != nil {
		return false, fmt.Errorf("oci: check blob %s: %w", dgst, err)
	}
	return ok, nil
}

// Get fetches the blob identified by dgst from the registry. When the
// underlying repo is a remote registry, oras-go's blobStore.Fetch validates
// the response's Content-Length against desc.Size; passing Size=0 would
// falsely trip that check. We therefore resolve the blob's size first
// (HEAD for remote; layout index for local) and populate the descriptor
// before fetching.
func (s *OCIStore) Get(ctx context.Context, dgst cas.Digest) (io.ReadCloser, error) {
	ocidgst := toOCIDigest(dgst)

	// Remote repositories: bypass oras-go's blobStore.Fetch, which refuses
	// to return a body unless desc.Size matches the server's
	// Content-Length. We usually don't have the size (ActionResult.Outputs
	// stores digests only) and some registries return 404 on HEAD even
	// when GET works — so we can't reliably populate the size up front.
	// Go straight to a GET through the authed client.
	if rr, ok := s.repo.(*remote.Repository); ok {
		return remoteBlobGet(ctx, rr, ocidgst)
	}

	desc := ocispec.Descriptor{
		MediaType: MediaTypeMuBlob,
		Digest:    ocidgst,
	}
	if d, err := s.repo.Resolve(ctx, ocidgst.String()); err == nil {
		desc.Size = d.Size
	}
	rc, err := s.repo.Fetch(ctx, desc)
	if err != nil {
		return nil, fmt.Errorf("oci: fetch blob %s: %w", dgst, err)
	}
	return rc, nil
}

// remoteBlobGet streams a blob body directly from a remote repository via
// its authenticated Client. It skips oras-go's Content-Length validation
// and (intentionally) does not verify the content digest here: callers are
// responsible for digest verification (the CAS boundary does this via
// Digest.Match on Put, and the tiered cache re-Puts any fetched bytes).
func remoteBlobGet(ctx context.Context, rr *remote.Repository, dgst godigest.Digest) (io.ReadCloser, error) {
	ref := rr.Reference
	ref.Reference = dgst.String()
	ctx = auth.AppendRepositoryScope(ctx, ref, auth.ActionPull)

	scheme := "https"
	if rr.PlainHTTP {
		scheme = "http"
	}
	url := fmt.Sprintf("%s://%s/v2/%s/blobs/%s", scheme, ref.Host(), ref.Repository, dgst.String())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("oci: build blob request: %w", err)
	}

	client := rr.Client
	if client == nil {
		client = auth.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("oci: fetch blob %s: %w", dgst, err)
	}
	switch resp.StatusCode {
	case http.StatusOK:
		return resp.Body, nil
	case http.StatusNotFound:
		resp.Body.Close()
		return nil, fmt.Errorf("oci: fetch blob %s: %w", dgst, errdef.ErrNotFound)
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("oci: fetch blob %s: %s", dgst, resp.Status)
	}
}

// Put streams data from r, computing a SHA-256 digest, then pushes the blob
// to the registry. OCI requires a content-length, so the data is buffered to
// a temporary file first.
func (s *OCIStore) Put(ctx context.Context, r io.Reader) (cas.Digest, error) {
	// Buffer to temp file to get size (OCI requires content-length).
	tmp, err := os.CreateTemp("", "mu-oci-blob-*")
	if err != nil {
		return cas.Digest{}, fmt.Errorf("oci: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), r)
	if err != nil {
		tmp.Close()
		return cas.Digest{}, fmt.Errorf("oci: buffer blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return cas.Digest{}, fmt.Errorf("oci: close temp file: %w", err)
	}

	dgst := godigest.NewDigestFromEncoded(godigest.SHA256, hex.EncodeToString(h.Sum(nil)))

	// Re-open for push.
	f, err := os.Open(tmpName)
	if err != nil {
		return cas.Digest{}, fmt.Errorf("oci: reopen temp file: %w", err)
	}
	defer f.Close()

	desc := ocispec.Descriptor{
		MediaType: MediaTypeMuBlob,
		Digest:    dgst,
		Size:      size,
	}

	if err := s.repo.Push(ctx, desc, f); err != nil {
		// Already-exists is fine for content-addressed dedup.
		if !isAlreadyExists(err) {
			return cas.Digest{}, fmt.Errorf("oci: push blob: %w", err)
		}
	}

	return fromOCIDigest(dgst), nil
}

// Delete removes the blob identified by dgst from the registry.
func (s *OCIStore) Delete(ctx context.Context, dgst cas.Digest) error {
	desc := ocispec.Descriptor{
		MediaType: MediaTypeMuBlob,
		Digest:    toOCIDigest(dgst),
	}
	if err := s.repo.Delete(ctx, desc); err != nil {
		return fmt.Errorf("oci: delete blob %s: %w", dgst, err)
	}
	return nil
}

// PutActionResult stores an action result as an OCI manifest tagged by the
// action key hash. Each output artifact becomes a layer descriptor, and the
// result metadata is stored as the config blob.
func (s *OCIStore) PutActionResult(ctx context.Context, key cas.ActionKey, result *cas.ActionResult) error {
	// Serialise the action result as the config blob.
	configBytes, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("oci: marshal action result: %w", err)
	}

	configDigest := godigest.FromBytes(configBytes)
	configDesc := ocispec.Descriptor{
		MediaType: MediaTypeMuAction,
		Digest:    configDigest,
		Size:      int64(len(configBytes)),
	}

	// Push config blob.
	if err := s.repo.Push(ctx, configDesc, bytes.NewReader(configBytes)); err != nil {
		if !isAlreadyExists(err) {
			return fmt.Errorf("oci: push action config: %w", err)
		}
	}

	// Build layer descriptors from output artifacts. Size is looked up via
	// Resolve so the manifest carries the full descriptor; without it,
	// oras-go sets req.ContentLength = 0 on a push, Go's http client falls
	// back to chunked transfer encoding, and strict registries (e.g. Zot)
	// reject the monolithic-upload PUT with 400.
	layers := make([]ocispec.Descriptor, 0, len(result.Outputs))
	for name, dgst := range result.Outputs {
		layer := ocispec.Descriptor{
			MediaType: MediaTypeMuBlob,
			Digest:    toOCIDigest(dgst),
			Annotations: map[string]string{
				"mu.output.name": name,
			},
		}
		if resolved, err := s.repo.Resolve(ctx, layer.Digest.String()); err == nil {
			layer.Size = resolved.Size
		}
		layers = append(layers, layer)
	}

	manifest := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    configDesc,
		Layers:    layers,
	}

	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("oci: marshal manifest: %w", err)
	}

	manifestDigest := godigest.FromBytes(manifestBytes)
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    manifestDigest,
		Size:      int64(len(manifestBytes)),
	}

	// Push the manifest blob.
	if err := s.repo.Push(ctx, manifestDesc, bytes.NewReader(manifestBytes)); err != nil {
		if !isAlreadyExists(err) {
			return fmt.Errorf("oci: push action manifest: %w", err)
		}
	}

	// Tag the manifest with the action key for lookup.
	tag := actionKeyToTag(key)
	if err := s.repo.Tag(ctx, manifestDesc, tag); err != nil {
		return fmt.Errorf("oci: tag action manifest: %w", err)
	}

	return nil
}

// GetActionResult retrieves an action result by resolving the tag derived from
// the action key. Returns (nil, nil) on a cache miss.
func (s *OCIStore) GetActionResult(ctx context.Context, key cas.ActionKey) (*cas.ActionResult, error) {
	tag := actionKeyToTag(key)

	// Resolve the tag to get the manifest descriptor.
	manifestDesc, err := s.repo.Resolve(ctx, tag)
	if err != nil {
		if isNotFound(err) {
			return nil, nil // cache miss
		}
		return nil, fmt.Errorf("oci: resolve action tag %s: %w", tag, err)
	}

	// Fetch the manifest.
	manifestRC, err := s.repo.Fetch(ctx, manifestDesc)
	if err != nil {
		return nil, fmt.Errorf("oci: fetch action manifest: %w", err)
	}
	defer manifestRC.Close()

	var manifest ocispec.Manifest
	if err := json.NewDecoder(manifestRC).Decode(&manifest); err != nil {
		return nil, fmt.Errorf("oci: decode action manifest: %w", err)
	}

	// Fetch the config to get the ActionResult.
	configRC, err := s.repo.Fetch(ctx, manifest.Config)
	if err != nil {
		return nil, fmt.Errorf("oci: fetch action config: %w", err)
	}
	defer configRC.Close()

	var result cas.ActionResult
	if err := json.NewDecoder(configRC).Decode(&result); err != nil {
		return nil, fmt.Errorf("oci: decode action result: %w", err)
	}

	return &result, nil
}

// actionKeyToTag returns a deterministic tag for an action key.
func actionKeyToTag(key cas.ActionKey) string {
	return "action-" + key.Digest.Algorithm + "-" + key.Digest.Hash
}

func isAlreadyExists(err error) bool {
	return errors.Is(err, errdef.ErrAlreadyExists)
}

func isNotFound(err error) bool {
	return errors.Is(err, errdef.ErrNotFound)
}
