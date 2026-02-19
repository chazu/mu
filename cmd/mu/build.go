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
	"github.com/chau/mu/internal/cas/disk"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
)

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ExitOnError)
	jobs := fs.Int("jobs", 0, "max parallel actions (0 = NumCPU)")
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	_ = fs.Bool("verbose", false, "show plugin I/O")
	fs.Parse(args)

	targets := fs.Args()
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "mu build: no targets specified")
		return 2
	}

	// Find project root.
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
		return 2
	}
	projectRoot, err := config.FindProjectRoot(cwd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
		return 2
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
		ds, err := disk.New(cachePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: creating cache store: %v\n", err)
			return 2
		}
		store = ds
	}

	// Build with cancellation on SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	c := &coordinator.Coordinator{
		ProjectRoot: projectRoot,
		Config:      cfg,
		Store:       store,
		Workers:     *jobs,
	}

	fmt.Fprintf(os.Stderr, "mu build %s\n", formatTargets(targets))
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

func formatTargets(targets []string) string {
	if len(targets) == 1 {
		return targets[0]
	}
	s := ""
	for i, t := range targets {
		if i > 0 {
			s += " "
		}
		s += t
	}
	return s
}
