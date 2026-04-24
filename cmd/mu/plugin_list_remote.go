package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/chau/mu/internal/cas/oci"
)

// remotePlugin is a row in `mu plugin list --remote` output.
type remotePlugin struct {
	Name     string `json:"name"`
	Version  string `json:"version"`  // OCI tag (e.g. sha256-abc123def456)
	Location string `json:"location"` // backend ref, e.g. "registry.example.com/mu"
	Digest   string `json:"digest"`   // CAS digest from PluginConfig (e.g. sha256:...)
}

// pluginRepoOpener returns an oci.Registry rooted at the per-plugin sub-repo
// "<backend>/mu/plugins/<name>". Production callers pass a function that
// builds a real *remote.Repository; tests pass a function backed by fakes.
type pluginRepoOpener func(name string) (oci.Registry, error)

// listFromBackend reads the plugin index at backend, then for each plugin
// listed walks tags via the per-plugin sub-repo (opened via openPluginRepo)
// and fetches each plugin manifest's config for metadata. Returns one row
// per (plugin, version) pair found.
//
// A missing or empty index returns nil rows + nil error so an empty backend
// shows up as "no plugins found here," not as a fatal failure of the whole
// list operation.
func listFromBackend(ctx context.Context, backend string, indexRepo oci.Registry, openPluginRepo pluginRepoOpener) ([]remotePlugin, error) {
	idx, err := oci.FetchPluginIndex(ctx, indexRepo)
	if err != nil {
		return nil, fmt.Errorf("backend %s: %w", backend, err)
	}
	if len(idx.Plugins) == 0 {
		return nil, nil
	}

	var rows []remotePlugin
	for _, name := range idx.Plugins {
		pluginRepo, err := openPluginRepo(name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  %s/%s: %v\n", backend, name, err)
			continue
		}
		err = pluginRepo.Tags(ctx, "", func(tags []string) error {
			for _, t := range tags {
				if !strings.HasPrefix(t, "sha256-") {
					continue
				}
				cfg, ferr := oci.FetchPluginConfig(ctx, pluginRepo, t)
				if ferr != nil {
					// Skip foreign artifacts silently — index promised a plugin,
					// but this tag may be unrelated. Log only at verbose.
					continue
				}
				rows = append(rows, remotePlugin{
					Name:     cfg.Name,
					Version:  t,
					Location: backend,
					Digest:   cfg.Digest,
				})
			}
			return nil
		})
		if err != nil {
			// Tag listing unsupported (some registries lack /v2/<n>/tags/list).
			// The index told us this plugin exists; emit an unversioned row.
			rows = append(rows, remotePlugin{
				Name:     name,
				Version:  "?",
				Location: backend,
			})
		}
	}
	return rows, nil
}

// runPluginListRemote is the CLI entry point for `mu plugin list --remote`.
// Prints rows for plugins discovered across all configured remote backends.
func runPluginListRemote(c *cliContext, jsonOut bool) int {
	ctx := context.Background()

	refs := resolveRemoteBackendRefs(c)
	if len(refs) == 0 {
		fmt.Fprintln(os.Stderr,
			`no remote backends configured. Either:
  - run from a project with cache.backends (oci type) configured, or
  - set MU_CACHE_BACKENDS="<registry>/<repository>[,<registry>/<repository>...]"`)
		return exitUsage
	}

	var all []remotePlugin
	for _, ref := range refs {
		indexRepoRef := ref + "/" + oci.PluginIndexRef
		indexRepo, err := newPushRepository(indexRepoRef)
		if err != nil {
			fmt.Fprintf(os.Stderr, "backend %s: %v\n", ref, err)
			continue
		}
		opener := func(name string) (oci.Registry, error) {
			return newPushRepository(ref + "/" + oci.PluginRepoPrefix + "/" + name)
		}
		rows, err := listFromBackend(ctx, ref, indexRepo, opener)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			continue
		}
		all = append(all, rows...)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(all)
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

// resolveRemoteBackendRefs collects "<registry>/<repository>" backend refs
// from MU_CACHE_BACKENDS and (if c is non-nil and a project config loaded)
// from cache.backends + cache.push. Deduplicated, in encounter order.
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
		repo := ""
		if c.Config.Cache.Push != nil {
			repo = c.Config.Cache.Push.Repository
		}
		for _, b := range c.Config.Cache.Backends {
			if b.Type != "oci" {
				continue
			}
			if b.Read != nil && !*b.Read {
				continue
			}
			if repo == "" {
				continue // can't form a full ref without a repository
			}
			add(b.Registry + "/" + repo)
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
