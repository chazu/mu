package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"

	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/content/memory"
)

func TestPluginConfigRoundTrip(t *testing.T) {
	src := PluginConfig{
		Name:       "fmt",
		Entrypoint: "fmt.bb",
		Toolchain:  "bb",
		Files:      []string{"fmt.bb"},
		Guide:      "GUIDE.md",
		Digest:     "sha256:abc123",
		Source:     "https://github.com/example/mu-plugins",
	}
	b, err := json.Marshal(&src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PluginConfig
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Name != src.Name || got.Entrypoint != src.Entrypoint ||
		got.Toolchain != src.Toolchain || got.Guide != src.Guide ||
		got.Digest != src.Digest || got.Source != src.Source ||
		!slices.Equal(got.Files, src.Files) {
		t.Fatalf("round-trip mismatch:\n got: %+v\nwant: %+v", got, src)
	}
}

func TestPluginIndexRoundTrip(t *testing.T) {
	src := PluginIndex{
		SchemaVersion: 1,
		Plugins:       []string{"fmt", "lint", "test"},
	}
	b, err := json.Marshal(&src)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got PluginIndex
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.SchemaVersion != 1 || !slices.Equal(got.Plugins, src.Plugins) {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}

func TestPluginTagStripsSha256Prefix(t *testing.T) {
	full := "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	want := "sha256-0123456789ab"
	if got := PluginTag(full); got != want {
		t.Fatalf("PluginTag(%q) = %q, want %q", full, got, want)
	}
}

func TestPluginTagShortensDigest(t *testing.T) {
	// 64-char sha256 hex → "sha256-" + first 12 chars.
	full := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	want := "sha256-0123456789ab"
	if got := PluginTag(full); got != want {
		t.Fatalf("PluginTag(%q) = %q, want %q", full, got, want)
	}
	// Short input passes through with prefix.
	if got := PluginTag("abc"); got != "sha256-abc" {
		t.Fatalf("PluginTag(short) = %q, want sha256-abc", got)
	}
}

// memStore wraps memory.Store and adds stub Delete/Tags to satisfy Registry.
type memStore struct {
	*memory.Store
}

func (m *memStore) Delete(_ context.Context, _ ocispec.Descriptor) error { return nil }
func (m *memStore) Tags(_ context.Context, last string, fn func([]string) error) error {
	return nil
}

func newMemStore() *memStore { return &memStore{Store: memory.New()} }

func TestPushAndFetchPlugin(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()

	cfg := PluginConfig{
		Name:       "fmt",
		Entrypoint: "fmt.bb",
		Toolchain:  "bb",
		Files:      []string{"fmt.bb"},
		Digest:     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	files := map[string][]byte{
		"fmt.bb": []byte("#!/usr/bin/env bb\n(println :hello)\n"),
	}

	desc, err := PushPlugin(ctx, store, "fmt", cfg, files)
	if err != nil {
		t.Fatalf("PushPlugin: %v", err)
	}
	if desc.Digest == "" {
		t.Fatal("expected non-empty manifest digest")
	}
	if desc.MediaType != ocispec.MediaTypeImageManifest {
		t.Fatalf("expected manifest media type, got %q", desc.MediaType)
	}
	if desc.ArtifactType != ArtifactTypePlugin {
		t.Fatalf("expected artifactType %q, got %q", ArtifactTypePlugin, desc.ArtifactType)
	}

	tag := PluginTag(cfg.Digest)
	got, err := FetchPluginConfig(ctx, store, tag)
	if err != nil {
		t.Fatalf("FetchPluginConfig: %v", err)
	}
	if got.Name != "fmt" || got.Entrypoint != "fmt.bb" || got.Toolchain != "bb" {
		t.Fatalf("unexpected config: %+v", got)
	}
}

func TestPushPluginFilesMatchLayers(t *testing.T) {
	// Verify that each file in the bundle becomes a separate layer with the
	// correct title annotation and media type.
	ctx := context.Background()
	store := newMemStore()

	cfg := PluginConfig{
		Name:       "multi",
		Entrypoint: "main.bb",
		Files:      []string{"main.bb", "lib.bb"},
		Digest:     "sha256:abcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabcabca",
	}
	files := map[string][]byte{
		"main.bb": []byte("(println :main)"),
		"lib.bb":  []byte("(println :lib)"),
	}

	mdesc, err := PushPlugin(ctx, store, "multi", cfg, files)
	if err != nil {
		t.Fatalf("PushPlugin: %v", err)
	}

	// Re-fetch the manifest and verify layers.
	mrc, err := store.Fetch(ctx, mdesc)
	if err != nil {
		t.Fatalf("fetch manifest: %v", err)
	}
	defer mrc.Close()
	mb, err := io.ReadAll(mrc)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(mb, &manifest); err != nil {
		t.Fatalf("decode manifest: %v", err)
	}
	if len(manifest.Layers) != 2 {
		t.Fatalf("expected 2 layers, got %d", len(manifest.Layers))
	}
	titles := []string{}
	for _, l := range manifest.Layers {
		if l.MediaType != MediaTypePluginFile {
			t.Errorf("layer %s has media type %q, want %q", l.Digest, l.MediaType, MediaTypePluginFile)
		}
		titles = append(titles, l.Annotations[ocispec.AnnotationTitle])
	}
	// Order should follow cfg.Files order (deterministic).
	if titles[0] != "main.bb" || titles[1] != "lib.bb" {
		t.Fatalf("layer order: got %v, want [main.bb lib.bb]", titles)
	}
}

func TestFetchPluginConfigRejectsWrongMediaType(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()

	// Hand-craft a manifest with a non-mu config media type and tag it like a plugin.
	badConfig := []byte(`{"arbitrary":"json"}`)
	badDesc := ocispec.Descriptor{
		MediaType: "application/vnd.oci.image.config.v1+json",
		Digest:    godigest.FromBytes(badConfig),
		Size:      int64(len(badConfig)),
	}
	if err := store.Push(ctx, badDesc, bytes.NewReader(badConfig)); err != nil {
		t.Fatalf("push bad config: %v", err)
	}
	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: "application/vnd.example.other",
		Config:       badDesc,
	}
	mb, _ := json.Marshal(&manifest)
	mdesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    godigest.FromBytes(mb),
		Size:      int64(len(mb)),
	}
	if err := store.Push(ctx, mdesc, bytes.NewReader(mb)); err != nil {
		t.Fatalf("push bad manifest: %v", err)
	}
	if err := store.Tag(ctx, mdesc, "sha256-cafebabecafe"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	_, err := FetchPluginConfig(ctx, store, "sha256-cafebabecafe")
	if err == nil {
		t.Fatal("expected error when fetching non-mu artifact, got nil")
	}
}

func TestPushPluginErrorsOnMissingFile(t *testing.T) {
	ctx := context.Background()
	store := newMemStore()
	cfg := PluginConfig{
		Name:       "broken",
		Entrypoint: "main.bb",
		Files:      []string{"main.bb", "missing.bb"},
		Digest:     "sha256:" + strings.Repeat("a", 64),
	}
	files := map[string][]byte{"main.bb": []byte("x")}
	_, err := PushPlugin(ctx, store, "broken", cfg, files)
	if err == nil {
		t.Fatal("expected error for file in cfg.Files but missing in files map, got nil")
	}
}
