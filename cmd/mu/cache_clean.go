package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chazu/mu/internal/staging"
)

// runCacheClean removes blobs from ~/.mu/cache that are not reachable from any
// action result under ~/.mu/cache/ac. Action results are the GC roots. If any
// root is unreadable, we abort rather than risk deleting a live blob.
func runCacheClean(args []string) int {
	fs := flag.NewFlagSet("cache clean", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dryRun := fs.Bool("dry-run", false, "show what would be removed without deleting")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cacheDir := cachePath()
	reachable, err := reachableBlobs(cacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache clean: %v\n", err)
		return 1
	}

	blobDir := filepath.Join(cacheDir, "blobs", "sha256")
	type removal struct {
		Digest string `json:"digest"`
		Size   int64  `json:"size"`
	}
	var removed []removal
	var totalBytes int64

	walkErr := filepath.Walk(blobDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		digest := "sha256:" + info.Name()
		if reachable[digest] {
			return nil
		}
		size := info.Size()
		removed = append(removed, removal{Digest: digest, Size: size})
		totalBytes += size
		if !*dryRun {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("removing %s: %w", digest, err)
			}
		}
		return nil
	})
	if walkErr != nil {
		fmt.Fprintf(os.Stderr, "mu cache clean: %v\n", walkErr)
		return 1
	}

	// Also reclaim staging stranded by crashed processes. The inactivity
	// threshold still applies, so concurrent runs are never disturbed.
	var staged []staging.Removal
	var stagedBytes int64
	if home, err := os.UserHomeDir(); err == nil {
		var sweepErr error
		staged, sweepErr = staging.Sweep(staging.DefaultRules(home, os.TempDir()), staging.MinAge, time.Now(), *dryRun)
		if sweepErr != nil {
			fmt.Fprintf(os.Stderr, "mu cache clean: staging: %v\n", sweepErr)
		}
		for _, s := range staged {
			stagedBytes += s.Bytes
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"dry_run":             *dryRun,
			"removed":             removed,
			"removed_count":       len(removed),
			"freed_bytes":         totalBytes,
			"freed":               formatSize(totalBytes),
			"staging_removed":     staged,
			"staging_freed_bytes": stagedBytes,
		})
		return 0
	}

	verb := "Removed"
	if *dryRun {
		verb = "Would remove"
	}
	fmt.Printf("%s %d unreachable blob(s), %s\n", verb, len(removed), formatSize(totalBytes))
	fmt.Printf("%s %d stale staging path(s), %s\n", verb, len(staged), formatSize(stagedBytes))
	return 0
}

// reachableBlobs opens the local cache and returns the set of blob digests
// transitively referenced by its action results (the GC roots). Toolchain
// manifest blobs are opened so that toolchain-artifact digests inside them are
// also reachable. Opening the store first migrates any legacy index.json, so
// results recorded by older releases stay roots.
func reachableBlobs(cacheDir string) (map[string]bool, error) {
	store, err := openLocalCache(cacheDir)
	if err != nil {
		return nil, err
	}
	actions, err := readCachedActions(store, true)
	if err != nil {
		return nil, fmt.Errorf("reading action results: %w", err)
	}
	reachable := make(map[string]bool)
	for _, a := range actions {
		for _, dgst := range a.Result.Outputs {
			reachable[dgst.String()] = true
		}
		if tc, ok := readToolchainManifest(cacheDir, a.Result); ok {
			for _, dgst := range tc.Artifacts {
				if !strings.Contains(dgst, ":") {
					dgst = "sha256:" + dgst
				}
				reachable[dgst] = true
			}
		}
	}
	return reachable, nil
}
