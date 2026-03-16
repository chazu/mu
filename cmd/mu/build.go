package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/chau/mu/internal/scratch"
	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/oci"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
)

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jobs := fs.Int("jobs", 0, "max parallel actions (0 = NumCPU)")
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	configFile := fs.String("config", "", "path to mu.json (default: discover by walking up)")
	_ = fs.Bool("verbose", false, "show plugin I/O")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	targets := fs.Args()
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "mu build: no targets specified")
		return 2
	}

	// Find project root and load config.
	var projectRoot string
	if *configFile != "" {
		// Explicit config: project root is the directory containing the config file.
		absConfig, err := filepath.Abs(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
			return 2
		}
		projectRoot = filepath.Dir(absConfig)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
			return 2
		}
		projectRoot, err = config.FindProjectRoot(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
			return 2
		}
	}

	// Load and validate config.
	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
		return 2
	}
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
		return 2
	}

	// Create CAS store.
	var store cas.Store
	if !*noCache {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: resolving home directory: %v\n", err)
			return 2
		}
		cachePath := filepath.Join(home, ".mu", "cache")
		ds, err := oci.NewLocal(cachePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: creating cache store: %v\n", err)
			return 2
		}
		store = ds
	}

	// Build with cancellation on SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	registry := coordinator.NewToolchainRegistry(store)
	home, _ := os.UserHomeDir()

	c := &coordinator.Coordinator{
		ProjectRoot:       projectRoot,
		Config:            cfg,
		Store:             store,
		ToolchainRegistry: registry,
		Workers:           *jobs,
	}

	if len(cfg.Toolchains) > 0 && store != nil {
		c.Builder = &scratch.Builder{
			Store:    store,
			Registry: registry,
			CacheDir: filepath.Join(home, ".mu", "cache"),
		}
	}

	fmt.Fprintf(os.Stderr, "mu build %s\n", strings.Join(targets, " "))
	fmt.Fprintln(os.Stderr, "  building...")

	start := time.Now()
	result, err := c.Build(ctx, targets)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Fprintf(os.Stderr, "  error: %v\n", err)
		return 1
	}

	if result.Failed > 0 {
		fmt.Fprintf(os.Stderr, "  \u2717 %d failed, %d cancelled\n", result.Failed, result.Cancelled)
		return 1
	}

	total := result.Completed + result.Cached
	fmt.Fprintf(os.Stderr, "  \u2713 %d completed (%d cached), %d failed in %.1fs\n",
		total, result.Cached, result.Failed, elapsed.Seconds())
	return 0
}
