package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/dag"
)

// runCachePush publishes the local cache results of the named targets
// (default //...) to the repository configured as cache.push in mu.cue.
//
// The targets are planned, not built, and only results for their actions are
// pushed, so other projects sharing the local cache never leak into this
// project's registry. A downstream action's key depends on its producers'
// outputs, so keys are derived in dependency order from the local results
// (dag.CachedResults); actions without a local result are reported and
// skipped. Each result is pushed independently: entries the registry already
// has are skipped, and a failed entry does not stop the rest. The exit status
// is non-zero if any entry failed.
func runCachePush(args []string) int {
	fs := flag.NewFlagSet("cache push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("cache push", fs, useConfig, useVerbose, useOffline)
	defer c.Close()
	dryRun := fs.Bool("dry-run", false, "list the action results that would be pushed, without pushing")
	if code, ok := parseFlags(fs, args, "cache push [flags] [target...]"); !ok {
		return code
	}
	targets := fs.Args()
	if len(targets) == 0 {
		targets = []string{"//..."}
	}

	if code, ok := c.Resolve(resolveOpts{NeedConfig: true, ValidateConfig: true, NeedStore: true}); !ok {
		return code
	}
	ref, code, ok := resolvePushRef(c)
	if !ok {
		return code
	}
	if !*dryRun && !c.requireRegistry() {
		return exitUsage
	}
	local := localDiskStore(c.Store)
	if local == nil {
		return c.fail(exitFail, "no local cache store")
	}

	ctx := c.networkContext(context.Background())
	plan, err := newCoordinator(c, coordinatorOpts{}).Plan(ctx, targets)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	found, missing, err := dag.CachedResults(ctx, plan.Graph, local.GetActionResult)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	found = uniqueResults(found)
	if c.Verbose {
		for _, id := range missing {
			fmt.Fprintf(os.Stderr, "  %s: no local result\n", id)
		}
	}

	if *dryRun {
		fmt.Printf("Would push %d action result(s) to %s:\n", len(found), ref)
		for _, r := range found {
			fmt.Printf("  %s  %s\n", oci.ActionTag(r.Key), r.ActionID)
		}
		printNotBuilt(len(missing))
		return exitOK
	}

	repo, err := oci.NewRepository(ref)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	remote := oci.New(repo)

	var pushed, present, failed int
	for _, r := range found {
		tag := oci.ActionTag(r.Key)
		exists, err := remote.HasActionResult(ctx, r.Key)
		if err == nil && !exists {
			err = remote.PushActionResult(ctx, local, r.Key, r.Result)
		}
		switch {
		case ctx.Err() != nil:
			return c.fail(exitFail, "%v", ctx.Err())
		case err != nil:
			fmt.Fprintf(os.Stderr, "  %s (%s): %v\n", r.ActionID, tag, err)
			failed++
		case exists:
			present++
		default:
			if c.Verbose {
				fmt.Printf("  pushed %s (%s)\n", r.ActionID, tag)
			}
			pushed++
		}
	}

	fmt.Printf("Pushed %d, skipped %d (already present), failed %d of %d action result(s) to %s\n",
		pushed, present, failed, len(found), ref)
	printNotBuilt(len(missing))
	if failed > 0 {
		return exitFail
	}
	return exitOK
}

func printNotBuilt(n int) {
	if n > 0 {
		fmt.Printf("%d planned action(s) have no local result (not built here); not pushed\n", n)
	}
}

// uniqueResults drops repeated action keys (identical actions planned twice),
// keeping the first.
func uniqueResults(found []dag.CachedResult) []dag.CachedResult {
	seen := make(map[cas.ActionKey]bool, len(found))
	out := found[:0]
	for _, r := range found {
		if !seen[r.Key] {
			seen[r.Key] = true
			out = append(out, r)
		}
	}
	return out
}

// localDiskStore returns the local disk store behind the build cache.
func localDiskStore(store cas.Store) *cas.DiskStore {
	switch s := store.(type) {
	case *cas.DiskStore:
		return s
	case *cas.Cache:
		return s.Local
	}
	return nil
}

// resolvePushRef returns cache.push ("host/namespace/repo") from the loaded
// config, or a user-actionable error if it is missing.
func resolvePushRef(c *cliContext) (string, int, bool) {
	if c.Config == nil || c.Config.Cache == nil || c.Config.Cache.Push == "" {
		return "", c.fail(exitUsage,
			`cache.push is not configured in mu.cue — add:
  cache: { push: "<host>/<namespace>/<repo>" }`), false
	}
	return c.Config.Cache.Push, exitOK, true
}
