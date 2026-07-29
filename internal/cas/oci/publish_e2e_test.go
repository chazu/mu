package oci

import (
	"bytes"
	"context"
	"io"
	"os"
	"testing"

	"oras.land/oras-go/v2/registry/remote"
	"oras.land/oras-go/v2/registry/remote/auth"

	"github.com/chazu/mu/internal/cas"
)

// e2eSrc is an in-memory cas.Store serving fixed blobs (see fakeSrc; duplicated
// minimal form to keep this file self-contained).
type e2eSrc struct{ blobs map[string][]byte }

func (s *e2eSrc) Get(_ context.Context, d cas.Digest) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(s.blobs[d.String()])), nil
}
func (s *e2eSrc) Has(_ context.Context, d cas.Digest) (bool, error) {
	_, ok := s.blobs[d.String()]
	return ok, nil
}
func (s *e2eSrc) Put(context.Context, io.Reader) (cas.Digest, error) { panic("unused") }
func (s *e2eSrc) Delete(context.Context, cas.Digest) error           { panic("unused") }
func (s *e2eSrc) GetActionResult(context.Context, cas.ActionKey) (*cas.ActionResult, error) {
	panic("unused")
}
func (s *e2eSrc) PutActionResult(context.Context, cas.ActionKey, *cas.ActionResult) error {
	panic("unused")
}

// TestPublishArtifact_E2E pushes a synthetic artifact to a real registry to
// confirm the push path (manifest + config + layers + tags) is accepted. Set
// MU_PUBLISH_E2E_REF (e.g. registry.platform.loosh.cloud/loosh-industries/mu-publish-e2e)
// and MU_PUBLISH_E2E_PAT (a push-capable snk_ token) to run; skipped otherwise.
func TestPublishArtifact_E2E(t *testing.T) {
	ref := os.Getenv("MU_PUBLISH_E2E_REF")
	pat := os.Getenv("MU_PUBLISH_E2E_PAT")
	if ref == "" || pat == "" {
		t.Skip("set MU_PUBLISH_E2E_REF and MU_PUBLISH_E2E_PAT to run the publish e2e test")
	}
	ctx := context.Background()

	payload := []byte("mu publish e2e output bytes")
	dg, _ := cas.ComputeDigest(bytes.NewReader(payload))
	src := &e2eSrc{blobs: map[string][]byte{dg.String(): payload}}

	repo, err := remote.NewRepository(ref)
	if err != nil {
		t.Fatal(err)
	}
	host := ref
	if i := indexByte(host, '/'); i >= 0 {
		host = host[:i]
	}
	repo.Client = &auth.Client{
		Cache: auth.NewCache(),
		Credential: auth.StaticCredential(host, auth.Credential{
			Username: "x", Password: pat,
		}),
	}

	meta := ArtifactMeta{
		Target:   "//e2e/app",
		Command:  []string{"echo", "hi"},
		Created:  "2026-07-01T00:00:00Z",
		ExitCode: 0,
		Outputs:  map[string]cas.Digest{"app": dg},
	}
	desc, err := New(repo).PublishArtifact(ctx, meta, src, []string{"e2e", "latest"})
	if err != nil {
		t.Fatalf("PublishArtifact: %v", err)
	}
	t.Logf("published %s:e2e -> %s", ref, desc.Digest)

	// Confirm the tag resolves to what we pushed.
	got, err := repo.Resolve(ctx, "e2e")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if got.Digest != desc.Digest {
		t.Fatalf("resolved %s, want %s", got.Digest, desc.Digest)
	}
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
