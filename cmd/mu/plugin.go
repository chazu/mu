package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/plugin"
)

func runPlugin(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: mu plugin <command>

Commands:
  list      List registered plugins`)
		return 2
	}

	switch args[0] {
	case "list":
		return runPluginList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mu plugin: unknown command %q\n", args[0])
		return 2
	}
}

func runPluginList(args []string) int {
	fs := flag.NewFlagSet("plugin list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFile := fs.String("config", "", "path to mu.json")
	discover := fs.Bool("discover", false, "start plugins and run discover to show capabilities")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var projectRoot string
	if *configFile != "" {
		absConfig, err := filepath.Abs(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
			return 2
		}
		projectRoot = filepath.Dir(absConfig)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
			return 2
		}
		projectRoot, err = config.FindProjectRoot(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
			return 2
		}
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu plugin list: %v\n", err)
		return 2
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
	// We need the bb runtime if any plugin uses script.
	mgr := plugin.NewManager(projectRoot)

	// Resolve bb if needed.
	for _, p := range cfg.Plugins {
		if p.Script != "" {
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
		if err := mgr.Register(plugin.PluginDef{
			Name:    p.Name,
			Command: p.Command,
			Script:  p.Script,
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
