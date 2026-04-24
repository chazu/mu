package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chau/mu/internal/cas/oci"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"oras.land/oras-go/v2/errdef"
)

// fakeRegistry is a minimal in-memory oci.Registry for testing the
// pure-data plugin push helper without spinning up a registry.
type fakeRegistry struct {
	mu    sync.Mutex
	blobs map[godigest.Digest][]byte
	tags  map[string]ocispec.Descriptor
}

func newFakeRegistry() *fakeRegistry {
	return &fakeRegistry{
		blobs: map[godigest.Digest][]byte{},
		tags:  map[string]ocispec.Descriptor{},
	}
}

func (f *fakeRegistry) Push(_ context.Context, d ocispec.Descriptor, r io.Reader) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.blobs[d.Digest]; ok {
		return errdef.ErrAlreadyExists
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	f.blobs[d.Digest] = b
	return nil
}

func (f *fakeRegistry) Fetch(_ context.Context, d ocispec.Descriptor) (io.ReadCloser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, ok := f.blobs[d.Digest]
	if !ok {
		return nil, errdef.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func (f *fakeRegistry) Exists(_ context.Context, d ocispec.Descriptor) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.blobs[d.Digest]
	return ok, nil
}

func (f *fakeRegistry) Delete(_ context.Context, _ ocispec.Descriptor) error { return nil }

func (f *fakeRegistry) Tag(_ context.Context, d ocispec.Descriptor, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.blobs[d.Digest]; !ok {
		return errdef.ErrNotFound
	}
	f.tags[ref] = d
	return nil
}

func (f *fakeRegistry) Resolve(_ context.Context, ref string) (ocispec.Descriptor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	d, ok := f.tags[ref]
	if !ok {
		return ocispec.Descriptor{}, errdef.ErrNotFound
	}
	return d, nil
}

func (f *fakeRegistry) Tags(_ context.Context, last string, fn func([]string) error) error {
	f.mu.Lock()
	out := []string{}
	for t := range f.tags {
		if last == "" || t > last {
			out = append(out, t)
		}
	}
	f.mu.Unlock()
	if len(out) == 0 {
		return nil
	}
	return fn(out)
}

// Compile-time check.
var _ oci.Registry = (*fakeRegistry)(nil)

func TestPushPluginToRegistry(t *testing.T) {
	ctx := context.Background()
	pluginRepo := newFakeRegistry()
	indexRepo := newFakeRegistry()

	cfg := oci.PluginConfig{
		Name:       "fmt",
		Entrypoint: "fmt.bb",
		Toolchain:  "bb",
		Files:      []string{"fmt.bb"},
		Digest:     "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
	}
	files := map[string][]byte{"fmt.bb": []byte("#!/usr/bin/env bb\n")}

	if err := pushPluginToRegistry(ctx, pluginRepo, indexRepo, cfg, files); err != nil {
		t.Fatalf("pushPluginToRegistry: %v", err)
	}

	tag := oci.PluginTag(cfg.Digest)
	got, err := oci.FetchPluginConfig(ctx, pluginRepo, tag)
	if err != nil {
		t.Fatalf("FetchPluginConfig: %v", err)
	}
	if got.Name != "fmt" {
		t.Fatalf("config name: %q", got.Name)
	}

	idx, err := oci.FetchPluginIndex(ctx, indexRepo)
	if err != nil {
		t.Fatalf("FetchPluginIndex: %v", err)
	}
	if len(idx.Plugins) != 1 || idx.Plugins[0] != "fmt" {
		t.Fatalf("index: %+v", idx)
	}
}

func TestPushPluginToRegistryIdempotent(t *testing.T) {
	ctx := context.Background()
	pluginRepo := newFakeRegistry()
	indexRepo := newFakeRegistry()

	cfg := oci.PluginConfig{
		Name: "x", Entrypoint: "x.bb", Files: []string{"x.bb"},
		Digest: "sha256:" + repeatByte('a', 64),
	}
	files := map[string][]byte{"x.bb": []byte("a")}

	for i := 0; i < 3; i++ {
		if err := pushPluginToRegistry(ctx, pluginRepo, indexRepo, cfg, files); err != nil {
			t.Fatalf("push iter %d: %v", i, err)
		}
	}

	idx, err := oci.FetchPluginIndex(ctx, indexRepo)
	if err != nil {
		t.Fatalf("FetchPluginIndex: %v", err)
	}
	if len(idx.Plugins) != 1 {
		t.Fatalf("expected 1 entry after 3 pushes, got %d: %v", len(idx.Plugins), idx.Plugins)
	}
}

func repeatByte(b byte, n int) string {
	out := make([]byte, n)
	for i := range out {
		out[i] = b
	}
	return string(out)
}

// Smoke check that the dispatcher routes "push" correctly.
func TestPluginDispatchIncludesPush(t *testing.T) {
	// runPluginPush with no args (nil) should return exitUsage.
	if got := runPluginPush(nil); got != exitUsage {
		t.Fatalf("runPluginPush(nil) = %d, want %d", got, exitUsage)
	}
}

func TestCollectPluginFilesUsesEntrypointForSingleFile(t *testing.T) {
	// Stage a fake ~/.mu/plugins/fmt/plugin-deadbeef.bb under a temp HOME.
	tmp := t.TempDir()
	t.Setenv("HOME", tmp)
	dir := filepath.Join(tmp, ".mu", "plugins", "fmt")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin-deadbeef.bb"), []byte("body"), 0o644); err != nil {
		t.Fatal(err)
	}

	files, isBundle, err := collectPluginFiles("fmt", "format.bb")
	if err != nil {
		t.Fatalf("collectPluginFiles: %v", err)
	}
	if isBundle {
		t.Fatal("expected single-file, got bundle")
	}
	if _, ok := files["format.bb"]; !ok {
		t.Fatalf("expected key %q, got keys %v", "format.bb", keysOf(files))
	}
	if _, ok := files["fmt.bb"]; ok {
		t.Fatal("did not expect mangled key fmt.bb")
	}
}

func keysOf(m map[string][]byte) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
