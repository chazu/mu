package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

// gatherRemotePlugins iterates configured backends and returns all discovered
// plugins. Errors against individual backends are written to stderr and the
// scan continues.
func gatherRemotePlugins(ctx context.Context, c *cliContext) []remotePlugin {
	refs := resolveRemoteBackendRefs(c)
	if len(refs) == 0 {
		fmt.Fprintln(os.Stderr,
			`no remote backends configured. Either:
  - run from a project with cache.backends (oci type) configured, or
  - set MU_CACHE_BACKENDS="<registry>/<repository>[,<registry>/<repository>...]"`)
		return nil
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
	return all
}

// runPluginListRemote is the CLI entry point for `mu plugin list --remote`.
// Prints rows for plugins discovered across all configured remote backends.
func runPluginListRemote(c *cliContext, jsonOut bool) int {
	ctx := context.Background()
	all := gatherRemotePlugins(ctx, c)

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(all); err != nil {
			fmt.Fprintf(os.Stderr, "encode JSON: %v\n", err)
			return exitFail
		}
		return exitOK
	}

	if len(all) == 0 {
		// Either backends weren't configured (gatherRemotePlugins printed) or
		// no plugins were found. Print a benign message in the latter case.
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

// runPluginListRemoteCached prints remote plugins annotated with whether each
// is also present in the local cache. This is the union of `--remote` (rows)
// and `--cached` (cached column), focused on the remote view since local-only
// plugins are already discoverable via `--cached` alone.
func runPluginListRemoteCached(c *cliContext, jsonOut bool) int {
	ctx := context.Background()
	remote := gatherRemotePlugins(ctx, c)
	local := localPluginNames()
	rows := mergeLocalRemote(remote, local)

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(rows); err != nil {
			fmt.Fprintf(os.Stderr, "encode JSON: %v\n", err)
			return exitFail
		}
		return exitOK
	}

	if len(rows) == 0 {
		return exitOK
	}

	fmt.Printf("%-20s %-25s %-40s %-7s %s\n", "PLUGIN", "VERSION", "LOCATION", "CACHED", "DIGEST")
	for _, r := range rows {
		dig := r.Digest
		if len(dig) > 23 {
			dig = dig[:23] + "..."
		}
		cached := "no"
		if r.Cached {
			cached = "yes"
		}
		fmt.Printf("%-20s %-25s %-40s %-7s %s\n", r.Name, r.Version, r.Location, cached, dig)
	}
	return exitOK
}

// localPluginNames returns the names of plugins extracted under
// ~/.mu/plugins/. Mirrors the directory walk in pluginListCached but returns
// only names, not digests.
func localPluginNames() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	pluginsDir := filepath.Join(home, ".mu", "plugins")
	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// Has at least one bundle-* or plugin-* artifact?
		sub, _ := os.ReadDir(filepath.Join(pluginsDir, e.Name()))
		for _, s := range sub {
			if strings.HasPrefix(s.Name(), "bundle-") || strings.HasPrefix(s.Name(), "plugin-") {
				names = append(names, e.Name())
				break
			}
		}
	}
	return names
}

// mergedRow represents one plugin row in a merged local+remote view. The
// `Cached` flag indicates whether the plugin name (regardless of version) is
// present in the local cache. Local-only plugins (in cache but not in any
// remote backend) are NOT included — those surface via `mu plugin list
// --cached` instead.
type mergedRow struct {
	Name     string `json:"name"`
	Version  string `json:"version,omitempty"`
	Location string `json:"location"`
	Cached   bool   `json:"cached"`
	Digest   string `json:"digest,omitempty"`
}

// mergeLocalRemote produces a row per remote plugin, marking Cached=true when
// the plugin name appears in localNames. Local-only plugins are deliberately
// omitted; the caller is expected to merge with `--cached` output separately
// if it wants the full union.
func mergeLocalRemote(remote []remotePlugin, localNames []string) []mergedRow {
	localSet := make(map[string]bool, len(localNames))
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
