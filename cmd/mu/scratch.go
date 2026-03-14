package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/oci"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
	"github.com/chau/mu/internal/scratch"
)

func runScratch(args []string) int {
	fs := flag.NewFlagSet("scratch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	_ = fs.Bool("verbose", false, "show plugin I/O")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Find project root.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu scratch: %v\n", err)
		return 2
	}
	projectRoot, err := config.FindProjectRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu scratch: %v\n", err)
		return 2
	}

	// Load and validate config.
	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu scratch: %v\n", err)
		return 2
	}
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "mu scratch: %v\n", err)
		return 2
	}

	if len(cfg.Toolchains) == 0 {
		fmt.Fprintln(os.Stderr, "mu scratch: no toolchains defined")
		return 0
	}

	// Create CAS store.
	var store cas.Store
	if !*noCache {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu scratch: resolving home directory: %v\n", err)
			return 2
		}
		cachePath := filepath.Join(home, ".mu", "cache")
		ds, err := oci.NewLocal(cachePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu scratch: creating cache store: %v\n", err)
			return 2
		}
		store = ds
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(os.Stderr, "mu scratch: %d toolchain(s)\n", len(cfg.Toolchains))

	start := time.Now()

	// Check MU_SCRATCH env var for external plugin override.
	if ext := os.Getenv("MU_SCRATCH"); ext != "" {
		fmt.Fprintf(os.Stderr, "  using external scratch build: %s\n", ext)
		if err := scratch.External(ctx, ext, projectRoot, cfg, store); err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			return 1
		}
	} else {
		registry := coordinator.NewToolchainRegistry(store)
		home, _ := os.UserHomeDir()
		b := &scratch.Builder{
			Store:    store,
			Registry: registry,
			CacheDir: filepath.Join(home, ".mu", "cache"),
		}
		if err := b.Build(ctx, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			return 1
		}
	}

	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "  ✓ scratch build complete in %.1fs\n", elapsed.Seconds())
	return 0
}
