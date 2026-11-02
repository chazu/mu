// Package oci implements a content-addressable store backed by an OCI registry.
//
// OCIStore works against a remote OCI registry (via New with a
// remote.Repository) or any Registry implementation. Blobs map directly to OCI
// blobs. Action results are stored as OCI manifests tagged by the action key
// hash, with each output as a layer descriptor and the result metadata as the
// config blob, so standard OCI tools (crane, skopeo, oras) can inspect a
// remote cache. The local cache is a cas.DiskStore (see NewLocal); its
// per-key action results become manifests only when pushed (PushActionResult).
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
	"time"

	"net/http"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/registryhttp"
	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"
	"oras.land/oras-go/v2/registry/remote/errcode"
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
// It is the remote side of the build cache (see cas.Cache): it reports
// failures as plain errors and leaves deciding what an outage means to the
// caller.
type OCIStore struct {
	repo Registry
	// lookupTimeout bounds small metadata lookups (Has, GetActionResult) so
	// an unresponsive registry cannot stall a build. Zero means no bound.
	lookupTimeout time.Duration
	// host is the registry host, named in authentication-failure hints.
	host string
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
		s.host = remoteRepo.Reference.Registry
	}
	return s
}

// authErr turns an authentication failure into an AuthError naming s's host
// and the login command; other errors pass through.
func (s *OCIStore) authErr(err error) error { return asAuthError(s.host, err) }

// NewLocal opens the local disk cache at path, creating it if needed. It is a
// convenience alias for cas.NewDiskStore kept for existing callers; the local
// cache is no longer an OCI layout (see cas.DiskStore).
func NewLocal(path string) (*cas.DiskStore, error) {
	return cas.NewDiskStore(path)
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
	if err := dgst.Validate(); err != nil {
		return false, fmt.Errorf("oci: check blob %s: %w", dgst, err)
	}
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
		return false, s.authErr(fmt.Errorf("oci: check blob %s: %w", dgst, err))
	}
	return ok, nil
}

// Get fetches the blob identified by dgst from the registry. When the
// underlying repo is a remote registry, oras-go's blobStore.Fetch validates
// the response's Content-Length against desc.Size; passing Size=0 would
// falsely trip that check. We therefore resolve the blob's size first
// (HEAD for remote; layout index for local) and populate the descriptor
// before fetching. The returned reader is digest-verified (cas.VerifyReader):
// registry bytes that do not hash to dgst fail at EOF with
// cas.ErrDigestMismatch.
func (s *OCIStore) Get(ctx context.Context, dgst cas.Digest) (io.ReadCloser, error) {
	rc, err := s.get(ctx, dgst)
	if err != nil {
		return nil, s.authErr(err)
	}
	return cas.VerifyReader(rc, dgst, nil), nil
}

func (s *OCIStore) get(ctx context.Context, dgst cas.Digest) (io.ReadCloser, error) {
	if err := dgst.Validate(); err != nil {
		return nil, fmt.Errorf("oci: get blob %s: %w", dgst, err)
	}
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
		if isNotFound(err) {
			return nil, fmt.Errorf("oci: fetch blob %s: %w", dgst, cas.ErrNotFound)
		}
		return nil, fmt.Errorf("oci: fetch blob %s: %w", dgst, err)
	}
	return rc, nil
}

// remoteBlobGet streams a blob body directly from a remote repository via
// its authenticated Client. It skips oras-go's Content-Length validation;
// Get verifies the content digest as the body streams.
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
		return nil, fmt.Errorf("oci: fetch blob %s: %w", dgst, cas.ErrNotFound)
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("oci: fetch blob %s: %w", dgst,
			&errcode.ErrorResponse{Method: req.Method, URL: req.URL, StatusCode: resp.StatusCode})
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
			return cas.Digest{}, s.authErr(fmt.Errorf("oci: push blob: %w", err))
		}
	}

	return fromOCIDigest(dgst), nil
}

// Delete removes the blob identified by dgst from the registry.
func (s *OCIStore) Delete(ctx context.Context, dgst cas.Digest) error {
	if err := dgst.Validate(); err != nil {
		return fmt.Errorf("oci: delete blob %s: %w", dgst, err)
	}
	desc := ocispec.Descriptor{
		MediaType: MediaTypeMuBlob,
		Digest:    toOCIDigest(dgst),
	}
	if err := s.repo.Delete(ctx, desc); err != nil {
		return s.authErr(fmt.Errorf("oci: delete blob %s: %w", dgst, err))
	}
	return nil
}

// PutActionResult stores an action result as an OCI manifest tagged by the
// action key hash. Each output artifact becomes a layer descriptor, and the
// result metadata is stored as the config blob.
func (s *OCIStore) PutActionResult(ctx context.Context, key cas.ActionKey, result *cas.ActionResult) error {
	return s.authErr(s.putActionResult(ctx, key, result, nil))
}

// putActionResult is PutActionResult with known output blob sizes. Outputs
// missing from sizes have their size looked up via Resolve.
func (s *OCIStore) putActionResult(ctx context.Context, key cas.ActionKey, result *cas.ActionResult, sizes map[godigest.Digest]int64) error {
	if err := key.Digest.Validate(); err != nil {
		return fmt.Errorf("oci: action key: %w", err)
	}
	for name, out := range result.Outputs {
		if err := out.Validate(); err != nil {
			return fmt.Errorf("oci: action result output %q: %w", name, err)
		}
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
			return fmt.Errorf("oci: push action config: %w", err)
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
		if size, ok := sizes[layer.Digest]; ok {
			layer.Size = size
		} else if resolved, err := s.repo.Resolve(ctx, layer.Digest.String()); err == nil {
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

// PushActionResult copies one action result from the local disk cache to s:
// every output blob missing from the registry is uploaded from local, then the
// OCI manifest and action tag are built and pushed exactly as PutActionResult
// does. It is how `mu cache push` publishes per-key local receipts.
func (s *OCIStore) PushActionResult(ctx context.Context, local *cas.DiskStore, key cas.ActionKey, result *cas.ActionResult) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	names := make([]string, 0, len(result.Outputs))
	for name := range result.Outputs {
		names = append(names, name)
	}
	sort.Strings(names)
	sizes := make(map[godigest.Digest]int64, len(names))
	for _, name := range names {
		out := result.Outputs[name]
		if _, done := sizes[toOCIDigest(out)]; done {
			continue
		}
		size, err := s.pushLocalBlob(ctx, local, out)
		if err != nil {
			return s.authErr(fmt.Errorf("oci: push output %q: %w", name, err))
		}
		sizes[toOCIDigest(out)] = size
	}
	return s.authErr(s.putActionResult(ctx, key, result, sizes))
}

// pushLocalBlob uploads one local blob unless the registry already has it,
// returning its size.
func (s *OCIStore) pushLocalBlob(ctx context.Context, local *cas.DiskStore, dgst cas.Digest) (int64, error) {
	p, err := local.BlobPath(dgst)
	if err != nil {
		return 0, err
	}
	f, err := os.Open(p)
	if err != nil {
		return 0, fmt.Errorf("local blob %s: %w", dgst, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return 0, fmt.Errorf("local blob %s: %w", dgst, err)
	}
	desc := ocispec.Descriptor{MediaType: MediaTypeMuBlob, Digest: toOCIDigest(dgst), Size: info.Size()}
	exists, err := s.repo.Exists(ctx, desc)
	if err != nil {
		return 0, fmt.Errorf("check blob %s: %w", dgst, err)
	}
	if exists {
		return desc.Size, nil
	}
	if err := s.repo.Push(ctx, desc, cas.ContextReader(ctx, f)); err != nil && !isAlreadyExists(err) {
		return 0, fmt.Errorf("push blob %s: %w", dgst, err)
	}
	return desc.Size, nil
}

// HasActionResult reports whether the registry already has a result for key
// (its action tag resolves). A tag is applied only after its manifest's
// blobs were pushed, so a present tag means a complete entry.
func (s *OCIStore) HasActionResult(ctx context.Context, key cas.ActionKey) (bool, error) {
	if err := key.Digest.Validate(); err != nil {
		return false, fmt.Errorf("oci: action key: %w", err)
	}
	tag := actionKeyToTag(key)
	if _, err := s.repo.Resolve(ctx, tag); err != nil {
		if isNotFound(err) {
			return false, nil
		}
		return false, s.authErr(fmt.Errorf("oci: resolve action tag %s: %w", tag, err))
	}
	return true, nil
}

// ActionTag returns the registry tag under which the result for key is stored.
func ActionTag(key cas.ActionKey) string { return actionKeyToTag(key) }

// GetActionResult retrieves an action result by resolving the tag derived from
// the action key. Returns (nil, nil) on a cache miss.
func (s *OCIStore) GetActionResult(ctx context.Context, key cas.ActionKey) (*cas.ActionResult, error) {
	result, err := s.getActionResult(ctx, key)
	return result, s.authErr(err)
}

func (s *OCIStore) getActionResult(ctx context.Context, key cas.ActionKey) (*cas.ActionResult, error) {
	if err := key.Digest.Validate(); err != nil {
		return nil, fmt.Errorf("oci: action key: %w", err)
	}
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
		return nil, fmt.Errorf("oci: resolve action tag %s: %w", tag, err)
	}

	var manifest ocispec.Manifest
	if err := fetchMetadata(ctx, s.repo, manifestDesc, "action manifest", MaxManifestBytes, &manifest); err != nil {
		return nil, err
	}
	var result cas.ActionResult
	if err := fetchMetadata(ctx, s.repo, manifest.Config, "action result", MaxActionConfigBytes, &result); err != nil {
		return nil, err
	}
	// A receipt is untrusted registry content. Reject output identities that
	// could not name stored content (or could name paths) before any caller
	// restores from them; this is a corrupt entry, not a registry outage.
	for name, out := range result.Outputs {
		if err := out.Validate(); err != nil {
			return nil, fmt.Errorf("oci: action result %s output %q: %w", tag, name, err)
		}
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
