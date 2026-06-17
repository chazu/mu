package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/scratch"
)

func runScratch(args []string) int {
	fs := flag.NewFlagSet("scratch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("scratch", fs)
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if code, ok := cli.Resolve(resolveOpts{
		NeedConfig:     true,
		NeedStore:      true,
		NoCache:        *noCache,
		ValidateConfig: true,
	}); !ok {
		return code
	}

	if len(cli.Config.Toolchains) == 0 {
		fmt.Fprintln(os.Stderr, "mu scratch: no toolchains defined")
		return exitOK
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Fprintf(os.Stderr, "mu scratch: %d toolchain(s)\n", len(cli.Config.Toolchains))

	start := time.Now()

	if ext := os.Getenv("MU_SCRATCH"); ext != "" {
		fmt.Fprintf(os.Stderr, "  using external scratch build: %s\n", ext)
		if err := scratch.External(ctx, ext, cli.ProjectRoot, cli.Config, cli.Store); err != nil {
			return cli.fail(exitFail, "%v", err)
		}
	} else {
		registry := coordinator.NewToolchainRegistry(cli.Store)
		home, _ := os.UserHomeDir()
		b := &scratch.Builder{
			Store:    cli.Store,
			Registry: registry,
			CacheDir: filepath.Join(home, ".mu", "cache"),
		}
		if err := b.Build(ctx, cli.Config); err != nil {
			return cli.fail(exitFail, "%v", err)
		}
	}

	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "  ✓ scratch build complete in %.1fs\n", elapsed.Seconds())
	return exitOK
}
