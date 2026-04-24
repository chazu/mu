package main

import (
	"context"
	"strings"
	"testing"

	"github.com/chau/mu/internal/cas/oci"
)

func TestListPluginsInBackend(t *testing.T) {
	ctx := context.Background()

	// In production, plugins live at <ref>/mu/plugins/<name> and the index
	// at <ref>/mu/plugin-index. Each is its own Registry handle (sub-repo).
	// In tests we model that by passing per-plugin and index repos.
	indexRepo := newFakeRegistry()
	pluginRepos := map[string]oci.Registry{
		"fmt":  newFakeRegistry(),
		"lint": newFakeRegistry(),
	}

	// Seed: push two plugins (each to its own repo) and update the index.
	for _, p := range []struct {
		name   string
		digest string
	}{
		{"fmt", "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
		{"lint", "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"},
	} {
		cfg := oci.PluginConfig{
			Name: p.name, Entrypoint: p.name + ".bb",
			Toolchain: "bb", Files: []string{p.name + ".bb"},
			Digest: p.digest,
		}
		files := map[string][]byte{p.name + ".bb": []byte("#!/usr/bin/env bb\n")}
		if _, err := oci.PushPlugin(ctx, pluginRepos[p.name], p.name, cfg, files); err != nil {
			t.Fatalf("seed %s: %v", p.name, err)
		}
		if err := oci.UpdatePluginIndex(ctx, indexRepo, p.name); err != nil {
			t.Fatalf("update index %s: %v", p.name, err)
		}
	}

	// listFromBackend takes a function that returns a per-plugin Registry,
	// since real registries have separate sub-repos per plugin.
	openPluginRepo := func(name string) (oci.Registry, error) {
		return pluginRepos[name], nil
	}

	rows, err := listFromBackend(ctx, "fake-registry/mu", indexRepo, openPluginRepo)
	if err != nil {
		t.Fatalf("listFromBackend: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 plugins, got %d: %+v", len(rows), rows)
	}
	names := []string{rows[0].Name, rows[1].Name}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "fmt") || !strings.Contains(joined, "lint") {
		t.Fatalf("missing plugin names in %v", names)
	}
	for _, r := range rows {
		if r.Location != "fake-registry/mu" {
			t.Fatalf("Location: got %q", r.Location)
		}
		if !strings.HasPrefix(r.Version, "sha256-") {
			t.Fatalf("Version: got %q (want sha256-* tag)", r.Version)
		}
	}
}

func TestListFromBackendEmptyIndex(t *testing.T) {
	ctx := context.Background()
	indexRepo := newFakeRegistry()
	openRepo := func(string) (oci.Registry, error) { return newFakeRegistry(), nil }

	rows, err := listFromBackend(ctx, "empty.example/mu", indexRepo, openRepo)
	if err != nil {
		t.Fatalf("listFromBackend: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected 0 rows on empty index, got %d", len(rows))
	}
}

func TestResolveRemoteBackendRefsFromEnv(t *testing.T) {
	t.Setenv("MU_CACHE_BACKENDS", "host1.example/repo, host2.example/other")
	refs := resolveRemoteBackendRefs(nil)
	if len(refs) != 2 {
		t.Fatalf("expected 2 refs, got %v", refs)
	}
	if refs[0] != "host1.example/repo" || refs[1] != "host2.example/other" {
		t.Fatalf("refs: %v", refs)
	}
}

func TestResolveRemoteBackendRefsDeduplicates(t *testing.T) {
	t.Setenv("MU_CACHE_BACKENDS", "h/r,h/r,h/r")
	refs := resolveRemoteBackendRefs(nil)
	if len(refs) != 1 || refs[0] != "h/r" {
		t.Fatalf("dedup failed: %v", refs)
	}
}

func TestMergeLocalAndRemote(t *testing.T) {
	remote := []remotePlugin{
		{Name: "fmt", Version: "sha256-abc", Location: "registry.example/mu", Digest: "sha256:abc"},
		{Name: "lint", Version: "sha256-def", Location: "registry.example/mu", Digest: "sha256:def"},
	}
	local := []string{"fmt"} // fmt is cached locally; lint is not

	rows := mergeLocalRemote(remote, local)

	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}

	var fmtRow, lintRow *mergedRow
	for i := range rows {
		switch rows[i].Name {
		case "fmt":
			fmtRow = &rows[i]
		case "lint":
			lintRow = &rows[i]
		}
	}
	if fmtRow == nil {
		t.Fatal("missing fmt row")
	}
	if !fmtRow.Cached {
		t.Errorf("fmt should be cached: %+v", *fmtRow)
	}
	if fmtRow.Version != "sha256-abc" {
		t.Errorf("fmt Version: got %q", fmtRow.Version)
	}
	if fmtRow.Digest != "sha256:abc" {
		t.Errorf("fmt Digest: got %q", fmtRow.Digest)
	}

	if lintRow == nil {
		t.Fatal("missing lint row")
	}
	if lintRow.Cached {
		t.Errorf("lint should NOT be cached: %+v", *lintRow)
	}
}

func TestMergeLocalAndRemoteEmptyInputs(t *testing.T) {
	// Both empty.
	if rows := mergeLocalRemote(nil, nil); len(rows) != 0 {
		t.Fatalf("expected 0 rows for empty inputs, got %d", len(rows))
	}
	// Remote empty, local non-empty: still 0 rows (we only merge into the
	// remote view; local-only plugins surface via `--cached`, not here).
	if rows := mergeLocalRemote(nil, []string{"only-local"}); len(rows) != 0 {
		t.Fatalf("expected 0 rows when remote is empty, got %d", len(rows))
	}
	// Remote non-empty, local empty: every row is uncached.
	rows := mergeLocalRemote(
		[]remotePlugin{{Name: "x", Version: "v", Location: "l", Digest: "d"}}, nil)
	if len(rows) != 1 || rows[0].Cached {
		t.Fatalf("expected 1 uncached row, got %+v", rows)
	}
}
