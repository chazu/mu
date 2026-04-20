package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chau/mu/internal/config"
)

// runPluginStatus reconciles plugins declared in mu.json against plugins
// materialized under ~/.mu/plugins. Each plugin lands in one of:
//
//   ok      — declared with a digest and that digest is present in the cache
//   missing — declared with a digest but the cache has no matching blob
//   stale   — cached blob(s) exist for this plugin, but none match the
//             declared digest (e.g. a digest was bumped in mu.json)
//   local   — declared as a local script / command / url; no cache check
func runPluginStatus(args []string) int {
	fs := flag.NewFlagSet("plugin status", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFile := fs.String("config", "", "path to mu.json")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	projectRoot, err := resolveProjectRoot(*configFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin status: %v\n", err)
		return 2
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin status: %v\n", err)
		return 2
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin status: %v\n", err)
		return 1
	}
	pluginsDir := filepath.Join(home, ".mu", "plugins")

	type statusEntry struct {
		Name          string `json:"name"`
		Declared      string `json:"declared"`           // digest / script path / url / command
		DeclaredKind  string `json:"declared_kind"`      // digest | script | url | command
		CachedDigest  string `json:"cached_digest,omitempty"`
		State         string `json:"state"`              // ok | missing | stale | local
	}

	var items []statusEntry
	anyNotOK := false

	for _, p := range cfg.Plugins {
		entry := statusEntry{Name: p.Name}
		switch {
		case p.Digest != "":
			entry.DeclaredKind = "digest"
			entry.Declared = p.Digest
			cached := cachedDigestsFor(pluginsDir, p.Name)
			switch {
			case contains(cached, p.Digest):
				entry.State = "ok"
				entry.CachedDigest = p.Digest
			case len(cached) == 0:
				entry.State = "missing"
				anyNotOK = true
			default:
				entry.State = "stale"
				entry.CachedDigest = cached[0]
				anyNotOK = true
			}
		case p.Script != "":
			entry.DeclaredKind = "script"
			entry.Declared = p.Script
			entry.State = "local"
		case p.URL != "":
			entry.DeclaredKind = "url"
			entry.Declared = p.URL
			entry.State = "local"
		default:
			entry.DeclaredKind = "command"
			if len(p.Command) > 0 {
				entry.Declared = p.Command[0]
			}
			entry.State = "local"
		}
		items = append(items, entry)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(items)
		if anyNotOK {
			return 1
		}
		return 0
	}

	if len(items) == 0 {
		fmt.Println("No plugins defined.")
		return 0
	}

	fmt.Printf("%-20s %-8s %-10s %s\n", "PLUGIN", "KIND", "STATE", "DECLARED")
	for _, it := range items {
		fmt.Printf("%-20s %-8s %-10s %s\n", it.Name, it.DeclaredKind, it.State, shorten(it.Declared, 56))
	}
	if anyNotOK {
		return 1
	}
	return 0
}

// cachedDigestsFor returns the set of sha256:... digests materialized under
// ~/.mu/plugins/<name>/ (both bundle-* and plugin-* layouts).
func cachedDigestsFor(pluginsDir, name string) []string {
	dir := filepath.Join(pluginsDir, name)
	var out []string

	if bundles, err := filepath.Glob(filepath.Join(dir, "bundle-*")); err == nil {
		for _, b := range bundles {
			base := filepath.Base(b)
			out = append(out, "sha256:"+strings.TrimPrefix(base, "bundle-"))
		}
	}
	if singles, err := filepath.Glob(filepath.Join(dir, "plugin-*")); err == nil {
		for _, s := range singles {
			base := filepath.Base(s)
			base = strings.TrimPrefix(base, "plugin-")
			if idx := strings.IndexByte(base, '.'); idx >= 0 {
				base = base[:idx]
			}
			if base != "" {
				out = append(out, "sha256:"+base)
			}
		}
	}
	return out
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
