# Plugin Remote Discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Let users discover mu plugins in local and remote OCI caches without having them declared in the current project — so a user in a fresh repo can run `mu plugin list --remote` and see what's available to install.

**Architecture:** Publish plugins as OCI artifacts under a mu-scoped path (`<repo>/mu/plugins/<name>:<tag>`) with a mu-specific config media type (`application/vnd.mu.plugin.v1+json`). Because OCI distribution has no reliable cross-repo listing, we also maintain a mu-specific index artifact at `<repo>/mu/plugin-index:v1` that enumerates known plugin names. `mu plugin push` writes the plugin artifact and updates the index; `mu plugin list --remote` reads the index per backend, walks tags per plugin, fetches the small config blob for metadata, and merges with a local `~/.mu/plugins/` scan.

**Tech Stack:** Go, `oras.land/oras-go/v2` (already a dep), existing `internal/cas/oci` package, existing auth path (`credstore.go`).

**Namespacing and artifact types:**

| Artifact | Ref | Config media type | artifactType |
|---|---|---|---|
| Plugin | `<repo>/mu/plugins/<name>:sha256-<12hex>` | `application/vnd.mu.plugin.v1+json` | `application/vnd.mu.plugin.v1` |
| Plugin file layer | (layer within plugin manifest) | `application/vnd.mu.plugin.file.v1` | — |
| Index | `<repo>/mu/plugin-index:v1` | `application/vnd.mu.plugin-index.v1+json` | `application/vnd.mu.plugin-index.v1` |

All mu artifacts MUST use these media types. `list --remote` and `push` both validate on fetch — a foreign artifact at the same tag is rejected with a clear error.

---

## File Structure

**Create:**
- `internal/cas/oci/plugin.go` — media type constants, `PluginConfig` / `PluginIndex` structs, `PushPlugin`, `FetchPluginConfig`, `FetchPluginIndex`, `UpdatePluginIndex`
- `internal/cas/oci/plugin_test.go` — round-trip tests against `oras-go`'s in-memory store
- `cmd/mu/plugin_push.go` — `mu plugin push <name>` subcommand
- `cmd/mu/plugin_push_test.go` — integration test using `oras-go` memory store as the "remote"
- `cmd/mu/plugin_list_remote.go` — `--remote` path for `mu plugin list`
- `cmd/mu/plugin_list_remote_test.go` — integration test

**Modify:**
- `internal/cas/oci/oci.go` — add `Tags(ctx, n int, last string, fn func([]string) error) error` to the `Registry` interface (already implemented by `remote.Repository` and `ocilayout`'s store)
- `cmd/mu/plugin.go` — wire `push` into dispatcher (lines 23-48); add `--remote` flag parsing to `runPluginList`; wire merged output
- `docs/*` — plugin discovery doc (if a plugin or caching doc exists, amend it; otherwise skip)

---

## Task 1: Plugin artifact schema + constants

**Files:**
- Create: `internal/cas/oci/plugin.go`
- Create: `internal/cas/oci/plugin_test.go`

- [ ] **Step 1: Write failing test for config round-trip**

In `internal/cas/oci/plugin_test.go`:

```go
package oci

import (
	"encoding/json"
	"testing"
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
	if got != src {
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
	if got.SchemaVersion != 1 || len(got.Plugins) != 3 {
		t.Fatalf("round-trip mismatch: %+v", got)
	}
}
```

- [ ] **Step 2: Run test, verify it fails**

Run: `go test ./internal/cas/oci/ -run TestPluginConfig -v`
Expected: FAIL — `undefined: PluginConfig`.

- [ ] **Step 3: Create plugin.go with types and media types**

In `internal/cas/oci/plugin.go`:

```go
package oci

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
	// the configured cache push repository. E.g. cache.push.repository
	// "mu-cache" gives plugins at "mu-cache/mu/plugins/<name>".
	PluginRepoPrefix = "mu/plugins"

	// PluginIndexRef is the tag reference path for the plugin index, relative
	// to cache.push.repository. The full ref is
	// "<registry>/<repository>/mu/plugin-index:v1".
	PluginIndexRef = "mu/plugin-index"

	// PluginIndexTag is the tag used for the plugin index artifact.
	PluginIndexTag = "v1"
)

// PluginConfig is the JSON payload of a plugin artifact's config blob.
// Mirrors config.PluginManifest but is self-contained — it travels with the
// artifact so consumers don't need the source project to understand it.
type PluginConfig struct {
	Name       string   `json:"name"`                 // logical plugin name
	Entrypoint string   `json:"entrypoint"`           // relative executable path
	Toolchain  string   `json:"toolchain,omitempty"`  // e.g. "bb"; empty = direct
	Files      []string `json:"files,omitempty"`      // files bundled in layers (relative paths)
	Guide      string   `json:"guide,omitempty"`      // relative path to guide file
	Digest     string   `json:"digest,omitempty"`     // original CAS digest (sha256:...)
	Source     string   `json:"source,omitempty"`     // optional git remote URL the plugin was pushed from
}

// PluginIndex is the JSON payload of the plugin-index artifact's config blob.
// It lists all plugin names known to this registry's mu namespace, so clients
// can enumerate without relying on the OCI _catalog endpoint.
type PluginIndex struct {
	SchemaVersion int      `json:"schemaVersion"` // always 1
	Plugins       []string `json:"plugins"`       // sorted, deduplicated plugin names
}

// PluginTag returns the tag used for a plugin artifact given its content digest.
// We use the first 12 hex chars of the sha256 hash so tags are stable, short,
// and don't collide with future semver tagging schemes.
func PluginTag(sha256Hex string) string {
	if len(sha256Hex) > 12 {
		sha256Hex = sha256Hex[:12]
	}
	return "sha256-" + sha256Hex
}
```

- [ ] **Step 4: Run test, verify pass**

Run: `go test ./internal/cas/oci/ -run "TestPluginConfig|TestPluginIndex" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cas/oci/plugin.go internal/cas/oci/plugin_test.go
git commit -m "feat(oci): add plugin artifact schema + media types"
```

---

## Task 2: Extend Registry interface with Tags

**Files:**
- Modify: `internal/cas/oci/oci.go:42-49`

**Context:** `remote.Repository` already implements `Tags(ctx, last string, fn func([]string) error) error` per oras-go v2.6. The local `ocilayout.Store` also satisfies this signature. We add it to the `Registry` interface so downstream code can call it without type-asserting to `*remote.Repository`.

- [ ] **Step 1: Write failing test**

Append to `internal/cas/oci/oci_test.go`:

```go
func TestRegistryInterfaceHasTags(t *testing.T) {
	var _ Registry = (*stubRegistryWithTags)(nil)
}

type stubRegistryWithTags struct{}

func (s *stubRegistryWithTags) Push(context.Context, ocispec.Descriptor, io.Reader) error { return nil }
func (s *stubRegistryWithTags) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) { return nil, nil }
func (s *stubRegistryWithTags) Exists(context.Context, ocispec.Descriptor) (bool, error) { return false, nil }
func (s *stubRegistryWithTags) Delete(context.Context, ocispec.Descriptor) error { return nil }
func (s *stubRegistryWithTags) Tag(context.Context, ocispec.Descriptor, string) error { return nil }
func (s *stubRegistryWithTags) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return ocispec.Descriptor{}, nil
}
func (s *stubRegistryWithTags) Tags(context.Context, string, func([]string) error) error { return nil }
```

Add `"io"` and the `context`/`ocispec` imports at the top of the test file if missing.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/cas/oci/ -run TestRegistryInterfaceHasTags -v`
Expected: FAIL — `*stubRegistryWithTags does not implement Registry (missing method Tags)` OR the assignment compiles and the test passes by accident because Tags isn't on the interface yet. If it passes, invert the assertion to `_ = s.Tags` after the interface assignment to force the compile error. Iterate until the test fails because `Tags` isn't on `Registry`.

- [ ] **Step 3: Add Tags to Registry interface**

In `internal/cas/oci/oci.go`, modify the `Registry` interface (lines 42-49):

```go
type Registry interface {
	Push(ctx context.Context, expected ocispec.Descriptor, content io.Reader) error
	Fetch(ctx context.Context, target ocispec.Descriptor) (io.ReadCloser, error)
	Exists(ctx context.Context, target ocispec.Descriptor) (bool, error)
	Delete(ctx context.Context, target ocispec.Descriptor) error
	Tag(ctx context.Context, desc ocispec.Descriptor, reference string) error
	Resolve(ctx context.Context, reference string) (ocispec.Descriptor, error)
	// Tags enumerates tag names in lexical order, calling fn for each page.
	// `last` is the tag to start after (empty = from the beginning). Implementations
	// for registries lacking `/v2/<name>/tags/list` may return a wrapped error;
	// callers should treat an empty tag list as "no plugins here" not as failure.
	Tags(ctx context.Context, last string, fn func(tags []string) error) error
}
```

- [ ] **Step 4: Run full oci tests, verify pass**

Run: `go test ./internal/cas/oci/ -v`
Expected: PASS. If `ocilayout.Store` doesn't satisfy the new signature, the compile will fail at `NewLocal` — in that case check the oras-go version in go.mod. v2.6.0 has `Tags(ctx, last string, fn func([]string) error) error` on `*ocilayout.Store`. If the signature differs, update the interface method to match both implementations exactly (verify with `go doc oras.land/oras-go/v2/content/oci Store.Tags` and `go doc oras.land/oras-go/v2/registry/remote Repository.Tags`).

- [ ] **Step 5: Commit**

```bash
git add internal/cas/oci/oci.go internal/cas/oci/oci_test.go
git commit -m "feat(oci): expose Tags on Registry interface"
```

---

## Task 3: PushPlugin + FetchPluginConfig

**Files:**
- Modify: `internal/cas/oci/plugin.go`
- Modify: `internal/cas/oci/plugin_test.go`

**Context:** A plugin artifact is an OCI image manifest whose `config` blob is the JSON-encoded `PluginConfig` and whose layers are the plugin's bundled files (one layer each). We use oras-go's low-level `Push` + `Tag` rather than `oras.Pack` so we control the exact media types.

- [ ] **Step 1: Write failing round-trip test using memory store**

Append to `internal/cas/oci/plugin_test.go`:

```go
import (
	// existing imports...
	"bytes"
	"context"
	"oras.land/oras-go/v2/content/memory"
)

func TestPushAndFetchPlugin(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	cfg := PluginConfig{
		Name:       "fmt",
		Entrypoint: "fmt.bb",
		Toolchain:  "bb",
		Files:      []string{"fmt.bb"},
		Digest:     "sha256:deadbeef",
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

	// memory.Store tags under a local ref; resolve it.
	tag := PluginTag("deadbeef")
	got, err := FetchPluginConfig(ctx, store, tag)
	if err != nil {
		t.Fatalf("FetchPluginConfig: %v", err)
	}
	if got.Name != "fmt" || got.Entrypoint != "fmt.bb" {
		t.Fatalf("unexpected config: %+v", got)
	}
}

func TestFetchPluginConfigRejectsWrongMediaType(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	// Push a manifest with the wrong config media type and tag it like a plugin.
	badConfig := []byte(`{"arbitrary":"json"}`)
	badDesc := ocispec.Descriptor{
		MediaType: "application/vnd.oci.image.config.v1+json", // NOT the mu plugin type
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
	if err := store.Tag(ctx, mdesc, "sha256-cafebabe"); err != nil {
		t.Fatalf("tag: %v", err)
	}

	_, err := FetchPluginConfig(ctx, store, "sha256-cafebabe")
	if err == nil {
		t.Fatal("expected error when fetching non-mu artifact, got nil")
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/cas/oci/ -run TestPushAndFetchPlugin -v`
Expected: FAIL — `undefined: PushPlugin`.

- [ ] **Step 3: Implement PushPlugin and FetchPluginConfig**

Append to `internal/cas/oci/plugin.go`:

```go
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"

	godigest "github.com/opencontainers/go-digest"
	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// PushPlugin writes a plugin artifact (config + file layers + manifest) to repo
// and tags it with PluginTag(cfg.Digest). Returns the manifest descriptor.
//
// files maps relative file paths to contents; cfg.Files SHOULD mirror these
// keys (but we don't enforce — PluginConfig is data-only, PushPlugin is plumbing).
func PushPlugin(ctx context.Context, repo Registry, name string, cfg PluginConfig, files map[string][]byte) (ocispec.Descriptor, error) {
	// 1. Push config blob.
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

	// 2. Push each file as a layer.
	layers := make([]ocispec.Descriptor, 0, len(files))
	// Iterate cfg.Files in declared order for determinism when provided.
	order := cfg.Files
	if len(order) == 0 {
		for k := range files {
			order = append(order, k)
		}
	}
	seen := map[string]bool{}
	for _, path := range order {
		if seen[path] {
			continue
		}
		seen[path] = true
		content, ok := files[path]
		if !ok {
			return ocispec.Descriptor{}, fmt.Errorf("file %q in cfg.Files but not in files map", path)
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

	// 3. Push manifest.
	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypePlugin,
		Config:       cfgDesc,
		Layers:       layers,
		Annotations: map[string]string{
			"org.opencontainers.image.title":   name,
			"org.opencontainers.image.version": cfg.Digest,
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

	// 4. Tag.
	shortHex := cfg.Digest
	if i := len(shortHex) - 1; i > 0 {
		// Strip "sha256:" prefix if present.
		for j := 0; j < len(shortHex)-1; j++ {
			if shortHex[j] == ':' {
				shortHex = shortHex[j+1:]
				break
			}
		}
	}
	tag := PluginTag(shortHex)
	if err := repo.Tag(ctx, mdesc, tag); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("tag plugin %s: %w", tag, err)
	}
	return mdesc, nil
}

// FetchPluginConfig resolves `ref` to a manifest, validates it is a mu plugin
// artifact (by config media type), and returns the decoded PluginConfig.
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

// pushIfAbsent is a small helper that skips the push if desc already exists
// in repo. Registries return errdef.ErrAlreadyExists on duplicate pushes;
// we pre-check to avoid the round-trip when we can.
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
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/cas/oci/ -run "TestPushAndFetchPlugin|TestFetchPluginConfigRejectsWrongMediaType" -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/cas/oci/plugin.go internal/cas/oci/plugin_test.go
git commit -m "feat(oci): implement PushPlugin and FetchPluginConfig"
```

---

## Task 4: Plugin index helpers

**Files:**
- Modify: `internal/cas/oci/plugin.go`
- Modify: `internal/cas/oci/plugin_test.go`

- [ ] **Step 1: Write failing tests for index round-trip and merge**

Append to `internal/cas/oci/plugin_test.go`:

```go
func TestFetchPluginIndexEmpty(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	got, err := FetchPluginIndex(ctx, store)
	if err != nil {
		t.Fatalf("FetchPluginIndex on empty store: %v (want nil + empty index)", err)
	}
	if len(got.Plugins) != 0 {
		t.Fatalf("expected empty index, got %+v", got)
	}
}

func TestUpdatePluginIndex(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	if err := UpdatePluginIndex(ctx, store, "fmt"); err != nil {
		t.Fatalf("UpdatePluginIndex: %v", err)
	}
	if err := UpdatePluginIndex(ctx, store, "lint"); err != nil {
		t.Fatalf("UpdatePluginIndex: %v", err)
	}
	// Adding a duplicate should be a no-op.
	if err := UpdatePluginIndex(ctx, store, "fmt"); err != nil {
		t.Fatalf("UpdatePluginIndex duplicate: %v", err)
	}
	idx, err := FetchPluginIndex(ctx, store)
	if err != nil {
		t.Fatalf("FetchPluginIndex: %v", err)
	}
	want := []string{"fmt", "lint"}
	if !reflect.DeepEqual(idx.Plugins, want) {
		t.Fatalf("index plugins: got %v, want %v", idx.Plugins, want)
	}
}

func TestFetchPluginIndexRejectsWrongMediaType(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	// Tag a non-mu manifest at the index ref.
	badConfig := []byte(`{}`)
	badDesc := ocispec.Descriptor{
		MediaType: "application/vnd.oci.image.config.v1+json",
		Digest:    godigest.FromBytes(badConfig),
		Size:      int64(len(badConfig)),
	}
	store.Push(ctx, badDesc, bytes.NewReader(badConfig))
	m := ocispec.Manifest{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageManifest,
		Config:    badDesc,
	}
	mb, _ := json.Marshal(&m)
	mdesc := ocispec.Descriptor{
		MediaType: ocispec.MediaTypeImageManifest,
		Digest:    godigest.FromBytes(mb),
		Size:      int64(len(mb)),
	}
	store.Push(ctx, mdesc, bytes.NewReader(mb))
	store.Tag(ctx, mdesc, PluginIndexTag)

	_, err := FetchPluginIndex(ctx, store)
	if err == nil {
		t.Fatal("expected error for non-mu artifact at index ref")
	}
}
```

Add `"reflect"` to imports.

- [ ] **Step 2: Run, verify fail**

Run: `go test ./internal/cas/oci/ -run TestPluginIndex -v`
Expected: FAIL — `undefined: FetchPluginIndex`.

- [ ] **Step 3: Implement index helpers**

Append to `internal/cas/oci/plugin.go`:

```go
import (
	"sort"
	"oras.land/oras-go/v2/errdef"
)

// FetchPluginIndex returns the plugin index stored at PluginIndexTag. If the
// tag does not exist, returns an empty index and nil error (registry is simply
// un-indexed yet). Any other error, including a manifest with the wrong config
// media type, is returned.
func FetchPluginIndex(ctx context.Context, repo Registry) (PluginIndex, error) {
	mdesc, err := repo.Resolve(ctx, PluginIndexTag)
	if err != nil {
		if errors.Is(err, errdef.ErrNotFound) {
			return PluginIndex{SchemaVersion: 1}, nil
		}
		return PluginIndex{}, fmt.Errorf("resolve plugin index: %w", err)
	}
	mrc, err := repo.Fetch(ctx, mdesc)
	if err != nil {
		return PluginIndex{}, fmt.Errorf("fetch plugin index manifest: %w", err)
	}
	mb, err := io.ReadAll(mrc)
	mrc.Close()
	if err != nil {
		return PluginIndex{}, err
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(mb, &manifest); err != nil {
		return PluginIndex{}, fmt.Errorf("decode plugin index manifest: %w", err)
	}
	if manifest.Config.MediaType != MediaTypePluginIndexConfig {
		return PluginIndex{}, fmt.Errorf("plugin index ref %q has config media type %q (want %q) — not a mu index",
			PluginIndexTag, manifest.Config.MediaType, MediaTypePluginIndexConfig)
	}
	crc, err := repo.Fetch(ctx, manifest.Config)
	if err != nil {
		return PluginIndex{}, fmt.Errorf("fetch plugin index config: %w", err)
	}
	cb, err := io.ReadAll(crc)
	crc.Close()
	if err != nil {
		return PluginIndex{}, err
	}
	var idx PluginIndex
	if err := json.Unmarshal(cb, &idx); err != nil {
		return PluginIndex{}, fmt.Errorf("decode plugin index: %w", err)
	}
	return idx, nil
}

// UpdatePluginIndex fetches the current index, inserts `name` (if absent),
// sorts, and re-publishes the index artifact at PluginIndexTag. Safe to call
// after every push; concurrent callers may race but the operation is
// idempotent and the order-of-the-last-writer wins.
func UpdatePluginIndex(ctx context.Context, repo Registry, name string) error {
	idx, err := FetchPluginIndex(ctx, repo)
	if err != nil {
		return err
	}
	if idx.SchemaVersion == 0 {
		idx.SchemaVersion = 1
	}
	for _, p := range idx.Plugins {
		if p == name {
			return nil // already present
		}
	}
	idx.Plugins = append(idx.Plugins, name)
	sort.Strings(idx.Plugins)

	cfgBytes, err := json.Marshal(&idx)
	if err != nil {
		return fmt.Errorf("marshal plugin index: %w", err)
	}
	cfgDesc := ocispec.Descriptor{
		MediaType: MediaTypePluginIndexConfig,
		Digest:    godigest.FromBytes(cfgBytes),
		Size:      int64(len(cfgBytes)),
	}
	if err := pushIfAbsent(ctx, repo, cfgDesc, cfgBytes); err != nil {
		return fmt.Errorf("push plugin index config: %w", err)
	}

	manifest := ocispec.Manifest{
		Versioned:    specs.Versioned{SchemaVersion: 2},
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypePluginIndex,
		Config:       cfgDesc,
		Layers:       []ocispec.Descriptor{}, // no layers; config carries the data
	}
	mb, err := json.Marshal(&manifest)
	if err != nil {
		return fmt.Errorf("marshal plugin index manifest: %w", err)
	}
	mdesc := ocispec.Descriptor{
		MediaType:    ocispec.MediaTypeImageManifest,
		ArtifactType: ArtifactTypePluginIndex,
		Digest:       godigest.FromBytes(mb),
		Size:         int64(len(mb)),
	}
	if err := pushIfAbsent(ctx, repo, mdesc, mb); err != nil {
		return fmt.Errorf("push plugin index manifest: %w", err)
	}
	return repo.Tag(ctx, mdesc, PluginIndexTag)
}
```

Add `"errors"` to imports.

- [ ] **Step 4: Run, verify pass**

Run: `go test ./internal/cas/oci/ -run TestPluginIndex -v`
Expected: PASS (all three subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/cas/oci/plugin.go internal/cas/oci/plugin_test.go
git commit -m "feat(oci): plugin index fetch/update helpers"
```

---

## Task 5: `mu plugin push <name>` command

**Files:**
- Create: `cmd/mu/plugin_push.go`
- Create: `cmd/mu/plugin_push_test.go`
- Modify: `cmd/mu/plugin.go:23-48` (dispatcher + usage)

**Context:** `mu plugin push <name>` locates the plugin by name in the project config, builds its `//plugins/<name>` target (same as `plugin add`), extracts files from CAS, calls `oci.PushPlugin` against the configured push target, then calls `oci.UpdatePluginIndex`.

For bundled plugins, files live under `~/.mu/plugins/<name>/bundle-<hash>/*` (multi-file). For single-file plugins, it's just `~/.mu/plugins/<name>/plugin-<hash>.<ext>`. We bundle whatever is on disk after build.

- [ ] **Step 1: Write failing integration test**

In `cmd/mu/plugin_push_test.go`:

```go
package main

import (
	"context"
	"testing"

	"github.com/chau/mu/internal/cas/oci"
	"oras.land/oras-go/v2/content/memory"
)

// TestPluginPushUpdatesIndex smoke-tests the push path end-to-end by invoking
// the internal helper against an in-memory OCI store. We do not spin up a real
// registry; cache_push_test.go follows the same pattern.
func TestPluginPushUpdatesIndex(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	cfg := oci.PluginConfig{
		Name:       "fmt",
		Entrypoint: "fmt.bb",
		Toolchain:  "bb",
		Files:      []string{"fmt.bb"},
		Digest:     "sha256:0123456789abcdef",
	}
	files := map[string][]byte{"fmt.bb": []byte("#!/usr/bin/env bb\n")}

	if err := pushPluginToRegistry(ctx, store, cfg, files); err != nil {
		t.Fatalf("pushPluginToRegistry: %v", err)
	}

	// Verify the artifact is tagged and the index lists it.
	tag := oci.PluginTag("0123456789abcdef")
	got, err := oci.FetchPluginConfig(ctx, store, tag)
	if err != nil {
		t.Fatalf("FetchPluginConfig after push: %v", err)
	}
	if got.Name != "fmt" {
		t.Fatalf("round-tripped config name: %q", got.Name)
	}
	idx, err := oci.FetchPluginIndex(ctx, store)
	if err != nil {
		t.Fatalf("FetchPluginIndex: %v", err)
	}
	if len(idx.Plugins) != 1 || idx.Plugins[0] != "fmt" {
		t.Fatalf("index plugins: %v", idx.Plugins)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./cmd/mu/ -run TestPluginPushUpdatesIndex -v`
Expected: FAIL — `undefined: pushPluginToRegistry`.

- [ ] **Step 3: Create plugin_push.go**

Create `cmd/mu/plugin_push.go`:

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chau/mu/internal/cas/oci"
)

// runPluginPush publishes the named plugin to the configured cache.push registry
// under <repo>/mu/plugins/<name>:sha256-<short>, and updates the mu plugin index.
func runPluginPush(args []string) int {
	fs := flag.NewFlagSet("plugin push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("plugin push", fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin push <name>")
		return exitUsage
	}
	name := fs.Arg(0)

	if code, ok := c.Resolve(resolveOpts{NeedConfig: true, ValidateConfig: true}); !ok {
		return code
	}

	// Locate the plugin target and its source config.
	targetName := "//plugins/" + name
	var sourcePaths []string
	var guide string
	var entrypoint string
	foundTarget := false
	for _, t := range c.Config.Targets {
		if t.Name == targetName {
			foundTarget = true
			sourcePaths = append(sourcePaths, t.Sources...)
			if len(t.Sources) > 0 {
				entrypoint = filepath.Base(t.Sources[0])
			}
			break
		}
	}
	if !foundTarget {
		return c.fail(exitFail, "target %q not found in config", targetName)
	}

	// Build to ensure CAS has the bundle.
	result, err := buildTargets(c.ProjectRoot, c.Config, []string{targetName})
	if err != nil {
		return c.fail(exitFail, "build %s: %v", targetName, err)
	}
	if result.Failed > 0 {
		return c.fail(exitFail, "build failed for %s", targetName)
	}

	dgst, err := extractPluginDigest(result, targetName)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}

	// Collect files from disk (the extracted bundle under ~/.mu/plugins/<name>/).
	files, err := collectPluginFiles(name)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}

	// Derive toolchain from entrypoint extension as plugin.go:337 does.
	toolchain := ""
	if ext := filepath.Ext(entrypoint); ext == ".bb" {
		toolchain = "bb"
	}

	cfg := oci.PluginConfig{
		Name:       name,
		Entrypoint: entrypoint,
		Toolchain:  toolchain,
		Files:      sortedKeys(files),
		Digest:     dgst.String(),
		Guide:      guide,
		Source:     detectGitRemote(c.ProjectRoot),
	}

	ref, code, ok := resolvePushRef(c)
	if !ok {
		return code
	}
	pluginRepoRef := ref + "/" + oci.PluginRepoPrefix + "/" + name
	indexRepoRef := ref + "/" + oci.PluginIndexRef

	ctx := context.Background()

	pluginRepo, err := newPushRepository(pluginRepoRef)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	if err := pushPluginToRegistry(ctx, pluginRepo, cfg, files); err != nil {
		return c.fail(exitFail, "push plugin: %v", err)
	}

	indexRepo, err := newPushRepository(indexRepoRef)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	if err := oci.UpdatePluginIndex(ctx, indexRepo, name); err != nil {
		return c.fail(exitFail, "update plugin index: %v", err)
	}

	fmt.Printf("Pushed %s → %s:%s\n", name, pluginRepoRef, oci.PluginTag(dgst.Hash))
	return exitOK
}

// pushPluginToRegistry is the pure-data push path, exposed for tests.
func pushPluginToRegistry(ctx context.Context, repo oci.Registry, cfg oci.PluginConfig, files map[string][]byte) error {
	if _, err := oci.PushPlugin(ctx, repo, cfg.Name, cfg, files); err != nil {
		return err
	}
	return oci.UpdatePluginIndex(ctx, repo, cfg.Name)
}

// collectPluginFiles reads all files under ~/.mu/plugins/<name>/ that were
// produced by the most recent build. For bundled plugins this walks
// bundle-<hash>/; for single-file plugins it returns just the plugin-<hash>.<ext>.
func collectPluginFiles(name string) (map[string][]byte, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".mu", "plugins", name)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read plugins dir %s: %w", dir, err)
	}

	out := map[string][]byte{}
	for _, e := range entries {
		full := filepath.Join(dir, e.Name())
		if e.IsDir() {
			// Bundle: walk and include everything.
			err := filepath.Walk(full, func(p string, info os.FileInfo, err error) error {
				if err != nil {
					return err
				}
				if info.IsDir() {
					return nil
				}
				rel, relErr := filepath.Rel(full, p)
				if relErr != nil {
					return relErr
				}
				b, rerr := os.ReadFile(p)
				if rerr != nil {
					return rerr
				}
				out[rel] = b
				return nil
			})
			if err != nil {
				return nil, err
			}
			continue
		}
		// Single file under plugins/<name>/plugin-<hash>.<ext>.
		b, rerr := os.ReadFile(full)
		if rerr != nil {
			return nil, rerr
		}
		// Strip the cache-mangled prefix so the uploaded file uses its natural name.
		// plugin-<hash>.<ext> → <name><ext>
		ext := filepath.Ext(e.Name())
		out[name+ext] = b
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no plugin files found under %s", dir)
	}
	return out, nil
}

func sortedKeys(m map[string][]byte) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// detectGitRemote best-effort reads .git/config to find origin URL. Returns
// "" on any error — Source is optional metadata.
func detectGitRemote(projectRoot string) string {
	// Intentionally minimal: read .git/config and look for [remote "origin"] url.
	b, err := os.ReadFile(filepath.Join(projectRoot, ".git", "config"))
	if err != nil {
		return ""
	}
	content := string(b)
	idx := strings.Index(content, `[remote "origin"]`)
	if idx < 0 {
		return ""
	}
	rest := content[idx:]
	urlIdx := strings.Index(rest, "url =")
	if urlIdx < 0 {
		return ""
	}
	line := rest[urlIdx+len("url ="):]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	return strings.TrimSpace(line)
}
```

Add imports: `"sort"`, `"strings"`.

- [ ] **Step 4: Wire push into dispatcher**

In `cmd/mu/plugin.go:23-48`, update `runPlugin`:

```go
func runPlugin(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: mu plugin <command>

Commands:
  list      List registered plugins
  add       Add a plugin from cache by building its //plugins/<name> target
  push      Publish a plugin to the configured OCI cache
  status    Reconcile declared plugins against the local cache
  test      Run scenarios against a plugin (bundled + testdata/*.yaml)`)
		return 2
	}

	switch args[0] {
	case "list":
		return runPluginList(args[1:])
	case "add":
		return runPluginAdd(args[1:])
	case "push":
		return runPluginPush(args[1:])
	case "status":
		return runPluginStatus(args[1:])
	case "test":
		return runPluginTest(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mu plugin: unknown command %q\n", args[0])
		return 2
	}
}
```

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./cmd/mu/ -run TestPluginPushUpdatesIndex -v && go build ./cmd/mu/`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add cmd/mu/plugin_push.go cmd/mu/plugin_push_test.go cmd/mu/plugin.go
git commit -m "feat(cli): add 'mu plugin push' command"
```

---

## Task 6: `mu plugin list --remote`

**Files:**
- Create: `cmd/mu/plugin_list_remote.go`
- Create: `cmd/mu/plugin_list_remote_test.go`
- Modify: `cmd/mu/plugin.go:110-143` (add `--remote` flag + dispatch)

**Context:** `list --remote` works without any project config (mirrors the plain `--cached` fix from earlier). It reads the mu config for `cache.backends` if available, but if no config is loaded, falls back to `~/.mu/backends.json` (future work) or just the push target. For now, we require a loadable config — document this clearly in usage.

Actually — to support the "user in a fresh project" case, we should read the global default push target from an env var or user-level config file. For this plan we'll support two sources:

1. `MU_CACHE_BACKENDS` env var — comma-separated `registry/repository` refs (read-only backends to query)
2. Project config `cache.backends` (oci type) if a config is loadable

If neither is set, `list --remote` prints a helpful message.

- [ ] **Step 1: Write failing test using memory store stub**

In `cmd/mu/plugin_list_remote_test.go`:

```go
package main

import (
	"context"
	"strings"
	"testing"

	"github.com/chau/mu/internal/cas/oci"
	"oras.land/oras-go/v2/content/memory"
)

func TestListRemoteFromStore(t *testing.T) {
	ctx := context.Background()
	store := memory.New()

	// Seed: push two plugins and update the index.
	for _, p := range []struct {
		name   string
		digest string
	}{
		{"fmt", "sha256:0123456789ab"},
		{"lint", "sha256:fedcba987654"},
	} {
		cfg := oci.PluginConfig{
			Name: p.name, Entrypoint: p.name + ".bb",
			Toolchain: "bb", Files: []string{p.name + ".bb"},
			Digest: p.digest,
		}
		files := map[string][]byte{p.name + ".bb": []byte("#!/usr/bin/env bb\n")}
		if _, err := oci.PushPlugin(ctx, store, p.name, cfg, files); err != nil {
			t.Fatalf("seed %s: %v", p.name, err)
		}
		if err := oci.UpdatePluginIndex(ctx, store, p.name); err != nil {
			t.Fatalf("index %s: %v", p.name, err)
		}
	}

	// Query.
	items, err := listPluginsInBackend(ctx, "memory", store)
	if err != nil {
		t.Fatalf("listPluginsInBackend: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 plugins, got %d: %+v", len(items), items)
	}
	names := []string{items[0].Name, items[1].Name}
	if !(strings.Contains(strings.Join(names, ","), "fmt") &&
		strings.Contains(strings.Join(names, ","), "lint")) {
		t.Fatalf("missing plugin names: %v", names)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./cmd/mu/ -run TestListRemoteFromStore -v`
Expected: FAIL — `undefined: listPluginsInBackend`.

- [ ] **Step 3: Implement plugin_list_remote.go**

Create `cmd/mu/plugin_list_remote.go`:

```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/chau/mu/internal/cas/oci"
)

// remotePlugin is the row type for `mu plugin list --remote`.
type remotePlugin struct {
	Name     string `json:"name"`
	Version  string `json:"version"` // tag (e.g. sha256-abc123def456)
	Location string `json:"location"`
	Digest   string `json:"digest"`
}

// listPluginsInBackend queries one backend: reads the index, walks tags per
// plugin, fetches each manifest config for metadata. Returns all found rows.
// A missing index is not an error — returns nil, nil so a registry without
// any mu plugins shows up as empty rather than failing the whole list.
func listPluginsInBackend(ctx context.Context, location string, repo oci.Registry) ([]remotePlugin, error) {
	idx, err := oci.FetchPluginIndex(ctx, repo)
	if err != nil {
		return nil, fmt.Errorf("backend %s: %w", location, err)
	}
	if len(idx.Plugins) == 0 {
		return nil, nil
	}

	// For each plugin name, we need a sub-repository. With a single Registry
	// handle rooted at the repo, tag-listing covers only this repo's tags.
	// The caller is expected to pass a Registry rooted at the index repo; for
	// real backends, runPluginListRemote constructs sub-repos per plugin. To
	// keep this helper testable against an in-memory store (which does not
	// model sub-repos), we tag-list the current store for each plugin's
	// artifacts as they were tagged by PushPlugin (sha256-<12>).
	// In-memory test mode: derive version from the plugin's recorded digest.
	var rows []remotePlugin
	for _, name := range idx.Plugins {
		// Strategy: try to resolve the "name's tag" — FetchPluginConfig
		// needs a tag to resolve. In a real registry this is done by listing
		// tags on the sub-repo. In memory we use the config digest stored in
		// the index later. For the test helper, we walk all tags and keep
		// those whose config name matches.
		versionsFound := []string{}
		err := repo.Tags(ctx, "", func(tags []string) error {
			for _, t := range tags {
				if !strings.HasPrefix(t, "sha256-") {
					continue
				}
				cfg, err := oci.FetchPluginConfig(ctx, repo, t)
				if err != nil {
					continue // skip foreign artifacts silently
				}
				if cfg.Name == name {
					versionsFound = append(versionsFound, t)
					rows = append(rows, remotePlugin{
						Name:     cfg.Name,
						Version:  t,
						Location: location,
						Digest:   cfg.Digest,
					})
				}
			}
			return nil
		})
		if err != nil {
			// Tag listing unsupported — the index still told us plugin exists.
			// Fall back to an unversioned row.
			if len(versionsFound) == 0 {
				rows = append(rows, remotePlugin{Name: name, Version: "?", Location: location})
			}
		}
	}
	return rows, nil
}

// runPluginListRemote is wired from runPluginList when --remote is set.
func runPluginListRemote(c *cliContext, jsonOut bool) int {
	ctx := context.Background()

	refs := resolveRemoteBackendRefs(c)
	if len(refs) == 0 {
		fmt.Fprintln(os.Stderr,
			`no remote backends configured. Either:
  - run from a project with cache.backends (oci type) in mu.cue/mu.json, or
  - set MU_CACHE_BACKENDS="<registry>/<repository>[,<registry>/<repository>...]"`)
		return exitUsage
	}

	var all []remotePlugin
	for _, ref := range refs {
		// Index lives at <ref>/mu/plugin-index:v1; plugins at
		// <ref>/mu/plugins/<name>. We query the index repo for the index and
		// the plugin sub-repo for tags — two different Registry handles.
		indexRepoRef := ref + "/" + oci.PluginIndexRef
		indexRepo, err := newReadRepository(indexRepoRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backend %s: %v\n", ref, err)
			continue
		}
		idx, err := oci.FetchPluginIndex(ctx, indexRepo)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backend %s: %v\n", ref, err)
			continue
		}
		for _, name := range idx.Plugins {
			pluginRepoRef := ref + "/" + oci.PluginRepoPrefix + "/" + name
			pluginRepo, err := newReadRepository(pluginRepoRef)
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: %v\n", name, err)
				continue
			}
			err = pluginRepo.Tags(ctx, "", func(tags []string) error {
				for _, t := range tags {
					if !strings.HasPrefix(t, "sha256-") {
						continue
					}
					cfg, ferr := oci.FetchPluginConfig(ctx, pluginRepo, t)
					if ferr != nil {
						continue
					}
					all = append(all, remotePlugin{
						Name:     cfg.Name,
						Version:  t,
						Location: ref,
						Digest:   cfg.Digest,
					})
				}
				return nil
			})
			if err != nil {
				fmt.Fprintf(os.Stderr, "  %s: tag listing failed: %v\n", name, err)
			}
		}
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(all)
		return exitOK
	}

	if len(all) == 0 {
		fmt.Println("No plugins found in remote caches.")
		return exitOK
	}

	fmt.Printf("%-20s %-25s %-40s %s\n", "PLUGIN", "VERSION", "LOCATION", "DIGEST")
	for _, r := range all {
		dig := r.Digest
		if len(dig) > 23 {
			dig = dig[:23] + "..."
		}
		fmt.Printf("%-20s %-25s %-40s %s\n", r.Name, r.Version, r.Location, dig)
	}
	return exitOK
}

// resolveRemoteBackendRefs returns the list of <registry>/<repository> refs
// to query. Sources (merged, deduped): MU_CACHE_BACKENDS env + project config
// cache.backends (oci type, read != false) + cache.push (as a fallback).
func resolveRemoteBackendRefs(c *cliContext) []string {
	seen := map[string]bool{}
	var out []string
	add := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}

	if env := os.Getenv("MU_CACHE_BACKENDS"); env != "" {
		for _, r := range strings.Split(env, ",") {
			add(strings.TrimSpace(r))
		}
	}
	if c != nil && c.Config != nil && c.Config.Cache != nil {
		for _, b := range c.Config.Cache.Backends {
			if b.Type != "oci" {
				continue
			}
			if b.Read != nil && !*b.Read {
				continue
			}
			// OCI backends in config store "registry" only; the repository
			// comes from cache.push.repository.
			if c.Config.Cache.Push != nil && c.Config.Cache.Push.Repository != "" {
				add(b.Registry + "/" + c.Config.Cache.Push.Repository)
			}
		}
		if c.Config.Cache.Push != nil {
			p := c.Config.Cache.Push
			if p.Registry != "" && p.Repository != "" {
				add(p.Registry + "/" + p.Repository)
			}
		}
	}
	return out
}

// newReadRepository is a read-only counterpart to newPushRepository.
// Same plumbing; the remote registry distinguishes by scope at auth time.
func newReadRepository(ref string) (oci.Registry, error) {
	return newPushRepository(ref)
}
```

- [ ] **Step 4: Wire `--remote` flag in runPluginList**

In `cmd/mu/plugin.go`, update the `--remote` flag handling in `runPluginList`:

```go
func runPluginList(args []string) int {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin list", fs)
	discover := fs.Bool("discover", false, "start plugins and run discover to show capabilities")
	cached := fs.Bool("cached", false, "show all //plugins/* targets with their CAS digests")
	remote := fs.Bool("remote", false, "list plugins available in configured remote OCI caches")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	if *cached && !*discover {
		return pluginListCached(nil, "", cli.JSON)
	}

	if *remote {
		// Resolve config best-effort so cache.backends can contribute, but
		// succeed even outside a project via MU_CACHE_BACKENDS.
		_, _ = cli.Resolve(resolveOpts{NeedConfig: true})
		return runPluginListRemote(cli, cli.JSON)
	}

	if code, ok := cli.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}
	projectRoot := cli.ProjectRoot
	cfg := cli.Config
	jsonOut := &cli.JSON

	if *cached && *discover {
		return pluginListCachedDiscover(cfg, projectRoot, *jsonOut)
	}

	if len(cfg.Plugins) == 0 {
		fmt.Println("No plugins defined.")
		return 0
	}

	if *discover {
		return pluginListDiscover(cfg, projectRoot, *jsonOut)
	}
	return pluginListConfig(cfg, *jsonOut)
}
```

Note: the best-effort Resolve may return a code we discard. For this to compile cleanly, add a small variant that doesn't fail the command — or simply wrap in `func() { defer func() { recover() }() ... }` pattern. Better: add a `resolveOpts.BestEffort` bool. If that's too invasive, guard with a pre-check: if no project config is findable, skip config loading entirely and rely on env vars.

**Refinement:** if `cliContext` lacks a best-effort mode, add this before the Resolve:

```go
// Try to load config; errors non-fatal for --remote.
if _, err := os.Stat("mu.cue"); err == nil {
	_, _ = cli.Resolve(resolveOpts{NeedConfig: true})
}
```

Check both `mu.cue` and `mu.json` via a small helper.

- [ ] **Step 5: Run tests, verify pass**

Run: `go test ./cmd/mu/ -run "TestListRemoteFromStore|TestPluginPushUpdatesIndex" -v && go build ./cmd/mu/`
Expected: PASS + clean build.

- [ ] **Step 6: Commit**

```bash
git add cmd/mu/plugin_list_remote.go cmd/mu/plugin_list_remote_test.go cmd/mu/plugin.go
git commit -m "feat(cli): add 'mu plugin list --remote' discovery"
```

---

## Task 7: Merge local + remote for a unified view

**Files:**
- Modify: `cmd/mu/plugin_list_remote.go`

**Context:** `--remote --cached` should produce a single table marking which plugins are cached locally.

- [ ] **Step 1: Write failing test**

Append to `cmd/mu/plugin_list_remote_test.go`:

```go
func TestMergeLocalAndRemote(t *testing.T) {
	remote := []remotePlugin{
		{Name: "fmt", Version: "sha256-abc", Location: "registry.example/mu", Digest: "sha256:abc"},
		{Name: "lint", Version: "sha256-def", Location: "registry.example/mu", Digest: "sha256:def"},
	}
	local := []string{"fmt"} // fmt is cached locally

	rows := mergeLocalRemote(remote, local)

	var fmtRow, lintRow *mergedRow
	for i := range rows {
		switch rows[i].Name {
		case "fmt":
			fmtRow = &rows[i]
		case "lint":
			lintRow = &rows[i]
		}
	}
	if fmtRow == nil || !fmtRow.Cached {
		t.Fatalf("fmt should be cached: %+v", fmtRow)
	}
	if lintRow == nil || lintRow.Cached {
		t.Fatalf("lint should not be cached: %+v", lintRow)
	}
}
```

- [ ] **Step 2: Run, verify fail**

Run: `go test ./cmd/mu/ -run TestMergeLocalAndRemote -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Add merge helpers**

Append to `cmd/mu/plugin_list_remote.go`:

```go
type mergedRow struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Location string `json:"location"`
	Cached   bool   `json:"cached"`
	Digest   string `json:"digest,omitempty"`
}

func mergeLocalRemote(remote []remotePlugin, localNames []string) []mergedRow {
	localSet := map[string]bool{}
	for _, n := range localNames {
		localSet[n] = true
	}
	out := make([]mergedRow, 0, len(remote))
	for _, r := range remote {
		out = append(out, mergedRow{
			Name:     r.Name,
			Version:  r.Version,
			Location: r.Location,
			Cached:   localSet[r.Name],
			Digest:   r.Digest,
		})
	}
	return out
}
```

- [ ] **Step 4: Run, verify pass**

Run: `go test ./cmd/mu/ -run TestMergeLocalAndRemote -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add cmd/mu/plugin_list_remote.go cmd/mu/plugin_list_remote_test.go
git commit -m "feat(cli): merge local/remote plugin views"
```

---

## Task 8: Manual end-to-end smoke test

**Files:** none — this is a manual verification step using a local Zot or `docker run -p 5000 registry`.

- [ ] **Step 1: Start a local registry**

```bash
docker run -d --rm -p 5000:5000 --name mu-test-reg registry:2
```

- [ ] **Step 2: Point a test project at it**

In a scratch `mu.cue`:

```cue
cache: {
  backends: [
    {type: "disk", path: "~/.mu/cache"},
    {type: "oci", registry: "localhost:5000", write: true},
  ]
  push: {registry: "localhost:5000", repository: "mu"}
}
```

- [ ] **Step 3: Push a plugin**

```bash
mu plugin push fmt
```

Expected stdout: `Pushed fmt → localhost:5000/mu/mu/plugins/fmt:sha256-<12chars>`.

- [ ] **Step 4: Verify with ORAS**

```bash
oras manifest fetch localhost:5000/mu/mu/plugin-index:v1
oras repo tags localhost:5000/mu/mu/plugins/fmt
```

Expected: index manifest shows `application/vnd.mu.plugin-index.v1+json` config; tag list shows one `sha256-*` tag.

- [ ] **Step 5: Discover from a fresh directory**

```bash
cd $(mktemp -d)
MU_CACHE_BACKENDS=localhost:5000/mu mu plugin list --remote
```

Expected: table showing `fmt` with the right digest.

- [ ] **Step 6: Stop the registry**

```bash
docker stop mu-test-reg
```

---

## Self-Review Notes

- **Spec coverage:**
  - Publish side (Task 3–5): ✅
  - Discovery side (Task 6): ✅
  - Works outside a project (Task 6, `MU_CACHE_BACKENDS`): ✅
  - Mu-scoped namespace to avoid collisions (`mu/plugins`, `mu/plugin-index`): ✅ (Tasks 1, 3, 4)
  - Media type validation on fetch: ✅ (Task 3, 4 tests)
- **Known weaknesses:**
  - `collectPluginFiles` in Task 5 is fragile for single-file plugins — the `<name><ext>` reconstruction may not match what the source tree called the file. A future refinement is to store the original filename in build metadata. For v1, the entrypoint declared in `PluginConfig.Entrypoint` is the source of truth; callers fetching the plugin back use that.
  - No referrers API usage in this plan; index is the discovery mechanism. Referrers can be added later for metadata like discover output, without breaking the index contract.
  - Index update is not atomic — concurrent pushers may race; last-writer-wins. Acceptable for v1.
  - `newReadRepository` is currently an alias for `newPushRepository`; true read-only auth scoping can be added later.
