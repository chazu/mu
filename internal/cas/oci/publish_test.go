package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/chazu/mu/internal/cas"
)

// fakeSrc is a minimal cas.Store that serves a fixed set of blobs by digest.
// Only Get is exercised by PublishArtifact; the rest are stubs.
type fakeSrc struct{ blobs map[string][]byte }

func (f *fakeSrc) Get(_ context.Context, d cas.Digest) (io.ReadCloser, error) {
	b, ok := f.blobs[d.String()]
	if !ok {
		return nil, errors.New("not found")
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}
func (f *fakeSrc) Has(_ context.Context, d cas.Digest) (bool, error) {
	_, ok := f.blobs[d.String()]
	return ok, nil
}
func (f *fakeSrc) Put(context.Context, io.Reader) (cas.Digest, error) { panic("unused") }
func (f *fakeSrc) Delete(context.Context, cas.Digest) error           { panic("unused") }
func (f *fakeSrc) GetActionResult(context.Context, cas.ActionKey) (*cas.ActionResult, error) {
	panic("unused")
}
func (f *fakeSrc) PutActionResult(context.Context, cas.ActionKey, *cas.ActionResult) error {
	panic("unused")
}

func TestPublishArtifact(t *testing.T) {
	ctx := context.Background()
	binBytes := []byte("compiled binary bytes")
	metaBytes := []byte("{\"k\":1}")
	binDg, _ := cas.ComputeDigest(bytes.NewReader(binBytes))
	metaDg, _ := cas.ComputeDigest(bytes.NewReader(metaBytes))

	src := &fakeSrc{blobs: map[string][]byte{
		binDg.String():  binBytes,
		metaDg.String(): metaBytes,
	}}
	reg := newMemStore()
	store := New(reg)

	meta := ArtifactMeta{
		Target:    "//app",
		Command:   []string{"go", "build", "-o", "app"},
		Created:   "2026-07-01T00:00:00Z",
		MuVersion: "v0.3.3",
		Revision:  "deadbeef",
		Source:    "git@github.com:chazu/mu.git",
		ExitCode:  0,
		Outputs:   map[string]cas.Digest{"app": binDg, "meta.json": metaDg},
	}

	desc, err := store.PublishArtifact(ctx, meta, src, []string{"abc123", "latest"})
	if err != nil {
		t.Fatalf("PublishArtifact: %v", err)
	}

	// Both tags resolve to the same manifest digest.
	for _, tag := range []string{"abc123", "latest"} {
		d, err := reg.Resolve(ctx, tag)
		if err != nil {
			t.Fatalf("resolve %s: %v", tag, err)
		}
		if d.Digest != desc.Digest {
			t.Errorf("tag %s -> %s, want %s", tag, d.Digest, desc.Digest)
		}
	}

	// Fetch + inspect the manifest.
	mdesc, _ := reg.Resolve(ctx, "abc123")
	rc, err := reg.Fetch(ctx, mdesc)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()

	var m ocispec.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if m.ArtifactType != ArtifactTypeMuArtifact {
		t.Errorf("artifactType = %q, want %q", m.ArtifactType, ArtifactTypeMuArtifact)
	}
	if m.Config.MediaType != MediaTypeMuArtifactConfig {
		t.Errorf("config mediaType = %q", m.Config.MediaType)
	}
	if len(m.Layers) != 2 {
		t.Fatalf("layers = %d, want 2", len(m.Layers))
	}
	// Layers are sorted by output name: "app" then "meta.json".
	if got := m.Layers[0].Annotations[ocispec.AnnotationTitle]; got != "app" {
		t.Errorf("layer[0] title = %q, want app", got)
	}
	if got := m.Layers[1].Annotations["mu.output.name"]; got != "meta.json" {
		t.Errorf("layer[1] mu.output.name = %q, want meta.json", got)
	}
	if m.Annotations[ocispec.AnnotationRevision] != "deadbeef" {
		t.Errorf("manifest revision annotation = %q", m.Annotations[ocispec.AnnotationRevision])
	}

	// Fetch + decode the config blob.
	crc, err := reg.Fetch(ctx, m.Config)
	if err != nil {
		t.Fatal(err)
	}
	cbody, _ := io.ReadAll(crc)
	crc.Close()
	var cfg artifactConfig
	if err := json.Unmarshal(cbody, &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.Target != "//app" || len(cfg.Command) != 4 {
		t.Errorf("config = %+v", cfg)
	}
	if cfg.Outputs["app"] != binDg.String() {
		t.Errorf("config output app = %q, want %q", cfg.Outputs["app"], binDg.String())
	}
}
