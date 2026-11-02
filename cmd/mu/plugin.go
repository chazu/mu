package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chazu/mu/internal/config"
)

func runPlugin(args []string) int {
	if len(args) == 0 || isHelpArg(args[0]) {
		w, code := dispatcherUsage(args)
		fmt.Fprintln(w, `usage: mu plugin <command>

Commands:
  list      List project plugins and installed plugin versions
  info      Show capabilities and metadata for a plugin (project or installed)
  install   Download, verify, bundle, and register a catalog plugin`)
		return code
	}

	switch args[0] {
	case "list":
		return runPluginList(args[1:])
	case "info":
		return runPluginInfo(args[1:])
	case "install":
		return runPluginInstall(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mu plugin: unknown command %q\n", args[0])
		return exitUsage
	}
}

// resolveOptionalProject loads the project config for commands that also
// work outside a project. An explicit --config, or a mu.cue discovered by
// walking up from the working directory, must load; a load error is
// returned (exit 1) rather than silently ignored. With neither, ProjectRoot
// and Config stay empty and the caller falls back to installed plugins.
func (c *cliContext) resolveOptionalProject() (int, bool) {
	if code, ok := c.Resolve(resolveOpts{}); !ok {
		return code, false
	}
	if c.configPath() == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return c.fail(exitFail, "%v", err), false
		}
		if _, err := config.FindProjectRoot(cwd); err != nil {
			return exitOK, true // outside any project
		}
	}
	root, code, ok := c.resolveProjectRoot()
	if !ok {
		return code, false
	}
	cfg, err := config.LoadOffline(root, c.Offline)
	if err != nil {
		return c.fail(exitFail, "%v", err), false
	}
	c.ProjectRoot = root
	c.Config = cfg
	return exitOK, true
}

// pluginListItem is one row of `mu plugin list`: a plugin declared in the
// project, a plugin only present under ~/.mu/plugins, or both.
type pluginListItem struct {
	Name     string `json:"name"`
	Declared bool   `json:"declared"`       // listed in the project's mu.cue
	Type     string `json:"type,omitempty"` // digest | script | url | command (declared only)
	Ref      string `json:"ref,omitempty"`  // the declared digest / script / url / command
	// State reconciles a declared plugin with the local plugin cache:
	// ok (declared digest installed), missing (nothing installed), stale
	// (other versions installed), or local (script/url/command; no check).
	State     string   `json:"state,omitempty"`
	Installed []string `json:"installed,omitempty"` // digests under ~/.mu/plugins/<name>
}

// runPluginList implements `mu plugin list`: the project's declared plugins
// (when run inside a project or with --config) plus every plugin installed
// under ~/.mu/plugins, in one table. Use `mu plugin info <name>` to start a
// plugin and see its discover response.
func runPluginList(args []string) int {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin list", fs, useConfig, useJSON, useOffline)
	if code, ok := parseFlags(fs, args, "plugin list [flags]"); !ok {
		return code
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin list [flags]")
		return exitUsage
	}
	if code, ok := cli.resolveOptionalProject(); !ok {
		return code
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	items, err := collectPluginList(cli.Config, filepath.Join(home, ".mu", "plugins"))
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}

	if cli.JSON {
		if items == nil {
			items = []pluginListItem{}
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(items); err != nil {
			return cli.fail(exitFail, "writing results: %v", err)
		}
		return exitOK
	}
	if len(items) == 0 {
		fmt.Println("No plugins declared or installed.")
		return exitOK
	}
	fmt.Printf("%-20s %-8s %-8s %-40s %s\n", "PLUGIN", "TYPE", "STATE", "REF", "INSTALLED")
	for _, it := range items {
		installed := make([]string, len(it.Installed))
		for i, d := range it.Installed {
			installed[i] = shorten(d, 19)
		}
		fmt.Printf("%-20s %-8s %-8s %-40s %s\n", it.Name, orDash(it.Type), orDash(it.State),
			orDash(shorten(it.Ref, 40)), orDash(strings.Join(installed, ",")))
	}
	return exitOK
}

// collectPluginList builds the list rows: declared plugins first, in
// mu.cue order, then installed-only plugins sorted by name. cfg may be nil
// (outside a project).
func collectPluginList(cfg *config.ProjectConfig, pluginsDir string) ([]pluginListItem, error) {
	installed, err := installedPluginDigests(pluginsDir)
	if err != nil {
		return nil, err
	}
	var items []pluginListItem
	declared := map[string]bool{}
	if cfg != nil {
		for _, p := range cfg.Plugins {
			declared[p.Name] = true
			it := pluginListItem{Name: p.Name, Declared: true, Installed: installed[p.Name], State: "local"}
			switch {
			case p.Digest != "":
				it.Type, it.Ref = "digest", p.Digest
				switch {
				case contains(it.Installed, p.Digest):
					it.State = "ok"
				case len(it.Installed) == 0:
					it.State = "missing"
				default:
					it.State = "stale"
				}
			case p.Script != "":
				it.Type, it.Ref = "script", p.Script
			case p.URL != "":
				it.Type, it.Ref = "url", p.URL
			default:
				it.Type = "command"
				it.Ref = strings.Join(p.Command, " ")
			}
			items = append(items, it)
		}
	}
	names := make([]string, 0, len(installed))
	for name := range installed {
		if !declared[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	for _, name := range names {
		items = append(items, pluginListItem{Name: name, Installed: installed[name]})
	}
	return items, nil
}

// installedPluginDigests maps each plugin name under pluginsDir to the
// distinct digests materialized there (bundle and single-file layouts).
func installedPluginDigests(pluginsDir string) (map[string][]string, error) {
	entries, err := os.ReadDir(pluginsDir)
	if os.IsNotExist(err) {
		return map[string][]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]string{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		versions, err := cachedPluginVersionsIn(pluginsDir, e.Name())
		if errors.Is(err, errCachedPluginMissing) {
			continue
		}
		if err != nil {
			return nil, err
		}
		var digests []string
		for _, v := range versions {
			d := v.digest.String()
			if !contains(digests, d) {
				digests = append(digests, d)
			}
		}
		out[e.Name()] = digests
	}
	return out, nil
}

func contains(ss []string, needle string) bool {
	for _, s := range ss {
		if s == needle {
			return true
		}
	}
	return false
}

func shorten(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
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
