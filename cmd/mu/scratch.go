package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/chazu/mu/internal/coordinator"
)

func runScratch(args []string) int {
	fs := flag.NewFlagSet("scratch", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("scratch", fs, useConfig, useOffline)
	defer cli.Close()
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	if code, ok := parseFlags(fs, args, "scratch [flags]"); !ok {
		return code
	}
	if code, ok := cli.Resolve(resolveOpts{
		NeedConfig:     true,
		NeedStore:      true,
		ValidateConfig: true,
	}); !ok {
		return code
	}

	if len(cli.Config.Toolchains) == 0 {
		fmt.Fprintln(os.Stderr, "mu scratch: no toolchains defined")
		return exitOK
	}

	ctx, stop := signal.NotifyContext(cli.networkContext(context.Background()), os.Interrupt)
	defer stop()

	fmt.Fprintf(os.Stderr, "mu scratch: %d toolchain(s)\n", len(cli.Config.Toolchains))

	start := time.Now()

	b := newScratchBuilder(cli, coordinator.NewToolchainRegistry(cli.Store), *noCache)
	if err := b.Build(ctx, cli.Config); err != nil {
		return cli.fail(exitFail, "%v", err)
	}

	elapsed := time.Since(start)
	fmt.Fprintf(os.Stderr, "  ✓ scratch build complete in %.1fs\n", elapsed.Seconds())
	return exitOK
}
