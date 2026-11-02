package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/plugincatalog"
	"github.com/chazu/mu/internal/staging"
)

// runCacheClean removes blobs from the local cache that are not reachable
// from any action result (the GC roots) or from a plugin bundle the project
// pins. If any root is unreadable, we abort rather than risk deleting a live
// blob. It also prunes the plugins directory (see prunePlugins) and stale
// staging. It holds the cache lock exclusively, so it waits for running
// builds (which hold it shared) and they wait for it.
func runCacheClean(args []string) int {
	fs := flag.NewFlagSet("cache clean", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("cache clean", fs, useConfig)
	dryRun := fs.Bool("dry-run", false, "show what would be removed without deleting")
	jsonOut := fs.Bool("json", false, "output as JSON")
	allPlugins := fs.Bool("all-plugins", false, "also keep only the newest installed version of every plugin, not just the project's")
	if code, ok := parseFlags(fs, args, "cache clean [flags]"); !ok {
		return code
	}

	cacheDir, code, ok := cli.localCacheDir()
	if !ok {
		return code
	}
	lock, err := cas.LockExclusive(cacheDir, func() {
		fmt.Fprintln(os.Stderr, "mu cache clean: waiting for running mu builds to release the cache...")
	})
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	defer lock.Release()

	pinned, err := pinnedPlugins(cli)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	reachable, err := reachableBlobs(cacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache clean: %v\n", err)
		return 1
	}
	for _, digests := range pinned {
		for hash := range digests {
			reachable["sha256:"+hash] = true
		}
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

	plugins, err := prunePlugins(cli.Paths.PluginsDir, pinned, *allPlugins, *dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache clean: plugins: %v\n", err)
	}
	var pluginBytes int64
	for _, p := range plugins {
		pluginBytes += p.Bytes
	}

	// Also reclaim staging stranded by crashed processes. The inactivity
	// threshold still applies, so concurrent runs are never disturbed.
	var stagedBytes int64
	staged, sweepErr := staging.Sweep(staging.DefaultRules(cli.Paths, os.TempDir()), staging.MinAge, time.Now(), *dryRun)
	if sweepErr != nil {
		fmt.Fprintf(os.Stderr, "mu cache clean: staging: %v\n", sweepErr)
	}
	for _, s := range staged {
		stagedBytes += s.Bytes
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
			"plugins_removed":     plugins,
			"plugins_freed_bytes": pluginBytes,
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
	fmt.Printf("%s %d plugin version(s), %s\n", verb, len(plugins), formatSize(pluginBytes))
	fmt.Printf("%s %d stale staging path(s), %s\n", verb, len(staged), formatSize(stagedBytes))
	return 0
}

// pinnedPlugins returns, per plugin name, the sha256 hashes the current
// project pins: bundle digests in mu.lock and plugin digests in mu.cue.
// Outside a project it is empty.
func pinnedPlugins(cli *cliContext) (map[string]map[string]bool, error) {
	pinned := map[string]map[string]bool{}
	add := func(name, digest string) {
		d, err := cas.ParseFullDigest(digest)
		if name == "" || err != nil || d.Algorithm != "sha256" {
			return
		}
		if pinned[name] == nil {
			pinned[name] = map[string]bool{}
		}
		pinned[name][d.Hash] = true
	}
	if cli.ProjectRoot == "" {
		return pinned, nil
	}
	lock, err := plugincatalog.LoadLock(filepath.Join(cli.ProjectRoot, "mu.lock"))
	if err != nil {
		return nil, err
	}
	for _, p := range lock.Plugins {
		add(p.Name, p.BundleDigest)
	}
	if cli.Config != nil {
		for _, p := range cli.Config.Plugins {
			add(p.Name, p.Digest)
		}
	}
	return pinned, nil
}

// pluginRemoval is one pruned entry of the plugins directory.
type pluginRemoval struct {
	Plugin string `json:"plugin"`
	Path   string `json:"path"`
	Reason string `json:"reason"` // "legacy", "unpinned", "superseded"
	Bytes  int64  `json:"bytes"`
}

// prunePlugins removes from pluginsDir:
//   - legacy entries named by a short (12-hex) digest prefix, always;
//   - for each plugin the project pins, every version it does not pin;
//   - with all, for every other plugin, every version but the newest
//     (by modification time).
//
// Versions of plugins the project does not pin are otherwise kept: other
// projects on this machine may use them. In-flight staging (.bundle-*,
// .plugin-*) is left to the staging sweep.
func prunePlugins(pluginsDir string, pinned map[string]map[string]bool, all, dryRun bool) ([]pluginRemoval, error) {
	names, err := os.ReadDir(pluginsDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	type version struct {
		path  string
		hash  string
		mtime time.Time
	}
	var removed []pluginRemoval
	var errs []error
	remove := func(name, path, reason string) {
		bytes := treeSize(path)
		if !dryRun {
			if err := os.RemoveAll(path); err != nil {
				errs = append(errs, err)
				return
			}
		}
		removed = append(removed, pluginRemoval{Plugin: name, Path: path, Reason: reason, Bytes: bytes})
	}
	for _, n := range names {
		if !n.IsDir() || strings.HasPrefix(n.Name(), ".") {
			continue
		}
		name := n.Name()
		dir := filepath.Join(pluginsDir, name)
		entries, err := os.ReadDir(dir)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var versions []version
		for _, e := range entries {
			hash, _, ok := pluginEntryHash(e)
			if !ok {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if len(hash) != 64 {
				remove(name, path, "legacy")
				continue
			}
			info, err := e.Info()
			if err != nil {
				errs = append(errs, err)
				continue
			}
			versions = append(versions, version{path: path, hash: hash, mtime: info.ModTime()})
		}
		switch {
		case pinned[name] != nil:
			for _, v := range versions {
				if !pinned[name][v.hash] {
					remove(name, v.path, "unpinned")
				}
			}
		case all && len(versions) > 0:
			newest := versions[0]
			for _, v := range versions[1:] {
				if v.mtime.After(newest.mtime) {
					newest = v
				}
			}
			for _, v := range versions {
				if v.hash != newest.hash { // a bundle and a file of one digest are one version
					remove(name, v.path, "superseded")
				}
			}
		}
		if !dryRun {
			os.Remove(dir) // drop the name directory once it is empty
		}
	}
	return removed, errors.Join(errs...)
}

// treeSize sums the sizes of the regular files at or under path.
func treeSize(path string) int64 {
	var total int64
	filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	return total
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
