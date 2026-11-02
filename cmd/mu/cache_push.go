package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/chazu/mu/internal/cas/oci"
	"oras.land/oras-go/v2/registry/remote"
)

// runCachePush publishes every cached action result (and its output blobs)
// from the local disk cache at ~/.mu/cache to the remote OCI repository
// configured as cache.push in mu.cue. Each per-key local receipt becomes an
// OCI manifest tagged action-sha256-<key> on the remote, built at push time.
// Blobs already present remotely are not uploaded again.
func runCachePush(args []string) int {
	fs := flag.NewFlagSet("cache push", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	c := newCLIContext("cache push", fs)
	dryRun := fs.Bool("dry-run", false, "list action tags that would be pushed, without pushing")
	if code, ok := parseFlags(fs, args, "cache push [flags]"); !ok {
		return code
	}

	if code, ok := c.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}

	ref, code, ok := resolvePushRef(c)
	if !ok {
		return code
	}

	cacheDir := c.CachePath()
	local, err := openLocalCache(cacheDir)
	if err != nil {
		return c.fail(exitFail, "opening local cache %s: %v", cacheDir, err)
	}
	actions, err := readCachedActions(local, false)
	if err != nil {
		return c.fail(exitFail, "reading local cache: %v", err)
	}
	tags := make([]string, 0, len(actions))
	for _, a := range actions {
		tags = append(tags, a.Tag)
	}

	if len(tags) == 0 {
		fmt.Println("No cached actions to push.")
		return exitOK
	}

	if *dryRun {
		fmt.Printf("Would push %d action(s) to %s:\n", len(tags), ref)
		for _, t := range tags {
			fmt.Printf("  %s\n", t)
		}
		return exitOK
	}

	if !c.requireRegistry() {
		return exitUsage
	}

	ctx := c.networkContext(context.Background())

	remoteRepo, err := oci.NewRepository(ref)
	if err != nil {
		return c.fail(exitFail, "%v", err)
	}
	remote := oci.New(remoteRepo)

	var pushed, failed int
	for _, a := range actions {
		if err := remote.PushActionResult(ctx, local, a.Key, a.Result); err != nil {
			fmt.Fprintf(os.Stderr, "  %s: %v\n", a.Tag, err)
			failed++
			break
		}
		if c.Verbose {
			fmt.Printf("  pushed %s\n", a.Tag)
		}
		pushed++
	}

	fmt.Printf("Pushed %d/%d action(s) to %s", pushed, len(tags), ref)
	if failed > 0 {
		fmt.Printf(" (%d failed)", failed)
	}
	fmt.Println()
	if failed > 0 {
		return exitFail
	}
	return exitOK
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

// newPushRepository opens a registry repository through the shared
// constructor. It remains only for the OCI plugin commands, which are being
// removed (mu-x57.4); new code calls oci.NewRepository directly.
func newPushRepository(ref string) (*remote.Repository, error) {
	return oci.NewRepository(ref)
}
