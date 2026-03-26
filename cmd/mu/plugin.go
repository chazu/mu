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

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/oci"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
	"github.com/chau/mu/internal/plugin"
	"github.com/chau/mu/internal/scratch"
)

func runPlugin(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: mu plugin <command>

Commands:
  list      List registered plugins
  add       Add a plugin from cache by building its //plugins/<name> target`)
		return 2
	}

	switch args[0] {
	case "list":
		return runPluginList(args[1:])
	case "add":
		return runPluginAdd(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mu plugin: unknown command %q\n", args[0])
		return 2
	}
}

func runPluginAdd(args []string) int {
	fs := flag.NewFlagSet("plugin add", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFile := fs.String("config", "", "path to mu.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin add <name>")
		return 2
	}
	pluginName := fs.Arg(0)

	projectRoot, err := resolveProjectRoot(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin add: %v\n", err)
		return 2
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin add: %v\n", err)
		return 2
	}
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin add: %v\n", err)
		return 2
	}

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

	// Update mu.json with the digest-based plugin entry.
	if err := updateMuJSONPlugin(projectRoot, pluginName, dgst.String()); err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin add: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "plugin %q added with digest %s\n", pluginName, dgst)
	return 0
}

func runPluginList(args []string) int {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFile := fs.String("config", "", "path to mu.json")
	discover := fs.Bool("discover", false, "start plugins and run discover to show capabilities")
	cached := fs.Bool("cached", false, "show all //plugins/* targets with their CAS digests")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	projectRoot, err := resolveProjectRoot(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 2
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 2
	}

	if *cached && *discover {
		return pluginListCachedDiscover(cfg, projectRoot, *jsonOut)
	}

	if *cached {
		return pluginListCached(cfg, projectRoot, *jsonOut)
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

// pluginListCached finds all //plugins/<name> targets, builds them (hitting
// cache), and displays each plugin with its CAS digest.
func pluginListCached(cfg *config.ProjectConfig, projectRoot string, jsonOut bool) int {
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

	result, err := buildTargets(projectRoot, cfg, targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 1
	}

	type cachedInfo struct {
		Name   string `json:"name"`
		Digest string `json:"digest"`
		Cached bool   `json:"cached"`
	}

	var items []cachedInfo
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
		items = append(items, cachedInfo{
			Name:   name,
			Digest: dgst.String(),
			Cached: wasCached,
		})
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(items)
		return 0
	}

	fmt.Printf("%-20s %-7s %s\n", "PLUGIN", "CACHED", "DIGEST")
	for _, item := range items {
		cachedStr := "no"
		if item.Cached {
			cachedStr = "yes"
		}
		fmt.Printf("%-20s %-7s %s\n", item.Name, cachedStr, item.Digest)
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
		isBB := strings.HasSuffix(p.path, ".bb")
		if err := mgr.Register(plugin.PluginDef{
			Name:         p.name,
			Script:       p.path,
			NeedsRuntime: isBB,
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

	// Resolve bb if any plugin needs it (based on .bb extension or explicit runtime).
	for _, p := range cfg.Plugins {
		needsBB := p.Runtime == "bb" || (p.Runtime == "" && p.Script != "" && strings.HasSuffix(p.Script, ".bb"))
		if needsBB {
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
		needsBB := p.Script != "" && strings.HasSuffix(p.Script, ".bb")
		if p.Runtime == "bb" {
			needsBB = true
		} else if p.Runtime == "none" {
			needsBB = false
		}
		def := plugin.PluginDef{
			Name:         p.Name,
			Command:      p.Command,
			Script:       p.Script,
			NeedsRuntime: needsBB,
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

// resolveProjectRoot finds the project root from a --config flag or cwd.
func resolveProjectRoot(configFile string) (string, error) {
	if configFile != "" {
		absConfig, err := filepath.Abs(configFile)
		if err != nil {
			return "", err
		}
		return filepath.Dir(absConfig), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return config.FindProjectRoot(cwd)
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

// updateMuJSONPlugin edits the root mu.json to add or replace a plugin entry
// with the given digest. It does surgical text replacement to preserve the
// existing key ordering and formatting.
func updateMuJSONPlugin(projectRoot, pluginName, digest string) error {
	path := filepath.Join(projectRoot, "mu.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading mu.json: %w", err)
	}

	text := string(data)

	// Format the new entry to match hand-written style.
	newEntry := fmt.Sprintf(`{"name": %q, "digest": %q}`, pluginName, digest)

	// Try to find and replace an existing plugin entry with this name.
	// Look for {"name": "<pluginName>", ...} or {"name": "<pluginName>"} patterns.
	// We search for the opening { of the entry containing the name.
	replaced := false
	namePattern := fmt.Sprintf(`"name": %q`, pluginName)
	nameAlt := fmt.Sprintf(`"name":%q`, pluginName) // no space variant

	// Find the plugin entry by locating the name field, then finding the
	// enclosing {} braces.
	for _, pat := range []string{namePattern, nameAlt} {
		idx := strings.Index(text, pat)
		if idx < 0 {
			continue
		}
		// Walk backward to find the opening {.
		start := strings.LastIndex(text[:idx], "{")
		if start < 0 {
			continue
		}
		// Walk forward to find the closing }.
		end := strings.Index(text[idx:], "}")
		if end < 0 {
			continue
		}
		end += idx + 1 // absolute position past the }

		text = text[:start] + newEntry + text[end:]
		replaced = true
		break
	}

	if !replaced {
		// Append to the plugins array. Find the closing ] of "plugins": [...].
		pluginsIdx := strings.Index(text, `"plugins"`)
		if pluginsIdx < 0 {
			return fmt.Errorf("no \"plugins\" key found in mu.json")
		}
		// Find the [ after "plugins":
		bracketStart := strings.Index(text[pluginsIdx:], "[")
		if bracketStart < 0 {
			return fmt.Errorf("no plugins array found in mu.json")
		}
		bracketStart += pluginsIdx

		// Find the matching ].
		depth := 0
		bracketEnd := -1
		for i := bracketStart; i < len(text); i++ {
			switch text[i] {
			case '[':
				depth++
			case ']':
				depth--
				if depth == 0 {
					bracketEnd = i
					break
				}
			}
			if bracketEnd >= 0 {
				break
			}
		}
		if bracketEnd < 0 {
			return fmt.Errorf("unterminated plugins array in mu.json")
		}

		// Insert before the ]. Walk back past any whitespace/newlines to
		// find the last real content before the ].
		insertAt := bracketEnd
		for insertAt > bracketStart && (text[insertAt-1] == ' ' || text[insertAt-1] == '\n' || text[insertAt-1] == '\r' || text[insertAt-1] == '\t') {
			insertAt--
		}
		insertion := ",\n    " + newEntry + "\n  "
		text = text[:insertAt] + insertion + text[bracketEnd:]
	}

	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		return fmt.Errorf("writing mu.json: %w", err)
	}

	return nil
}
