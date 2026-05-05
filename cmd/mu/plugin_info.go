package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/oci"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
	"github.com/chau/mu/internal/plugin"
)

// runPluginInfo implements `mu plugin info <name>`.
//
// Resolution order:
//  1. If a project mu.cue declares a plugin with this name, resolve it the
//     same way a build would (via PluginResolver) — fetching/extracting from
//     CAS or local sources as needed.
//  2. Otherwise, fall back to ~/.mu/plugins/<name>/, which works outside of
//     any project for plugins previously seen by this user.
//
// In both cases the plugin process is started briefly to run discover, then
// shut down. No build is triggered.
func runPluginInfo(args []string) int {
	fs := flag.NewFlagSet("plugin info", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin info", fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin info <name>")
		return exitUsage
	}
	name := fs.Arg(0)

	// Try project config first; absence isn't fatal (cache-only lookup).
	if root, err := os.Getwd(); err == nil {
		if pr, perr := config.FindProjectRoot(root); perr == nil {
			cli.ProjectRoot = pr
			if cfg, cerr := config.Load(pr); cerr == nil {
				cli.Config = cfg
			}
		}
	}
	cli.JSON = isJSONFlag(fs)

	source, def, dgst, err := resolveInfoTarget(cli.ProjectRoot, cli.Config, name)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin info: %v\n", err)
		return 1
	}

	mgr := plugin.NewManager(cli.ProjectRoot)
	if def.Toolchain == "bb" {
		bb := resolveBbPath()
		if bb == "" {
			fmt.Fprintln(os.Stderr, "mu plugin info: bb toolchain not found; run mu build to bootstrap it")
			return 1
		}
		mgr.SetScriptRuntime(bb)
	}
	if err := mgr.Register(def); err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin info: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin info: starting plugin: %v\n", err)
		return 1
	}
	defer mgr.Close()

	info := mgr.DiscoverInfo(name)
	if info == nil {
		fmt.Fprintf(os.Stderr, "mu plugin info: plugin %q produced no discover response\n", name)
		return 1
	}

	out := pluginInfoOutput{
		Name:            info.Name,
		Version:         info.Version,
		ProtocolVersion: info.ProtocolVersion,
		Source:          source,
		Capabilities:    info.Capabilities,
		Consumes:        info.Consumes,
		Produces:        info.Produces,
		ConfigSchema:    info.ConfigSchema,
		OutputSchema:    info.OutputSchema,
		Toolchain:       def.Toolchain,
		Path:            def.Script,
	}
	if !dgst.IsZero() {
		s := dgst.String()
		out.Digest = s
	}

	if cli.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(out)
		return 0
	}

	printPluginInfo(out)
	return 0
}

type pluginInfoOutput struct {
	Name            string             `json:"name"`
	Version         string             `json:"version,omitempty"`
	ProtocolVersion int                `json:"protocol_version,omitempty"`
	Source          string             `json:"source"` // "project" | "cache"
	Digest          string             `json:"digest,omitempty"`
	Toolchain       string             `json:"toolchain,omitempty"`
	Path            string             `json:"path,omitempty"`
	Capabilities    []string           `json:"capabilities,omitempty"`
	Consumes        []string           `json:"consumes,omitempty"`
	Produces        []string           `json:"produces,omitempty"`
	ConfigSchema    map[string]any     `json:"config_schema,omitempty"`
	OutputSchema    *plugin.SchemaRef  `json:"output_schema,omitempty"`
}

// resolveInfoTarget locates the plugin by name. Returns the source label
// ("project" or "cache"), a ready-to-Register PluginDef, and the CAS digest
// when known.
func resolveInfoTarget(projectRoot string, cfg *config.ProjectConfig, name string) (string, plugin.PluginDef, cas.Digest, error) {
	if cfg != nil {
		for _, p := range cfg.Plugins {
			if p.Name != name {
				continue
			}
			def, dgst, err := resolveProjectPlugin(projectRoot, p)
			if err != nil {
				return "", plugin.PluginDef{}, cas.Digest{}, err
			}
			return "project", def, dgst, nil
		}
	}

	def, dgst, err := resolveCachedPlugin(name)
	if err != nil {
		return "", plugin.PluginDef{}, cas.Digest{}, err
	}
	return "cache", def, dgst, nil
}

// resolveProjectPlugin runs the same resolver the coordinator uses, so a
// configured plugin (digest, url, local path, or command) is materialized
// to disk and ready to spawn.
func resolveProjectPlugin(projectRoot string, p config.PluginDef) (plugin.PluginDef, cas.Digest, error) {
	if len(p.Command) > 0 && p.Script == "" && p.Digest == "" && p.URL == "" {
		return plugin.PluginDef{Name: p.Name, Command: p.Command}, cas.Digest{}, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return plugin.PluginDef{}, cas.Digest{}, err
	}
	cachePath := filepath.Join(home, ".mu", "cache")
	store, err := oci.NewLocal(cachePath)
	if err != nil {
		return plugin.PluginDef{}, cas.Digest{}, err
	}
	resolver := &coordinator.PluginResolver{
		Store:       store,
		ProjectRoot: projectRoot,
		CacheDir:    filepath.Join(home, ".mu", "plugins"),
	}
	resolved, err := resolver.Resolve(context.Background(), []config.PluginDef{p})
	if err != nil {
		return plugin.PluginDef{}, cas.Digest{}, err
	}
	if len(resolved) == 0 {
		return plugin.PluginDef{}, cas.Digest{}, fmt.Errorf("plugin %q: resolver returned no entries", p.Name)
	}
	return resolved[0].Def, resolved[0].Digest, nil
}

// resolveCachedPlugin inspects ~/.mu/plugins/<name>/ for an extracted
// bundle directory or a single-file plugin and builds a PluginDef from it.
// This works without a project mu.cue.
func resolveCachedPlugin(name string) (plugin.PluginDef, cas.Digest, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return plugin.PluginDef{}, cas.Digest{}, err
	}
	dir := filepath.Join(home, ".mu", "plugins", name)
	if _, err := os.Stat(dir); err != nil {
		return plugin.PluginDef{}, cas.Digest{}, fmt.Errorf("plugin %q not found in project config or %s", name, dir)
	}

	// Prefer bundle-* (directory plugin with manifest).
	bundles, _ := filepath.Glob(filepath.Join(dir, "bundle-*"))
	sort.Strings(bundles)
	if len(bundles) > 0 {
		bundleDir := bundles[len(bundles)-1] // newest by lexicographic short hash
		entry, toolchain, err := resolveBundleEntry(bundleDir)
		if err != nil {
			return plugin.PluginDef{}, cas.Digest{}, err
		}
		dgst := cas.NewSHA256(strings.TrimPrefix(filepath.Base(bundleDir), "bundle-"))
		return plugin.PluginDef{
			Name:      name,
			Script:    entry,
			Toolchain: toolchain,
			WorkDir:   bundleDir,
		}, dgst, nil
	}

	// Fallback: single-file plugin-<short>.<ext>.
	singles, _ := filepath.Glob(filepath.Join(dir, "plugin-*"))
	sort.Strings(singles)
	if len(singles) > 0 {
		path := singles[len(singles)-1]
		base := strings.TrimPrefix(filepath.Base(path), "plugin-")
		hash := base
		if idx := strings.IndexByte(base, '.'); idx >= 0 {
			hash = base[:idx]
		}
		dgst := cas.NewSHA256(hash)
		return plugin.PluginDef{
			Name:      name,
			Script:    path,
			Toolchain: inferPluginToolchain(path),
		}, dgst, nil
	}

	return plugin.PluginDef{}, cas.Digest{}, fmt.Errorf("no cached plugin files in %s", dir)
}

// resolveBundleEntry picks an entrypoint in a bundle-* directory. If a
// plugin manifest (mu.cue) is present it wins; otherwise we fall back to
// the conventional plugin.bb / plugin / single-file layouts produced by
// older bundle artifacts.
func resolveBundleEntry(bundleDir string) (string, string, error) {
	if manifest, err := config.LoadPluginManifest(bundleDir); err == nil {
		entry := filepath.Join(bundleDir, manifest.Plugin.Entrypoint)
		if _, err := os.Stat(entry); err != nil {
			return "", "", fmt.Errorf("entrypoint %s missing: %w", entry, err)
		}
		toolchain := manifest.Plugin.Toolchain
		if toolchain == "" {
			toolchain = inferPluginToolchain(entry)
		}
		return entry, toolchain, nil
	}
	for _, candidate := range []string{"plugin.bb", "plugin"} {
		p := filepath.Join(bundleDir, candidate)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p, inferPluginToolchain(p), nil
		}
	}
	// Last resort: pick the only regular file in the bundle.
	entries, err := os.ReadDir(bundleDir)
	if err != nil {
		return "", "", err
	}
	var only string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if only != "" {
			only = ""
			break
		}
		only = filepath.Join(bundleDir, e.Name())
	}
	if only != "" {
		return only, inferPluginToolchain(only), nil
	}
	return "", "", fmt.Errorf("no entrypoint found in %s (no manifest, no plugin.bb)", bundleDir)
}

func printPluginInfo(o pluginInfoOutput) {
	fmt.Printf("Name:             %s\n", o.Name)
	if o.Version != "" {
		fmt.Printf("Version:          %s\n", o.Version)
	}
	if o.ProtocolVersion != 0 {
		fmt.Printf("Protocol:         %d\n", o.ProtocolVersion)
	}
	fmt.Printf("Source:           %s\n", o.Source)
	if o.Digest != "" {
		fmt.Printf("Digest:           %s\n", o.Digest)
	}
	if o.Toolchain != "" {
		fmt.Printf("Toolchain:        %s\n", o.Toolchain)
	}
	if o.Path != "" {
		fmt.Printf("Path:             %s\n", o.Path)
	}
	if len(o.Capabilities) > 0 {
		fmt.Printf("Capabilities:     %s\n", strings.Join(o.Capabilities, ", "))
	} else {
		fmt.Printf("Capabilities:     discover, plan (default)\n")
	}
	fmt.Printf("Consumes:         %s\n", joinOrDash(o.Consumes))
	fmt.Printf("Produces:         %s\n", joinOrDash(o.Produces))
	if o.OutputSchema != nil {
		ref := o.OutputSchema.Module + "@" + o.OutputSchema.Version
		if o.OutputSchema.Definition != "" {
			ref += " " + o.OutputSchema.Definition
		}
		if o.OutputSchema.Source != "" {
			ref += " (" + o.OutputSchema.Source + ")"
		}
		fmt.Printf("Output schema:    %s\n", ref)
	}
	if len(o.ConfigSchema) > 0 {
		fmt.Println("Config schema:")
		b, _ := json.MarshalIndent(o.ConfigSchema, "  ", "  ")
		fmt.Printf("  %s\n", string(b))
	}
}

// isJSONFlag reads the --json flag from fs after Parse. Resolve sets
// this normally, but we skip Resolve to avoid its "mu.cue not found"
// stderr when running outside a project.
func isJSONFlag(fs *flag.FlagSet) bool {
	f := fs.Lookup("json")
	if f == nil {
		return false
	}
	return f.Value.String() == "true"
}

func joinOrDash(s []string) string {
	if len(s) == 0 {
		return "-"
	}
	return strings.Join(s, ", ")
}
