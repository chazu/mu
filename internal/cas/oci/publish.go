package oci

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"

	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"

	"github.com/chazu/mu/internal/cas"
)

// ArtifactMeta is the human-facing metadata captured for a published artifact.
// Unlike a cache entry (which stores only {Outputs, ExitCode}), a publish runs
// inside the build so it can record the target, the argv that produced the
// outputs, and side-band provenance (git revision, source, timestamp).
type ArtifactMeta struct {
	Target    string                // e.g. "//image/akashic"
	Command   []string              // argv that produced the outputs
	Created   string                // RFC3339 publish time
	MuVersion string                // mu CLI version
	Revision  string                // git commit sha (optional)
	Source    string                // git remote URL (optional)
	ExitCode  int                   // producing action's exit code
	Outputs   map[string]cas.Digest // output name -> content digest
}

// artifactConfig is the JSON config blob of a published artifact. It mirrors
// ArtifactMeta with outputs flattened to digest strings, and is what a registry
// UI reads to render the artifact detail (target, command, outputs table).
type artifactConfig struct {
	Target    string            `json:"target"`
	Command   []string          `json:"command,omitempty"`
	Created   string            `json:"created"`
	MuVersion string            `json:"mu_version,omitempty"`
	Revision  string            `json:"revision,omitempty"`
	Source    string            `json:"source,omitempty"`
	ExitCode  int               `json:"exit_code"`
	Outputs   map[string]string `json:"outputs"`
}

// PublishArtifact writes a published artifact (config + one layer per output +
// manifest) to the OCIStore's repository and applies each tag. Output blob bytes
// are read from src (the local CAS holding the just-built outputs). Returns the
// artifact manifest descriptor (used as the subject when attaching referrers).
//
// Each output is buffered in memory to fill its layer descriptor size (the
// cas.Store interface exposes no stat); adequate for the typical publish
// artifact (binary, archive, wasm). Layers are emitted in sorted output-name
// order so the manifest is deterministic for a given set of outputs.
func (s *OCIStore) PublishArtifact(ctx context.Context, meta ArtifactMeta, src cas.Store, tags []string) (ocispec.Descriptor, error) {
	if len(meta.Outputs) == 0 {
		return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: no outputs", meta.Target)
	}

	names := make([]string, 0, len(meta.Outputs))
	for name := range meta.Outputs {
		names = append(names, name)
	}
	sort.Strings(names)

	layers := make([]ocispec.Descriptor, 0, len(names))
	outStrings := make(map[string]string, len(names))
	for _, name := range names {
		dgst := meta.Outputs[name]
		outStrings[name] = dgst.String()

		rc, err := src.Get(ctx, dgst)
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: read output %q (%s): %w", meta.Target, name, dgst, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: read output %q: %w", meta.Target, name, err)
		}

		layer := ocispec.Descriptor{
			MediaType: MediaTypeMuBlob,
			Digest:    toOCIDigest(dgst),
			Size:      int64(len(data)),
			Annotations: map[string]string{
				ocispec.AnnotationTitle: name,
				"mu.output.name":        name,
			},
		}
		if err := pushIfAbsent(ctx, s.repo, layer, data); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: push output %q: %w", meta.Target, name, err)
		}
		layers = append(layers, layer)
	}

	cfg := artifactConfig{
		Target:    meta.Target,
		Command:   meta.Command,
		Created:   meta.Created,
		MuVersion: meta.MuVersion,
		Revision:  meta.Revision,
		Source:    meta.Source,
		ExitCode:  meta.ExitCode,
		Outputs:   outStrings,
	}
	configBytes, err := json.Marshal(cfg)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: marshal config: %w", meta.Target, err)
	}
	configDesc := ocispec.Descriptor{
		MediaType: MediaTypeMuArtifactConfig,
		Digest:    godigest.FromBytes(configBytes),
		Size:      int64(len(configBytes)),
	}
	if err := pushIfAbsent(ctx, s.repo, configDesc, configBytes); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: push config: %w", meta.Target, err)
	}

	// Manifest annotations mirror the config for tools that read descriptors
	// without fetching the config blob (standard OCI keys where they exist).
	ann := map[string]string{"dev.mu.target": meta.Target}
	if meta.Created != "" {
		ann[ocispec.AnnotationCreated] = meta.Created
	}
	if meta.Revision != "" {
		ann[ocispec.AnnotationRevision] = meta.Revision
	}
	if meta.Source != "" {
		ann[ocispec.AnnotationSource] = meta.Source
	}

	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypeMuArtifact,
		Config:       configDesc,
		Layers:       layers,
		Annotations:  ann,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: marshal manifest: %w", meta.Target, err)
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    godigest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
	}
	if err := pushIfAbsent(ctx, s.repo, manifestDesc, manifestBytes); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: push manifest: %w", meta.Target, err)
	}

	for _, tag := range tags {
		if err := s.repo.Tag(ctx, manifestDesc, tag); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("oci: publish %s: tag %q: %w", meta.Target, tag, err)
		}
	}
	return manifestDesc, nil
}

// mediaTypeEmptyConfig is the OCI 1.1 "empty" config blob for artifact manifests
// that carry no config of their own (referrers). Its content is the two bytes
// "{}".
const mediaTypeEmptyConfig = "application/vnd.oci.empty.v1+json"

// AttachReferrer pushes filename/data as an OCI 1.1 referrer of subject: a
// manifest with artifactType=<artifactType>, an empty config, one layer holding
// the file, and subject=<subject>. Pushed by digest (no tag). A remote registry
// that advertises OCI-Subject serves it via the Referrers API; oras-go writes
// the tag-schema fallback automatically otherwise. Returns the referrer digest.
func (s *OCIStore) AttachReferrer(ctx context.Context, subject ocispec.Descriptor, artifactType, filename string, data []byte, created string) (godigest.Digest, error) {
	if artifactType == "" {
		return "", fmt.Errorf("oci: attach %s: empty artifactType", filename)
	}

	layer := ocispec.Descriptor{
		MediaType: MediaTypeMuBlob,
		Digest:    godigest.FromBytes(data),
		Size:      int64(len(data)),
		Annotations: map[string]string{
			ocispec.AnnotationTitle: filename,
			"mu.output.name":        filename,
		},
	}
	if err := pushIfAbsent(ctx, s.repo, layer, data); err != nil {
		return "", fmt.Errorf("oci: attach %s: push file: %w", filename, err)
	}

	emptyConfig := []byte("{}")
	configDesc := ocispec.Descriptor{
		MediaType: mediaTypeEmptyConfig,
		Digest:    godigest.FromBytes(emptyConfig),
		Size:      int64(len(emptyConfig)),
	}
	if err := pushIfAbsent(ctx, s.repo, configDesc, emptyConfig); err != nil {
		return "", fmt.Errorf("oci: attach %s: push config: %w", filename, err)
	}

	ann := map[string]string{}
	if created != "" {
		ann[ocispec.AnnotationCreated] = created
	}
	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: artifactType,
		Config:       configDesc,
		Layers:       []ocispec.Descriptor{layer},
		Subject:      &subject,
		Annotations:  ann,
	}
	manifestBytes, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("oci: attach %s: marshal manifest: %w", filename, err)
	}
	// artifactType lives in the manifest body (above), not on this push
	// descriptor: a registry reads it from the body, and setting it here trips
	// oras-go's manifest indexing on some content stores.
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    godigest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
	}
	// Push by digest. oras-go's remote.Repository inspects the subject and manages
	// the Referrers API vs tag-schema fallback based on the registry's response.
	if err := pushIfAbsent(ctx, s.repo, manifestDesc, manifestBytes); err != nil {
		return "", fmt.Errorf("oci: attach %s: push manifest: %w", filename, err)
	}
	return manifestDesc.Digest, nil
}
