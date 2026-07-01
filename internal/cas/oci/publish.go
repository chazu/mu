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
	Target    string                // e.g. "//image/snooker"
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
// manifest digest.
//
// Each output is buffered in memory to fill its layer descriptor size (the
// cas.Store interface exposes no stat); adequate for the typical publish
// artifact (binary, archive, wasm). Layers are emitted in sorted output-name
// order so the manifest is deterministic for a given set of outputs.
func (s *OCIStore) PublishArtifact(ctx context.Context, meta ArtifactMeta, src cas.Store, tags []string) (godigest.Digest, error) {
	if len(meta.Outputs) == 0 {
		return "", fmt.Errorf("oci: publish %s: no outputs", meta.Target)
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
			return "", fmt.Errorf("oci: publish %s: read output %q (%s): %w", meta.Target, name, dgst, err)
		}
		data, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return "", fmt.Errorf("oci: publish %s: read output %q: %w", meta.Target, name, err)
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
			return "", fmt.Errorf("oci: publish %s: push output %q: %w", meta.Target, name, err)
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
		return "", fmt.Errorf("oci: publish %s: marshal config: %w", meta.Target, err)
	}
	configDesc := ocispec.Descriptor{
		MediaType: MediaTypeMuArtifactConfig,
		Digest:    godigest.FromBytes(configBytes),
		Size:      int64(len(configBytes)),
	}
	if err := pushIfAbsent(ctx, s.repo, configDesc, configBytes); err != nil {
		return "", fmt.Errorf("oci: publish %s: push config: %w", meta.Target, err)
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
		return "", fmt.Errorf("oci: publish %s: marshal manifest: %w", meta.Target, err)
	}
	manifestDesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    godigest.FromBytes(manifestBytes),
		Size:      int64(len(manifestBytes)),
	}
	if err := pushIfAbsent(ctx, s.repo, manifestDesc, manifestBytes); err != nil {
		return "", fmt.Errorf("oci: publish %s: push manifest: %w", meta.Target, err)
	}

	for _, tag := range tags {
		if err := s.repo.Tag(ctx, manifestDesc, tag); err != nil {
			return "", fmt.Errorf("oci: publish %s: tag %q: %w", meta.Target, tag, err)
		}
	}
	return manifestDesc.Digest, nil
}
