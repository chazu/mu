package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/oci"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
	"github.com/chau/mu/internal/scratch"
)

func runObserve(args []string) int {
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFile := fs.String("config", "", "path to mu.json (default: discover by walking up)")
	jsonOut := fs.Bool("json", false, "output as JSON")
	_ = fs.Bool("verbose", false, "show plugin I/O")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	targets := fs.Args()
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "mu observe: no targets specified")
		return 2
	}

	// Find project root and load config.
	var projectRoot string
	if *configFile != "" {
		absConfig, err := filepath.Abs(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu observe: %v\n", err)
			return 2
		}
		projectRoot = filepath.Dir(absConfig)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu observe: %v\n", err)
			return 2
		}
		projectRoot, err = config.FindProjectRoot(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu observe: %v\n", err)
			return 2
		}
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu observe: %v\n", err)
		return 2
	}
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "mu observe: %v\n", err)
		return 2
	}

	// Create CAS store.
	var store cas.Store
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu observe: resolving home directory: %v\n", err)
		return 2
	}
	cachePath := filepath.Join(home, ".mu", "cache")
	ds, err := oci.NewLocal(cachePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu observe: creating cache store: %v\n", err)
		return 2
	}
	store = ds

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

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
			CacheDir: filepath.Join(home, ".mu", "cache"),
		}
	}

	fmt.Fprintf(os.Stderr, "mu observe %s\n", strings.Join(targets, " "))

	results, err := c.Observe(ctx, targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  error: %v\n", err)
		return 1
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(results)
	} else {
		for _, r := range results {
			if r.Error != "" {
				fmt.Printf("  %s\terror: %s\n", r.Target, r.Error)
			} else if r.Current != nil {
				currentJSON, _ := json.Marshal(r.Current)
				fmt.Printf("  %s\t%s\n", r.Target, string(currentJSON))
			} else {
				fmt.Printf("  %s\t(no observe data)\n", r.Target)
			}
		}
	}

	// Count errors.
	var errors int
	for _, r := range results {
		if r.Error != "" {
			errors++
		}
	}

	if !*jsonOut {
		fmt.Fprintf(os.Stderr, "\n  %d observed", len(results))
		if errors > 0 {
			fmt.Fprintf(os.Stderr, ", %d errors", errors)
		}
		fmt.Fprintln(os.Stderr)
	}

	if errors > 0 {
		return 1
	}
	return 0
}
