package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// pluginTagDigestLen is the number of leading hex chars retained in a
// plugin tag. 12 hex chars = 48 bits, well past birthday-collision risk
// at any plausible plugin count per registry.
const pluginTagDigestLen = 12

const (
	// MediaTypePluginConfig is the config blob media type for a mu plugin artifact.
	MediaTypePluginConfig = "application/vnd.mu.plugin.v1+json"

	// MediaTypePluginFile is the layer media type for a single plugin file.
	MediaTypePluginFile = "application/vnd.mu.plugin.file.v1"

	// MediaTypePluginIndexConfig is the config blob media type for the plugin index.
	MediaTypePluginIndexConfig = "application/vnd.mu.plugin-index.v1+json"

	// ArtifactTypePlugin is the OCI 1.1 artifactType for a plugin artifact.
	ArtifactTypePlugin = "application/vnd.mu.plugin.v1"

	// ArtifactTypePluginIndex is the OCI 1.1 artifactType for the plugin index.
	ArtifactTypePluginIndex = "application/vnd.mu.plugin-index.v1"

	// PluginRepoPrefix is the path segment under which mu plugins live within
	// the configured cache push repository.
	PluginRepoPrefix = "mu/plugins"

	// PluginIndexRef is the path under cache.push.repository where the plugin
	// index is tagged. Full ref: "<registry>/<repository>/mu/plugin-index:v1".
	PluginIndexRef = "mu/plugin-index"

	// PluginIndexTag is the tag used for the plugin index artifact.
	PluginIndexTag = "v1"
)

// PluginConfig is the JSON payload of a plugin artifact's config blob.
// It mirrors config.PluginManifest but is self-contained so the artifact
// is interpretable without the source project.
type PluginConfig struct {
	Name       string   `json:"name"`
	Entrypoint string   `json:"entrypoint"`
	Toolchain  string   `json:"toolchain,omitempty"`
	Files      []string `json:"files,omitempty"`
	Guide      string   `json:"guide,omitempty"`
	// Digest is the CAS content digest of the plugin's primary artifact (the
	// bundle for multi-file plugins, the entrypoint script for single-file
	// plugins). Format: "sha256:<hex>". Used to derive the OCI tag via PluginTag
	// and to round-trip the plugin back to the local CAS on install.
	Digest string `json:"digest,omitempty"`
	Source     string   `json:"source,omitempty"`
}

// PluginIndex is the JSON payload of the plugin-index artifact's config blob.
// It lists plugin names known in this registry's mu namespace so clients can
// enumerate without relying on the OCI _catalog endpoint.
type PluginIndex struct {
	SchemaVersion int      `json:"schemaVersion"`
	Plugins       []string `json:"plugins"`
}

// PluginTag returns the OCI tag used for a plugin artifact given its sha256
// hex digest. A leading "sha256:" prefix is stripped if present, so callers
// can pass either the bare hex string or a full digest reference. We use the
// first 12 hex chars so tags are short, stable, and don't collide with future
// semver-tagging schemes (which would not start with "sha256-").
func PluginTag(sha256Hex string) string {
	sha256Hex = strings.TrimPrefix(sha256Hex, "sha256:")
	if len(sha256Hex) > pluginTagDigestLen {
		sha256Hex = sha256Hex[:pluginTagDigestLen]
	}
	return "sha256-" + sha256Hex
}

// PushPlugin writes a plugin artifact (config + file layers + manifest) to repo
// and tags it with PluginTag(cfg.Digest). Each entry in cfg.Files MUST be
// present as a key in the files map; layer order follows cfg.Files for
// determinism. Returns the manifest descriptor (with ArtifactType set).
//
// PushPlugin is idempotent: re-pushing the same plugin (same digest, same
// content) is a no-op at the blob and manifest level — only the tag is
// overwritten.
func PushPlugin(ctx context.Context, repo Registry, name string, cfg PluginConfig, files map[string][]byte) (ocispec.Descriptor, error) {
	cfgBytes, err := json.Marshal(&cfg)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("marshal plugin config: %w", err)
	}
	cfgDesc := ocispec.Descriptor{
		MediaType: MediaTypePluginConfig,
		Digest:    godigest.FromBytes(cfgBytes),
		Size:      int64(len(cfgBytes)),
	}
	if err := pushIfAbsent(ctx, repo, cfgDesc, cfgBytes); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push plugin config: %w", err)
	}

	// Layers in cfg.Files order for determinism.
	layers := make([]ocispec.Descriptor, 0, len(cfg.Files))
	seen := make(map[string]bool, len(cfg.Files))
	for _, path := range cfg.Files {
		if seen[path] {
			continue
		}
		seen[path] = true
		content, ok := files[path]
		if !ok {
			return ocispec.Descriptor{}, fmt.Errorf("plugin %q: file %q in cfg.Files but not in files map", name, path)
		}
		desc := ocispec.Descriptor{
			MediaType: MediaTypePluginFile,
			Digest:    godigest.FromBytes(content),
			Size:      int64(len(content)),
			Annotations: map[string]string{
				ocispec.AnnotationTitle: path,
			},
		}
		if err := pushIfAbsent(ctx, repo, desc, content); err != nil {
			return ocispec.Descriptor{}, fmt.Errorf("push plugin file %q: %w", path, err)
		}
		layers = append(layers, desc)
	}

	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypePlugin,
		Config:       cfgDesc,
		Layers:       layers,
		Annotations: map[string]string{
			ocispec.AnnotationTitle: name,
			"vnd.mu.plugin.digest":  cfg.Digest,
		},
	}
	mb, err := json.Marshal(&manifest)
	if err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("marshal plugin manifest: %w", err)
	}
	mdesc := ocispec.Descriptor{
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypePlugin,
		Digest:       godigest.FromBytes(mb),
		Size:         int64(len(mb)),
	}
	if err := pushIfAbsent(ctx, repo, mdesc, mb); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("push plugin manifest: %w", err)
	}

	tag := PluginTag(cfg.Digest)
	if err := repo.Tag(ctx, mdesc, tag); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("tag plugin %s: %w", tag, err)
	}
	return mdesc, nil
}

// FetchPluginConfig resolves ref to a manifest, validates it is a mu plugin
// artifact (config media type == MediaTypePluginConfig), and returns the
// decoded PluginConfig. Returns a clear error if the artifact at ref is not
// a mu plugin (e.g. another tool wrote to the same tag).
func FetchPluginConfig(ctx context.Context, repo Registry, ref string) (PluginConfig, error) {
	mdesc, err := repo.Resolve(ctx, ref)
	if err != nil {
		return PluginConfig{}, fmt.Errorf("resolve %s: %w", ref, err)
	}
	mrc, err := repo.Fetch(ctx, mdesc)
	if err != nil {
		return PluginConfig{}, fmt.Errorf("fetch manifest %s: %w", ref, err)
	}
	mb, err := io.ReadAll(mrc)
	mrc.Close()
	if err != nil {
		return PluginConfig{}, fmt.Errorf("read manifest %s: %w", ref, err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(mb, &manifest); err != nil {
		return PluginConfig{}, fmt.Errorf("decode manifest %s: %w", ref, err)
	}
	if manifest.Config.MediaType != MediaTypePluginConfig {
		return PluginConfig{}, fmt.Errorf("ref %s is not a mu plugin: config media type %q (want %q)",
			ref, manifest.Config.MediaType, MediaTypePluginConfig)
	}
	crc, err := repo.Fetch(ctx, manifest.Config)
	if err != nil {
		return PluginConfig{}, fmt.Errorf("fetch plugin config: %w", err)
	}
	cb, err := io.ReadAll(crc)
	crc.Close()
	if err != nil {
		return PluginConfig{}, fmt.Errorf("read plugin config: %w", err)
	}
	var cfg PluginConfig
	if err := json.Unmarshal(cb, &cfg); err != nil {
		return PluginConfig{}, fmt.Errorf("decode plugin config: %w", err)
	}
	return cfg, nil
}

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
	return repo.Push(ctx, desc, bytes.NewReader(content))
}
