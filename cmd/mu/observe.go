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
			switch r.State {
			case "converged":
				fmt.Printf("  %s\tconverged\n", r.Target)
			case "drifted":
				fmt.Printf("  %s\tdrifted\n", r.Target)
				if r.Diff != "" {
					for _, line := range strings.Split(r.Diff, "\n") {
						if line != "" {
							fmt.Printf("    %s\n", line)
						}
					}
				}
			default:
				fmt.Printf("  %s\tunknown\n", r.Target)
			}
		}
	}

	// Count results.
	var converged, drifted, unknown int
	for _, r := range results {
		switch r.State {
		case "converged":
			converged++
		case "drifted":
			drifted++
		default:
			unknown++
		}
	}

	if !*jsonOut {
		parts := []string{}
		if converged > 0 {
			parts = append(parts, fmt.Sprintf("%d converged", converged))
		}
		if drifted > 0 {
			parts = append(parts, fmt.Sprintf("%d drifted", drifted))
		}
		if unknown > 0 {
			parts = append(parts, fmt.Sprintf("%d unknown", unknown))
		}
		fmt.Fprintf(os.Stderr, "\n  %s\n", strings.Join(parts, ", "))
	}

	// Exit 1 if any target is drifted.
	if drifted > 0 {
		return 1
	}
	return 0
}
