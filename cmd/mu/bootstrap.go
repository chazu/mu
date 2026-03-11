package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/chau/mu/internal/bootstrap"
	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/disk"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
)

func runBootstrap(args []string) int {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	_ = fs.Bool("verbose", false, "show plugin I/O")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// Find project root.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu bootstrap: %v\n", err)
		return 2
	}
	projectRoot, err := config.FindProjectRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu bootstrap: %v\n", err)
		return 2
	}

	// Load and validate config.
	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu bootstrap: %v\n", err)
		return 2
	}
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "mu bootstrap: %v\n", err)
		return 2
	}

	if len(cfg.Toolchains) == 0 {
		fmt.Fprintln(os.Stderr, "mu bootstrap: no toolchains defined")
		return 0
	}

	// Create CAS store.
	var store cas.Store
	if !*noCache {
		home, err := os.UserHomeDir()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu bootstrap: resolving home directory: %v\n", err)
			return 2
		}
		cachePath := filepath.Join(home, ".mu", "cache")
		ds, err := disk.New(cachePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu bootstrap: creating cache store: %v\n", err)
			return 2
		}
		store = ds
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(os.Stderr, "mu bootstrap: %d toolchain(s)\n", len(cfg.Toolchains))

	start := time.Now()

	// Check MU_BOOTSTRAP env var for external plugin override.
	if ext := os.Getenv("MU_BOOTSTRAP"); ext != "" {
		fmt.Fprintf(os.Stderr, "  using external bootstrap: %s\n", ext)
		if err := bootstrap.External(ctx, ext, projectRoot, cfg, store); err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			return 1
		}
	} else {
		registry := coordinator.NewToolchainRegistry(store)
		home, _ := os.UserHomeDir()
		b := &bootstrap.Bootstrapper{
			Store:    store,
			Registry: registry,
			CacheDir: filepath.Join(home, ".mu", "cache"),
		}
		if err := b.Bootstrap(ctx, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			return 1
		}
	}

	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "  ✓ bootstrap complete in %.1fs\n", elapsed.Seconds())
	return 0
}
