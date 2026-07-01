package oci_test

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/chazu/mu/internal/cas/oci"
)

// TestAttachReferrer verifies AttachReferrer builds a well-formed referrer
// manifest (artifactType, subject, empty config, titled layer). Uses the
// faithful digest-map registry (newTestRegistry) rather than oras-go's
// memory.Store, whose Exists() on a subject-bearing manifest no-ops the
// following Push — an artifact of that store, not of a real registry.
func TestAttachReferrer(t *testing.T) {
	ctx := context.Background()
	reg := newTestRegistry()
	store := oci.New(reg)

	subject := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    godigest.FromString("the-artifact"),
		Size:      42,
	}
	sbom := []byte(`{"spdxVersion":"SPDX-2.3"}`)

	refDg, err := store.AttachReferrer(ctx, subject, "application/vnd.mu.sbom.v1", "sbom.spdx.json", sbom, "2026-07-01T00:00:00Z")
	if err != nil {
		t.Fatalf("AttachReferrer: %v", err)
	}

	rc, err := reg.Fetch(ctx, ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: refDg})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(rc)
	rc.Close()

	var m ocispec.Manifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	if m.ArtifactType != "application/vnd.mu.sbom.v1" {
		t.Errorf("artifactType = %q", m.ArtifactType)
	}
	if m.Subject == nil || m.Subject.Digest != subject.Digest {
		t.Fatalf("subject = %+v, want %s", m.Subject, subject.Digest)
	}
	if m.Config.MediaType != "application/vnd.oci.empty.v1+json" {
		t.Errorf("config mediaType = %q, want empty config", m.Config.MediaType)
	}
	if len(m.Layers) != 1 || m.Layers[0].Annotations[ocispec.AnnotationTitle] != "sbom.spdx.json" {
		t.Fatalf("layers = %+v", m.Layers)
	}
}
