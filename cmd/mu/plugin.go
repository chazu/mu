package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/plugin"
	"github.com/chazu/mu/internal/scratch"
)

func runPlugin(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: mu plugin <command>

Commands:
  list      List registered plugins
  add       Add a plugin from cache by building its //plugins/<name> target
  info      Show capabilities and metadata for a plugin (project or cached)
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
	case "info":
		return runPluginInfo(args[1:])
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

func runPluginAdd(args []string) int {
	fs := flag.NewFlagSet("plugin add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin add", fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin add <name>")
		return exitUsage
	}
	pluginName := fs.Arg(0)
	if code, ok := cli.Resolve(resolveOpts{NeedConfig: true, ValidateConfig: true}); !ok {
		return code
	}
	projectRoot := cli.ProjectRoot
	cfg := cli.Config

	// Check that //plugins/<name> target exists.
	targetName := "//plugins/" + pluginName
	found := false
	for _, t := range cfg.Targets {
		if t.Name == targetName {
			found = true
			break
		}
	}
	if !found {
		fmt.Fprintf(os.Stderr, "mu plugin add: target %q not found in config\n", targetName)
		return 1
	}

	// Build the plugin target.
	result, err := buildTargets(projectRoot, cfg, []string{targetName})
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin add: %v\n", err)
		return 1
	}
	if result.Failed > 0 {
		fmt.Fprintf(os.Stderr, "mu plugin add: build failed for %s\n", targetName)
		return 1
	}

	// Extract plugin.bb digest from the build result.
	dgst, err := extractPluginDigest(result, targetName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin add: %v\n", err)
		return 1
	}

	// Update mu.cue with the digest-based plugin entry.
	if err := updateMuCuePlugin(projectRoot, pluginName, dgst.String()); err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin add: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "plugin %q added with digest %s\n", pluginName, dgst)
	return 0
}

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

	// --remote --cached: show remote plugins annotated with local cache status.
	// Must be checked before the individual --cached and --remote branches.
	if *remote && *cached && !*discover {
		_, _ = cli.Resolve(resolveOpts{NeedConfig: true})
		return runPluginListRemoteCached(cli, cli.JSON)
	}

	// --cached (without --discover) scans ~/.mu/plugins/ and needs no project config,
	// so it works outside any mu project — which is the point of discovery.
	if *cached && !*discover {
		return pluginListCached(nil, "", cli.JSON)
	}

	// --remote works outside a project too: env var MU_CACHE_BACKENDS suffices.
	// We still try to load config so cache.backends can contribute, but a
	// missing project is not fatal.
	if *remote {
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

// pluginListCached scans ~/.mu/plugins/ and shows all plugins stored in
// the CAS, regardless of the current project's configuration.
func pluginListCached(_ *config.ProjectConfig, _ string, jsonOut bool) int {
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 1
	}
	pluginsDir := filepath.Join(home, ".mu", "plugins")

	entries, err := os.ReadDir(pluginsDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No cached plugins.")
			return 0
		}
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 1
	}

	type cachedInfo struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
	}

	var items []cachedInfo
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		dir := filepath.Join(pluginsDir, name)
		digest := ""
		if bundles, err := filepath.Glob(filepath.Join(dir, "bundle-*")); err == nil && len(bundles) > 0 {
			base := filepath.Base(bundles[0])
			digest = "sha256:" + strings.TrimPrefix(base, "bundle-")
		} else if singles, err := filepath.Glob(filepath.Join(dir, "plugin-*")); err == nil && len(singles) > 0 {
			base := filepath.Base(singles[0])
			base = strings.TrimPrefix(base, "plugin-")
			if idx := strings.IndexByte(base, '.'); idx >= 0 {
				base = base[:idx]
			}
			digest = "sha256:" + base
		}
		if digest != "" {
			items = append(items, cachedInfo{Name: name, Digest: digest})
		}
	}

	if len(items) == 0 {
		fmt.Println("No cached plugins.")
		return 0
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(items)
		return 0
	}

	fmt.Printf("%-20s %s\n", "PLUGIN", "DIGEST")
	for _, item := range items {
		fmt.Printf("%-20s %s\n", item.Name, item.Digest)
	}
	return 0
}

// pluginListCachedDiscover builds all //plugins/* targets, extracts them from
// CAS, starts each as a plugin process, runs discover, and shows combined info.
func pluginListCachedDiscover(cfg *config.ProjectConfig, projectRoot string, jsonOut bool) int {
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 2
	}

	// Find all //plugins/<name> targets (skip //plugins itself).
	var targets []string
	for _, t := range cfg.Targets {
		if strings.HasPrefix(t.Name, "//plugins/") && !strings.Contains(t.Name[len("//plugins/"):], "/") {
			targets = append(targets, t.Name)
		}
	}

	if len(targets) == 0 {
		fmt.Println("No //plugins/* targets found.")
		return 0
	}

	// Build to get digests.
	result, err := buildTargets(projectRoot, cfg, targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 1
	}

	// Extract each plugin from CAS and collect info.
	home, _ := os.UserHomeDir()
	cacheDir := filepath.Join(home, ".mu", "plugins")
	cachePath := filepath.Join(home, ".mu", "cache")

	store, err := oci.NewLocal(cachePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 1
	}

	type pluginToDiscover struct {
		name   string
		digest cas.Digest
		cached bool
		path   string
	}

	var plugins []pluginToDiscover
	needsBB := false

	for _, target := range targets {
		name := strings.TrimPrefix(target, "//plugins/")
		dgst, err := extractPluginDigest(result, target)
		if err != nil {
			continue
		}
		wasCached := false
		for _, s := range result.ExecResult.Completed {
			if strings.HasPrefix(s.ID, target+":") && s.Cached {
				wasCached = true
				break
			}
		}

		// Find the original source extension from the target config.
		ext := ".bb" // default
		for _, t := range cfg.Targets {
			if t.Name == target && len(t.Sources) > 0 {
				ext = filepath.Ext(t.Sources[0])
				break
			}
		}

		// Extract from CAS.
		dir := filepath.Join(cacheDir, name)
		os.MkdirAll(dir, 0o755)
		short := dgst.Hash
		if len(short) > 12 {
			short = short[:12]
		}
		destPath := filepath.Join(dir, "plugin-"+short+ext)

		if _, statErr := os.Stat(destPath); statErr != nil {
			rc, getErr := store.Get(context.Background(), dgst)
			if getErr != nil {
				continue
			}
			f, createErr := os.OpenFile(destPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
			if createErr != nil {
				rc.Close()
				continue
			}
			io.Copy(f, rc)
			f.Close()
			rc.Close()
		}

		if strings.HasSuffix(destPath, ".bb") {
			needsBB = true
		}

		plugins = append(plugins, pluginToDiscover{
			name:   name,
			digest: dgst,
			cached: wasCached,
			path:   destPath,
		})
	}

	// Start plugins and run discover.
	mgr := plugin.NewManager(projectRoot)

	if needsBB {
		bbPath := resolveBbPath()
		if bbPath == "" {
			fmt.Fprintln(os.Stderr, "mu plugin list: --discover requires bb toolchain; run mu build first to bootstrap it")
			return 1
		}
		mgr.SetScriptRuntime(bbPath)
	}

	for _, p := range plugins {
		tc := inferPluginToolchain(p.path)
		if err := mgr.Register(plugin.PluginDef{
			Name:      p.name,
			Script:    p.path,
			Toolchain: tc,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
			return 1
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: starting plugins: %v\n", err)
		return 1
	}
	defer mgr.Close()

	type cachedDiscoverInfo struct {
		Name     string   `json:"name"`
		Version  string   `json:"version"`
		Digest   string   `json:"digest"`
		Cached   bool     `json:"cached"`
		Consumes []string `json:"consumes"`
		Produces []string `json:"produces"`
	}

	var items []cachedDiscoverInfo
	for _, p := range plugins {
		item := cachedDiscoverInfo{
			Name:   p.name,
			Digest: p.digest.String(),
			Cached: p.cached,
		}
		info := mgr.DiscoverInfo(p.name)
		if info != nil {
			item.Version = info.Version
			item.Consumes = info.Consumes
			item.Produces = info.Produces
		}
		items = append(items, item)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(items)
		return 0
	}

	fmt.Printf("%-15s %-8s %-7s %-25s %-25s %s\n", "PLUGIN", "VERSION", "CACHED", "CONSUMES", "PRODUCES", "DIGEST")
	for _, item := range items {
		consumes := "-"
		if len(item.Consumes) > 0 {
			consumes = fmt.Sprintf("%v", item.Consumes)
		}
		produces := "-"
		if len(item.Produces) > 0 {
			produces = fmt.Sprintf("%v", item.Produces)
		}
		cachedStr := "no"
		if item.Cached {
			cachedStr = "yes"
		}
		// Truncate digest for display.
		digest := item.Digest
		if len(digest) > 19 {
			digest = digest[:19] + "..."
		}
		fmt.Printf("%-15s %-8s %-7s %-25s %-25s %s\n", item.Name, item.Version, cachedStr, consumes, produces, digest)
	}
	return 0
}

func pluginListConfig(cfg *config.ProjectConfig, jsonOut bool) int {
	type pluginInfo struct {
		Name    string `json:"name"`
		Type    string `json:"type"` // "command", "script", "digest", or "url"
		Command string `json:"command,omitempty"`
		Script  string `json:"script,omitempty"`
		Digest  string `json:"digest,omitempty"`
		URL     string `json:"url,omitempty"`
	}

	var items []pluginInfo
	for _, p := range cfg.Plugins {
		item := pluginInfo{Name: p.Name}
		switch {
		case p.Script != "":
			item.Type = "script"
			item.Script = p.Script
		case p.Digest != "":
			item.Type = "digest"
			item.Digest = p.Digest
		case p.URL != "":
			item.Type = "url"
			item.URL = p.URL
		default:
			item.Type = "command"
			if len(p.Command) > 0 {
				item.Command = p.Command[0]
			}
		}
		items = append(items, item)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(items)
		return 0
	}

	fmt.Printf("%-20s %-10s %s\n", "PLUGIN", "TYPE", "REF")
	for _, item := range items {
		ref := item.Script
		if ref == "" {
			ref = item.Digest
		}
		if ref == "" {
			ref = item.URL
		}
		if ref == "" {
			ref = item.Command
		}
		fmt.Printf("%-20s %-10s %s\n", item.Name, item.Type, ref)
	}
	return 0
}

func pluginListDiscover(cfg *config.ProjectConfig, projectRoot string, jsonOut bool) int {
	mgr := plugin.NewManager(projectRoot)

	// Resolve bb if any plugin needs it.
	for _, p := range cfg.Plugins {
		tc := inferPluginToolchain(p.Script)
		if tc == "bb" {
			bbPath := resolveBbPath()
			if bbPath == "" {
				fmt.Fprintln(os.Stderr, "mu plugin list: --discover requires bb toolchain; run mu build first to bootstrap it")
				return 1
			}
			mgr.SetScriptRuntime(bbPath)
			break
		}
	}

	for _, p := range cfg.Plugins {
		def := plugin.PluginDef{
			Name:      p.Name,
			Command:   p.Command,
			Script:    p.Script,
			Toolchain: inferPluginToolchain(p.Script),
		}
		if err := mgr.Register(def); err != nil {
			fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
			return 1
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := mgr.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: starting plugins: %v\n", err)
		return 1
	}
	defer mgr.Close()

	type discoverInfo struct {
		Name     string   `json:"name"`
		Version  string   `json:"version"`
		Consumes []string `json:"consumes"`
		Produces []string `json:"produces"`
	}

	var items []discoverInfo
	for _, p := range cfg.Plugins {
		info := mgr.DiscoverInfo(p.Name)
		if info == nil {
			continue
		}
		items = append(items, discoverInfo{
			Name:     info.Name,
			Version:  info.Version,
			Consumes: info.Consumes,
			Produces: info.Produces,
		})
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(items)
		return 0
	}

	fmt.Printf("%-20s %-10s %-30s %s\n", "PLUGIN", "VERSION", "CONSUMES", "PRODUCES")
	for _, item := range items {
		consumes := "-"
		if len(item.Consumes) > 0 {
			consumes = fmt.Sprintf("%v", item.Consumes)
		}
		produces := "-"
		if len(item.Produces) > 0 {
			produces = fmt.Sprintf("%v", item.Produces)
		}
		fmt.Printf("%-20s %-10s %-30s %s\n", item.Name, item.Version, consumes, produces)
	}
	return 0
}

// inferPluginToolchain infers the runtime toolchain from a plugin's file
// extension. Returns "bb" for .bb files, empty string for direct execution.
func inferPluginToolchain(path string) string {
	if strings.HasSuffix(path, ".bb") {
		return "bb"
	}
	return ""
}

// resolveBbPath finds the cached bb binary. Returns "" if not found.
func resolveBbPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".mu", "toolchains", "bb", "bb")
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return ""
}

// buildTargets sets up a Coordinator and builds the given targets.
func buildTargets(projectRoot string, cfg *config.ProjectConfig, targets []string) (*coordinator.BuildResult, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolving home directory: %w", err)
	}

	cachePath := filepath.Join(home, ".mu", "cache")
	store, err := oci.NewLocal(cachePath)
	if err != nil {
		return nil, fmt.Errorf("creating cache store: %w", err)
	}

	registry := coordinator.NewToolchainRegistry(store)

	c := &coordinator.Coordinator{
		ProjectRoot:       projectRoot,
		Config:            cfg,
		Store:             store,
		ToolchainRegistry: registry,
	}

	if len(cfg.Toolchains) > 0 {
		c.Builder = &scratch.Builder{
			Store:    store,
			Registry: registry,
			CacheDir: cachePath,
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	return c.Build(ctx, targets)
}

// extractPluginDigest finds the plugin output digest from a build result.
// It looks for the first output ending in "-plugin.bb" from actions matching
// the target prefix (convention: //plugins/<name> produces <name>-plugin.bb).
func extractPluginDigest(result *coordinator.BuildResult, targetName string) (cas.Digest, error) {
	prefix := targetName + ":"
	for _, s := range result.ExecResult.Completed {
		if strings.HasPrefix(s.ID, prefix) {
			for name, d := range s.Outputs {
				if strings.HasSuffix(name, "-plugin.bb") {
					return d, nil
				}
			}
		}
	}
	return cas.Digest{}, fmt.Errorf("no plugin output found for target %s", targetName)
}

// updateMuCuePlugin edits the root mu.cue to add or replace a plugin
// entry with the given digest. The edit operates on the CUE AST: if a
// `plugins` list field exists with an entry whose `name` matches, its
// `digest` field is set (replacing any prior value); otherwise a new
// entry is appended. If no `plugins` field exists yet, one is created.
func updateMuCuePlugin(projectRoot, pluginName, digest string) error {
	path := filepath.Join(projectRoot, "mu.cue")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading mu.cue: %w", err)
	}

	file, err := parser.ParseFile(path, data, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parsing mu.cue: %w", err)
	}

	pluginsField := findTopField(file, "plugins")
	newEntry := pluginEntryStruct(pluginName, digest)

	if pluginsField == nil {
		// Append a fresh `plugins: [...]` declaration at file end.
		file.Decls = append(file.Decls, &ast.Field{
			Label: ast.NewIdent("plugins"),
			Value: &ast.ListLit{Elts: []ast.Expr{newEntry}},
		})
	} else {
		list, ok := pluginsField.Value.(*ast.ListLit)
		if !ok {
			return fmt.Errorf("mu.cue: top-level `plugins` is not a list literal")
		}
		replaced := false
		for i, elt := range list.Elts {
			s, ok := elt.(*ast.StructLit)
			if !ok {
				continue
			}
			if cueFieldStringValue(s, "name") != pluginName {
				continue
			}
			list.Elts[i] = mergeDigestField(s, digest)
			replaced = true
			break
		}
		if !replaced {
			list.Elts = append(list.Elts, newEntry)
		}
	}

	out, err := format.Node(file, format.Simplify())
	if err != nil {
		return fmt.Errorf("formatting mu.cue: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("writing mu.cue: %w", err)
	}
	return nil
}

// findTopField returns the first top-level *ast.Field with the given
// identifier label, or nil if none exists.
func findTopField(file *ast.File, name string) *ast.Field {
	for _, d := range file.Decls {
		f, ok := d.(*ast.Field)
		if !ok {
			continue
		}
		if id, ok := f.Label.(*ast.Ident); ok && id.Name == name {
			return f
		}
	}
	return nil
}

// cueFieldStringValue returns the string-literal value of the named field
// inside s, or "" if it is missing or not a basic string literal.
func cueFieldStringValue(s *ast.StructLit, name string) string {
	for _, e := range s.Elts {
		f, ok := e.(*ast.Field)
		if !ok {
			continue
		}
		var label string
		switch lbl := f.Label.(type) {
		case *ast.Ident:
			label = lbl.Name
		case *ast.BasicLit:
			if lbl.Kind == token.STRING {
				label = strings.Trim(lbl.Value, `"`)
			}
		}
		if label != name {
			continue
		}
		if lit, ok := f.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return strings.Trim(lit.Value, `"`)
		}
	}
	return ""
}

// pluginEntryStruct builds an AST struct literal {name: <name>, digest: <digest>}.
func pluginEntryStruct(name, digest string) *ast.StructLit {
	return &ast.StructLit{
		Elts: []ast.Decl{
			&ast.Field{
				Label: ast.NewIdent("name"),
				Value: ast.NewString(name),
			},
			&ast.Field{
				Label: ast.NewIdent("digest"),
				Value: ast.NewString(digest),
			},
		},
	}
}

// mergeDigestField returns a copy of s with its `digest` field set to the
// given value. An existing `digest` field is replaced in place; otherwise
// a new one is appended.
func mergeDigestField(s *ast.StructLit, digest string) *ast.StructLit {
	for i, e := range s.Elts {
		f, ok := e.(*ast.Field)
		if !ok {
			continue
		}
		id, ok := f.Label.(*ast.Ident)
		if !ok || id.Name != "digest" {
			continue
		}
		s.Elts[i] = &ast.Field{
			Label: ast.NewIdent("digest"),
			Value: ast.NewString(digest),
		}
		return s
	}
	s.Elts = append(s.Elts, &ast.Field{
		Label: ast.NewIdent("digest"),
		Value: ast.NewString(digest),
	})
	return s
}
