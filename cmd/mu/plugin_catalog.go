package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/plugincatalog"
)

func defaultPluginCatalogURL() string {
	if value := strings.TrimSpace(os.Getenv("MU_PLUGIN_CATALOG")); value != "" {
		return value
	}
	return plugincatalog.DefaultURL
}

func runPluginSearch(args []string) int {
	fs := flag.NewFlagSet("plugin search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin search", fs)
	catalogURL := fs.String("catalog", defaultPluginCatalogURL(), "catalog URL (or MU_PLUGIN_CATALOG)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin search [--catalog URL] [query]")
		return exitUsage
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	catalog, err := plugincatalog.Fetch(ctx, *catalogURL)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	query := ""
	if fs.NArg() == 1 {
		query = fs.Arg(0)
	}
	items := catalog.Search(query)
	if cli.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(items); err != nil {
			return cli.fail(exitFail, "writing results: %v", err)
		}
		return exitOK
	}
	if len(items) == 0 {
		fmt.Println("No plugins found.")
		return exitOK
	}
	fmt.Printf("%-18s %-9s %-28s %s\n", "PLUGIN", "VERSION", "TOOLCHAIN", "DESCRIPTION")
	for _, item := range items {
		toolchain := item.Toolchain
		if toolchain == "" {
			toolchain = "direct"
		}
		fmt.Printf("%-18s %-9s %-28s %s\n", item.Name, item.Version, toolchain, item.Description)
	}
	return exitOK
}

func runPluginInstall(args []string) int {
	fs := flag.NewFlagSet("plugin install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin install", fs)
	catalogURL := fs.String("catalog", defaultPluginCatalogURL(), "catalog URL (or MU_PLUGIN_CATALOG)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin install [--catalog URL] <name[@version]>")
		return exitUsage
	}
	name, version, err := splitPluginRef(fs.Arg(0))
	if err != nil {
		return cli.fail(exitUsage, "%v", err)
	}
	if code, ok := cli.Resolve(resolveOpts{NeedConfig: true, ValidateConfig: true, NeedStore: true}); !ok {
		return code
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	catalog, err := plugincatalog.Fetch(ctx, *catalogURL)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	selected, err := catalog.Select(name, version)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	entry, err := installCatalogPlugin(ctx, cli, *catalogURL, catalog, selected)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	fmt.Fprintf(os.Stdout, "installed plugin %q %s (%s)\n", entry.Name, entry.Version, entry.BundleDigest)
	return exitOK
}

func runPluginUpdate(args []string) int {
	fs := flag.NewFlagSet("plugin update", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin update", fs)
	catalogURL := fs.String("catalog", defaultPluginCatalogURL(), "catalog URL (or MU_PLUGIN_CATALOG)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() > 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin update [--catalog URL] [name]")
		return exitUsage
	}
	if code, ok := cli.Resolve(resolveOpts{NeedConfig: true, ValidateConfig: true, NeedStore: true}); !ok {
		return code
	}
	lockPath := filepath.Join(cli.ProjectRoot, "mu.lock")
	lock, err := plugincatalog.LoadLock(lockPath)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	if len(lock.Plugins) == 0 {
		return cli.fail(exitFail, "no plugins are locked; run mu plugin install first")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	catalog, err := plugincatalog.Fetch(ctx, *catalogURL)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}

	names := make([]string, 0, len(lock.Plugins))
	if fs.NArg() == 1 {
		name := fs.Arg(0)
		if _, ok := lock.Find(name); !ok {
			return cli.fail(exitFail, "plugin %q is not in mu.lock; run mu plugin install first", name)
		}
		names = append(names, name)
	} else {
		for _, p := range lock.Plugins {
			names = append(names, p.Name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		selected, selectErr := catalog.Select(name, "")
		if selectErr != nil {
			return cli.fail(exitFail, "%v", selectErr)
		}
		if _, installErr := installCatalogPlugin(ctx, cli, *catalogURL, catalog, selected); installErr != nil {
			return cli.fail(exitFail, "%v", installErr)
		}
		fmt.Fprintf(os.Stdout, "updated plugin %q to %s\n", name, selected.Version)
	}
	return exitOK
}

func runPluginLock(args []string) int {
	fs := flag.NewFlagSet("plugin lock", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin lock", fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin lock [--config path] [--json]")
		return exitUsage
	}
	if code, ok := cli.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}
	lock, err := plugincatalog.LoadLock(filepath.Join(cli.ProjectRoot, "mu.lock"))
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	if cli.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(lock); err != nil {
			return cli.fail(exitFail, "writing lockfile: %v", err)
		}
		return exitOK
	}
	if len(lock.Plugins) == 0 {
		fmt.Println("No plugins locked.")
		return exitOK
	}
	fmt.Printf("%-18s %-9s %-18s %s\n", "PLUGIN", "VERSION", "SOURCE", "BUNDLE")
	for _, p := range lock.Plugins {
		fmt.Printf("%-18s %-9s %-18s %s\n", p.Name, p.Version, p.SourceRevision, p.BundleDigest)
	}
	return exitOK
}

func splitPluginRef(ref string) (name, version string, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", fmt.Errorf("plugin reference is empty")
	}
	if strings.Count(ref, "@") > 1 {
		return "", "", fmt.Errorf("plugin reference %q has more than one @", ref)
	}
	name, version, _ = strings.Cut(ref, "@")
	if name == "" {
		return "", "", fmt.Errorf("plugin reference %q has no name", ref)
	}
	if strings.ContainsAny(name, `/\\`) {
		return "", "", fmt.Errorf("plugin name %q must not contain a path separator", name)
	}
	if strings.Contains(version, " ") {
		return "", "", fmt.Errorf("plugin version %q contains whitespace", version)
	}
	return name, version, nil
}

func installCatalogPlugin(ctx context.Context, cli *cliContext, catalogURL string, catalog *plugincatalog.Catalog, selected plugincatalog.Plugin) (plugincatalog.LockedPlugin, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return plugincatalog.LockedPlugin{}, fmt.Errorf("resolving home directory: %w", err)
	}
	lockPath := filepath.Join(cli.ProjectRoot, "mu.lock")
	lock, err := plugincatalog.LoadLock(lockPath)
	if err != nil {
		return plugincatalog.LockedPlugin{}, err
	}

	resolver := &coordinator.PluginResolver{
		Store:       cli.Store,
		ProjectRoot: cli.ProjectRoot,
		CacheDir:    filepath.Join(home, ".mu", "plugins"),
	}
	var resolved coordinator.ResolvedPlugin
	if previous, ok := lock.Find(selected.Name); ok && previous.Version == selected.Version &&
		previous.AssetSHA256 == selected.SHA256 && previous.BundleDigest != "" {
		bundleDigest, parseErr := cas.ParseDigest(previous.BundleDigest)
		if parseErr == nil {
			if present, hasErr := cli.Store.Has(ctx, bundleDigest); hasErr == nil && present {
				cached, resolveErr := resolver.Resolve(ctx, []config.PluginDef{{Name: selected.Name, Digest: previous.BundleDigest}})
				if resolveErr == nil && len(cached) == 1 {
					resolved = cached[0]
				}
			}
		}
	}

	if resolved.Digest.IsZero() {
		tmpDir, mkErr := os.MkdirTemp("", "mu-plugin-install-*")
		if mkErr != nil {
			return plugincatalog.LockedPlugin{}, fmt.Errorf("create install workspace: %w", mkErr)
		}
		defer os.RemoveAll(tmpDir)
		assetPath := filepath.Join(tmpDir, "package.tar.gz")
		if fetchErr := plugincatalog.DownloadAsset(ctx, selected, assetPath); fetchErr != nil {
			return plugincatalog.LockedPlugin{}, fetchErr
		}
		sourceRoot := filepath.Join(tmpDir, "source")
		if extractErr := plugincatalog.ExtractArchive(assetPath, sourceRoot); extractErr != nil {
			return plugincatalog.LockedPlugin{}, fmt.Errorf("extract plugin %q: %w", selected.Name, extractErr)
		}
		packageDir := filepath.Join(sourceRoot, filepath.FromSlash(selected.Path))
		if err := ensureDirectory(packageDir, sourceRoot); err != nil {
			return plugincatalog.LockedPlugin{}, fmt.Errorf("plugin %q: %w", selected.Name, err)
		}
		for _, source := range catalogBuildSources(selected) {
			if err := ensureFile(filepath.Join(packageDir, filepath.FromSlash(source)), packageDir); err != nil {
				return plugincatalog.LockedPlugin{}, fmt.Errorf("plugin %q: %w", selected.Name, err)
			}
		}
		entryPath := filepath.Join(packageDir, filepath.FromSlash(selected.Entrypoint))
		if _, statErr := os.Stat(entryPath); os.IsNotExist(statErr) {
			if selected.Build == nil {
				return plugincatalog.LockedPlugin{}, fmt.Errorf("plugin %q entrypoint %q is missing and has no build command", selected.Name, selected.Entrypoint)
			}
			cmd := exec.CommandContext(ctx, selected.Build.Command[0], selected.Build.Command[1:]...)
			cmd.Dir = packageDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			if runErr := cmd.Run(); runErr != nil {
				return plugincatalog.LockedPlugin{}, fmt.Errorf("build plugin %q: %w", selected.Name, runErr)
			}
		}
		resolvedList, resolveErr := resolver.Resolve(ctx, []config.PluginDef{{Name: selected.Name, Script: packageDir}})
		if resolveErr != nil {
			return plugincatalog.LockedPlugin{}, fmt.Errorf("bundle plugin %q: %w", selected.Name, resolveErr)
		}
		if len(resolvedList) != 1 {
			return plugincatalog.LockedPlugin{}, fmt.Errorf("bundle plugin %q: resolver returned %d entries", selected.Name, len(resolvedList))
		}
		resolved = resolvedList[0]
	}

	if err := updateMuCuePlugin(cli.ProjectRoot, selected.Name, resolved.Digest.String()); err != nil {
		return plugincatalog.LockedPlugin{}, fmt.Errorf("update mu.cue: %w", err)
	}
	if err := writeInstalledPluginMetadata(home, selected, resolved.Digest); err != nil {
		return plugincatalog.LockedPlugin{}, err
	}
	lock.Catalog = plugincatalog.LockedCatalog{
		URL:        catalogURL,
		Repository: catalog.Repository,
		ReleaseTag: catalog.ReleaseTag,
	}
	entry := plugincatalog.LockedPlugin{
		Name:           selected.Name,
		Version:        selected.Version,
		SourceRevision: catalog.ReleaseTag,
		AssetURL:       selected.AssetURL,
		AssetSHA256:    selected.SHA256,
		Path:           selected.Path,
		Entrypoint:     selected.Entrypoint,
		Toolchain:      selected.Toolchain,
		BundleDigest:   resolved.Digest.String(),
		Schemas:        selected.Schemas,
		PUDLMappings:   selected.PUDLMappings,
	}
	lock.Upsert(entry)
	if err := plugincatalog.WriteLock(lockPath, lock); err != nil {
		return plugincatalog.LockedPlugin{}, fmt.Errorf("write mu.lock: %w", err)
	}
	return entry, nil
}

// installedPluginMetadata is the cache-side copy of catalog metadata. The
// extracted bundle deliberately does not need to contain mu.cue, so tools
// such as pudl can inspect an installed plugin without re-fetching its source
// archive or importing mu's implementation packages.
type installedPluginMetadata struct {
	Name         string                      `json:"name"`
	Version      string                      `json:"version"`
	BundleDigest string                      `json:"bundle_digest"`
	Entrypoint   string                      `json:"entrypoint"`
	Toolchain    string                      `json:"toolchain,omitempty"`
	Schemas      []plugincatalog.Schema      `json:"schemas,omitempty"`
	PUDLMappings []plugincatalog.PUDLMapping `json:"pudl_mappings,omitempty"`
}

func writeInstalledPluginMetadata(home string, selected plugincatalog.Plugin, digest cas.Digest) error {
	short := digest.Hash
	if len(short) > 12 {
		short = short[:12]
	}
	bundleDir := filepath.Join(home, ".mu", "plugins", selected.Name, "bundle-"+short)
	if info, err := os.Stat(bundleDir); err != nil {
		return fmt.Errorf("locate installed plugin %q: %w", selected.Name, err)
	} else if !info.IsDir() {
		return fmt.Errorf("installed plugin %q bundle is not a directory", selected.Name)
	}
	metadata := installedPluginMetadata{
		Name:         selected.Name,
		Version:      selected.Version,
		BundleDigest: digest.String(),
		Entrypoint:   selected.Entrypoint,
		Toolchain:    selected.Toolchain,
		Schemas:      selected.Schemas,
		PUDLMappings: selected.PUDLMappings,
	}
	data, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return fmt.Errorf("encode installed plugin %q metadata: %w", selected.Name, err)
	}
	path := filepath.Join(bundleDir, "mu-plugin.json")
	if err := os.WriteFile(path, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write installed plugin %q metadata: %w", selected.Name, err)
	}
	return nil
}

func ensureDirectory(path, root string) error {
	if err := ensureWithin(path, root); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("directory %q: %w", path, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("path %q is not a directory", path)
	}
	return nil
}

func ensureFile(path, root string) error {
	if err := ensureWithin(path, root); err != nil {
		return err
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("source %q: %w", path, err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("source %q is not a regular file", path)
	}
	return nil
}

func ensureWithin(path, root string) error {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("path %q escapes %q", path, root)
	}
	return nil
}

func catalogBuildSources(p plugincatalog.Plugin) []string {
	if p.Build == nil {
		return nil
	}
	return p.Build.Sources
}
