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

	"github.com/chau/mu/internal/coordinator"
	"github.com/chau/mu/internal/scratch"
)

func runObserve(args []string) int {
	fs := flag.NewFlagSet("observe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("observe", fs)
	ndjsonOut := fs.Bool("ndjson", false, "output current.records as NDJSON (one record per line)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	targets := fs.Args()
	if len(targets) == 0 {
		return cli.fail(exitUsage, "no targets specified")
	}

	if code, ok := cli.Resolve(resolveOpts{
		NeedConfig:     true,
		NeedStore:      true,
		ValidateConfig: true,
	}); !ok {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	registry := coordinator.NewToolchainRegistry(cli.Store)

	c := &coordinator.Coordinator{
		ProjectRoot:       cli.ProjectRoot,
		Config:            cli.Config,
		Store:             cli.Store,
		ToolchainRegistry: registry,
		Verbose:           cli.Verbose,
	}

	if len(cli.Config.Toolchains) > 0 {
		home, _ := os.UserHomeDir()
		c.Builder = &scratch.Builder{
			Store:    cli.Store,
			Registry: registry,
			CacheDir: filepath.Join(home, ".mu", "cache"),
		}
	}

	fmt.Fprintf(os.Stderr, "mu observe %s\n", strings.Join(targets, " "))

	results, err := c.Observe(ctx, targets)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}

	switch {
	case *ndjsonOut:
		enc := json.NewEncoder(os.Stdout)
		for _, r := range results {
			if r.Current == nil {
				continue
			}
			if records, ok := r.Current["records"]; ok {
				if arr, ok := records.([]interface{}); ok {
					for _, rec := range arr {
						enc.Encode(rec)
					}
				}
			}
		}
	case cli.JSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(results)
	default:
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

	var errors int
	for _, r := range results {
		if r.Error != "" {
			errors++
		}
	}

	if !cli.JSON {
		fmt.Fprintf(os.Stderr, "\n  %d observed", len(results))
		if errors > 0 {
			fmt.Fprintf(os.Stderr, ", %d errors", errors)
		}
		fmt.Fprintln(os.Stderr)
	}

	if errors > 0 {
		return exitFail
	}
	return exitOK
}
