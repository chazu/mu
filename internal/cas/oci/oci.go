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
	"sort"
	"strconv"
	"sync"
	"time"

	"net/http"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/registryhttp"
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
	// ArtifactTypeMuAction is the manifest-level artifactType for a cache action
	// result (the config media type without the +json). A registry UI keys off it
	// to recognise and collapse a build cache; older caches lacking it are still
	// recognised by MediaTypeMuAction on the config.
	ArtifactTypeMuAction = "application/vnd.mu.action-result.v1"

	// mu artifact media types (`mu build --publish`). Unlike a cache entry, a
	// published artifact carries human metadata (target/command/created/revision)
	// in its config blob and is tagged by git sha + latest.
	MediaTypeMuArtifactConfig = "application/vnd.mu.artifact.v1+json" // config blob
	ArtifactTypeMuArtifact    = "application/vnd.mu.artifact.v1"      // manifest artifactType
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
	// without /v2/<name>/tags/list support may return 404/405. Transport
	// failures must be distinguished from unsupported discovery.
	Tags(ctx context.Context, last string, fn func(tags []string) error) error
}

// OCIStore is a CAS backend that stores blobs and action results in an OCI registry.
type OCIStore struct {
	repo          Registry
	mu            sync.Mutex
	outage        error
	lookupTimeout time.Duration
	probeGate     chan struct{}
	reachable     bool
}

// New creates an OCIStore backed by the given registry.
func New(repo Registry) *OCIStore {
	s := &OCIStore{repo: repo}
	if remoteRepo, ok := repo.(*remote.Repository); ok {
		if remoteRepo.Client == nil || remoteRepo.Client == auth.DefaultClient {
			client := *auth.DefaultClient
			client.Client = registryhttp.NewClient(registryhttp.Options{})
			client.Cache = auth.NewCache()
			remoteRepo.Client = &client
		}
		s.lookupTimeout = registryhttp.LookupTimeout
		s.probeGate = make(chan struct{}, 1)
		s.probeGate <- struct{}{}
	}
	return s
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
	if err := s.available(ctx); err != nil {
		return false, err
	}
	done, err := s.beginLookup(ctx)
	if err != nil {
		return false, err
	}
	defer done()

	parentCtx := ctx
	if s.lookupTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.lookupTimeout)
		defer cancel()
	}
	desc := ocispec.Descriptor{
		MediaType: MediaTypeMuBlob,
		Digest:    toOCIDigest(dgst),
	}
	ok, err := s.repo.Exists(ctx, desc)
	if err != nil {
		return false, s.failed(parentCtx, fmt.Errorf("oci: check blob %s: %w", dgst, err))
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
	if err := s.available(ctx); err != nil {
		return nil, err
	}
	ocidgst := toOCIDigest(dgst)

	// Remote repositories: bypass oras-go's blobStore.Fetch, which refuses
	// to return a body unless desc.Size matches the server's
	// Content-Length. We usually don't have the size (ActionResult.Outputs
	// stores digests only) and some registries return 404 on HEAD even
	// when GET works — so we can't reliably populate the size up front.
	// Go straight to a GET through the authed client.
	if rr, ok := s.repo.(*remote.Repository); ok {
		rc, err := remoteBlobGet(ctx, rr, ocidgst)
		if err != nil {
			return nil, s.failed(ctx, err)
		}
		return &outageReader{ReadCloser: rc, store: s, ctx: ctx}, nil
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
	if err := s.available(ctx); err != nil {
		return cas.Digest{}, err
	}
	// Buffer to temp file to get size (OCI requires content-length).
	tmp, err := os.CreateTemp("", "mu-oci-blob-*")
	if err != nil {
		return cas.Digest{}, fmt.Errorf("oci: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)

	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(tmp, h), cas.ContextReader(ctx, r))
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
			return cas.Digest{}, s.failed(ctx, fmt.Errorf("oci: push blob: %w", err))
		}
	}

	return fromOCIDigest(dgst), nil
}

// Delete removes the blob identified by dgst from the registry.
func (s *OCIStore) Delete(ctx context.Context, dgst cas.Digest) error {
	if err := s.available(ctx); err != nil {
		return err
	}
	desc := ocispec.Descriptor{
		MediaType: MediaTypeMuBlob,
		Digest:    toOCIDigest(dgst),
	}
	if err := s.repo.Delete(ctx, desc); err != nil {
		return s.failed(ctx, fmt.Errorf("oci: delete blob %s: %w", dgst, err))
	}
	return nil
}

// PutActionResult stores an action result as an OCI manifest tagged by the
// action key hash. Each output artifact becomes a layer descriptor, and the
// result metadata is stored as the config blob.
func (s *OCIStore) PutActionResult(ctx context.Context, key cas.ActionKey, result *cas.ActionResult) error {
	if err := s.available(ctx); err != nil {
		return err
	}
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
			return s.failed(ctx, fmt.Errorf("oci: push action config: %w", err))
		}
	}

	// Build layer descriptors from output artifacts. Size is looked up via
	// Resolve so the manifest carries the full descriptor; without it,
	// oras-go sets req.ContentLength = 0 on a push, Go's http client falls
	// back to chunked transfer encoding, and strict registries (e.g. Zot)
	// reject the monolithic-upload PUT with 400.
	layers := make([]ocispec.Descriptor, 0, len(result.Outputs))
	names := make([]string, 0, len(result.Outputs))
	for name := range result.Outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		dgst := result.Outputs[name]
		layer := ocispec.Descriptor{
			MediaType: MediaTypeMuBlob,
			Digest:    toOCIDigest(dgst),
			Annotations: map[string]string{
				// Standard OCI title (so registry UIs and `oras` name the layer)
				// plus the legacy mu key kept for back-compat.
				ocispec.AnnotationTitle: name,
				"mu.output.name":        name,
			},
		}
		if resolved, err := s.repo.Resolve(ctx, layer.Digest.String()); err == nil {
			layer.Size = resolved.Size
		}
		layers = append(layers, layer)
	}

	// Only deterministic annotations belong on a cache manifest: the entry is
	// content-addressed and tagged by action key, so a non-deterministic value
	// (e.g. a wall-clock `created`) would change the manifest digest on every
	// push, re-point the tag, and orphan the prior manifest. artifactType, output
	// titles, and exit code are all fixed for a given action.
	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypeMuAction,
		Config:       configDesc,
		Layers:       layers,
		Annotations: map[string]string{
			"dev.mu.exit-code": strconv.Itoa(result.ExitCode),
		},
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
			return s.failed(ctx, fmt.Errorf("oci: push action manifest: %w", err))
		}
	}

	// Tag the manifest with the action key for lookup.
	tag := actionKeyToTag(key)
	if err := s.repo.Tag(ctx, manifestDesc, tag); err != nil {
		return s.failed(ctx, fmt.Errorf("oci: tag action manifest: %w", err))
	}

	return nil
}

// GetActionResult retrieves an action result by resolving the tag derived from
// the action key. Returns (nil, nil) on a cache miss.
func (s *OCIStore) GetActionResult(ctx context.Context, key cas.ActionKey) (*cas.ActionResult, error) {
	if err := s.available(ctx); err != nil {
		return nil, err
	}
	done, err := s.beginLookup(ctx)
	if err != nil {
		return nil, err
	}
	defer done()

	parentCtx := ctx
	if s.lookupTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, s.lookupTimeout)
		defer cancel()
	}
	tag := actionKeyToTag(key)

	// Resolve the tag to get the manifest descriptor.
	manifestDesc, err := s.repo.Resolve(ctx, tag)
	if err != nil {
		if isNotFound(err) {
			return nil, nil // cache miss
		}
		return nil, s.failed(parentCtx, fmt.Errorf("oci: resolve action tag %s: %w", tag, err))
	}

	var manifest ocispec.Manifest
	if err := fetchMetadata(ctx, s.repo, manifestDesc, "action manifest", MaxManifestBytes, &manifest); err != nil {
		return nil, s.failed(parentCtx, err)
	}
	var result cas.ActionResult
	if err := fetchMetadata(ctx, s.repo, manifest.Config, "action result", MaxActionConfigBytes, &result); err != nil {
		return nil, s.failed(parentCtx, err)
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

// An OCIStore created for a CLI invocation remembers an unhealthy registry.
// Fresh commands construct a fresh store and retry; concurrent in-flight reads
// may still finish, but later actions never pay another outage probe.
func (s *OCIStore) available(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.outage
}
func (s *OCIStore) failed(ctx context.Context, err error) error {
	if _, remoteBackend := s.repo.(*remote.Repository); !remoteBackend || isNotFound(err) {
		return err
	}
	// Caller cancellation must not poison a healthy registry. The lookup's own
	// timeout is still an outage; distinguish it by the already established probe.
	if ctx.Err() != nil {
		return err
	}
	wrapped := fmt.Errorf("%w: %w", cas.ErrUnavailable, err)
	s.mu.Lock()
	if s.outage == nil {
		s.outage = wrapped
	}
	s.mu.Unlock()
	return wrapped
}

type outageReader struct {
	io.ReadCloser
	store *OCIStore
	ctx   context.Context
}

func (r *outageReader) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		err = r.store.failed(r.ctx, err)
	}
	return n, err
}

// Serialize only the first reachability probe. Concurrent cold actions share
// one outage penalty, while a healthy registry immediately regains parallelism.
func (s *OCIStore) beginLookup(ctx context.Context) (func(), error) {
	noop := func() {}
	if s.probeGate == nil {
		return noop, nil
	}
	s.mu.Lock()
	known := s.reachable
	s.mu.Unlock()
	if known {
		return noop, s.available(ctx)
	}
	select {
	case <-ctx.Done():
		return noop, ctx.Err()
	case <-s.probeGate:
	}
	release := func() { s.probeGate <- struct{}{} }
	if err := s.available(ctx); err != nil {
		release()
		return noop, err
	}
	s.mu.Lock()
	known = s.reachable
	s.mu.Unlock()
	if known {
		release()
		return noop, nil
	}
	return func() {
		s.mu.Lock()
		if s.outage == nil && ctx.Err() == nil {
			s.reachable = true
		}
		s.mu.Unlock()
		release()
	}, nil
}
