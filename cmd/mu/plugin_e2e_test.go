package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas/oci"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	ocilayout "oras.land/oras-go/v2/content/oci"
)

// nameFilteredRegistry wraps an oci.Registry and restricts Tags to only yield
// sha256- tags whose fetched PluginConfig.Name matches wantName. This simulates
// the per-plugin sub-repository scoping that real deployments get for free
// (each plugin lives under its own path in the registry).
type nameFilteredRegistry struct {
	oci.Registry
	wantName string
}

func (r *nameFilteredRegistry) Tags(ctx context.Context, last string, fn func([]string) error) error {
	return r.Registry.Tags(ctx, last, func(tags []string) error {
		var filtered []string
		for _, t := range tags {
			if !strings.HasPrefix(t, "sha256-") {
				continue
			}
			cfg, err := oci.FetchPluginConfig(ctx, r.Registry, t)
			if err != nil {
				continue
			}
			if cfg.Name == r.wantName {
				filtered = append(filtered, t)
			}
		}
		if len(filtered) == 0 {
			return nil
		}
		return fn(filtered)
	})
}

// Compile-time check that nameFilteredRegistry satisfies oci.Registry.
var _ oci.Registry = (*nameFilteredRegistry)(nil)

// Satisfy oci.Registry methods not overridden (delegation via embedding handles
// Push, Fetch, Exists, Delete, Tag, Resolve).
func (r *nameFilteredRegistry) Push(ctx context.Context, expected ocispec.Descriptor, content io.Reader) error {
	return r.Registry.Push(ctx, expected, content)
}
func (r *nameFilteredRegistry) Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error) {
	return r.Registry.Fetch(ctx, target)
}
func (r *nameFilteredRegistry) Exists(ctx context.Context, target ocispec.Descriptor) (bool, error) {
	return r.Registry.Exists(ctx, target)
}
func (r *nameFilteredRegistry) Delete(ctx context.Context, target ocispec.Descriptor) error {
	return r.Registry.Delete(ctx, target)
}
func (r *nameFilteredRegistry) Tag(ctx context.Context, desc ocispec.Descriptor, reference string) error {
	return r.Registry.Tag(ctx, desc, reference)
}
func (r *nameFilteredRegistry) Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error) {
	return r.Registry.Resolve(ctx, reference)
}

// TestPushAndListEndToEndAgainstOCILayout drives the full push + discover
// pipeline against a real oras-go local OCI layout — the same on-disk format
// `oras pull` produces from a registry. This proves the Registry interface
// contract holds without the unit-test fakes.
func TestPushAndListEndToEndAgainstOCILayout(t *testing.T) {
	ctx := context.Background()

	layoutDir := filepath.Join(t.TempDir(), "oci-layout")
	if err := os.MkdirAll(layoutDir, 0o755); err != nil {
		t.Fatal(err)
	}

	store, err := ocilayout.New(layoutDir)
	if err != nil {
		t.Fatalf("ocilayout.New: %v", err)
	}

	// Push two plugins. In production each plugin has its own sub-repo, but
	// a local OCI layout is repo-agnostic — tags coexist as long as they are
	// unique. PushPlugin's tag scheme (sha256-<12>) and UpdatePluginIndex's
	// tag (v1) can't collide.
	type seed struct {
		name   string
		digest string
		body   []byte
	}
	seeds := []seed{
		{"fmt", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef", []byte("(println :fmt)")},
		{"lint", "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210", []byte("(println :lint)")},
	}
	for _, s := range seeds {
		cfg := oci.PluginConfig{
			Name:       s.name,
			Entrypoint: s.name + ".bb",
			Toolchain:  "bb",
			Files:      []string{s.name + ".bb"},
			Digest:     s.digest,
		}
		files := map[string][]byte{s.name + ".bb": s.body}
		if _, err := pushPluginToRegistry(ctx, store, store, cfg, files); err != nil {
			t.Fatalf("pushPluginToRegistry %s: %v", s.name, err)
		}
	}

	// Verify the on-disk layout is well-formed: re-opening it succeeds.
	if _, err := ocilayout.New(layoutDir); err != nil {
		t.Fatalf("layout not well-formed after push: %v", err)
	}
	// Spot-check an expected file: blobs/sha256/ should exist with content.
	blobsDir := filepath.Join(layoutDir, "blobs", "sha256")
	entries, err := os.ReadDir(blobsDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("expected blob files under %s, got err=%v entries=%d", blobsDir, err, len(entries))
	}

	// Discover. The opener wraps the single store in a per-plugin name filter
	// to simulate per-plugin sub-repo scoping that real registry deployments
	// get naturally (each plugin lives at its own path in the registry).
	opener := func(name string) (oci.Registry, error) {
		return &nameFilteredRegistry{Registry: store, wantName: name}, nil
	}
	rows, err := listFromBackend(ctx, "test-layout", store, opener)
	if err != nil {
		t.Fatalf("listFromBackend: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(rows), rows)
	}

	gotNames := map[string]bool{}
	for _, r := range rows {
		gotNames[r.Name] = true
		if r.Location != "test-layout" {
			t.Errorf("row %s Location = %q", r.Name, r.Location)
		}
		// Verify the version tag is the expected sha256-<12> form.
		want := oci.PluginTag(r.Digest)
		if r.Version != want {
			t.Errorf("row %s Version = %q, want %q", r.Name, r.Version, want)
		}
	}
	for _, s := range seeds {
		if !gotNames[s.name] {
			t.Errorf("missing %q in discovered rows", s.name)
		}
	}

	// Round-trip: fetch one plugin's config back via FetchPluginConfig and
	// verify it matches what we pushed.
	fmtCfg, err := oci.FetchPluginConfig(ctx, store, oci.PluginTag(seeds[0].digest))
	if err != nil {
		t.Fatalf("FetchPluginConfig: %v", err)
	}
	if fmtCfg.Name != "fmt" || fmtCfg.Entrypoint != "fmt.bb" || fmtCfg.Toolchain != "bb" {
		t.Fatalf("round-tripped config: %+v", fmtCfg)
	}
	if fmtCfg.Digest != seeds[0].digest {
		t.Errorf("Digest round-trip: got %q, want %q", fmtCfg.Digest, seeds[0].digest)
	}
}
