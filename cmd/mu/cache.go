package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
)

func cachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mu", "cache")
}

// cachedAction is one action result in the local disk cache.
type cachedAction struct {
	Tag    string // action-sha256-<key>: the tag `mu cache push` publishes it under
	Key    cas.ActionKey
	Result *cas.ActionResult
}

// openLocalCache opens the local disk cache. Opening also migrates action
// results recorded by older releases in an OCI index.json.
func openLocalCache(cacheDir string) (*cas.DiskStore, error) {
	return cas.NewDiskStore(cacheDir)
}

// readCachedActions lists the local cache's action results in key order.
// Unreadable entries are skipped unless strict, in which case the first one is
// returned as an error (garbage collection must not guess at its roots).
func readCachedActions(store *cas.DiskStore, strict bool) ([]cachedAction, error) {
	var actions []cachedAction
	err := store.WalkActionResults(func(key cas.ActionKey, result *cas.ActionResult, err error) error {
		if err != nil {
			if strict {
				return err
			}
			return nil
		}
		actions = append(actions, cachedAction{Tag: oci.ActionTag(key), Key: key, Result: result})
		return nil
	})
	return actions, err
}

// toolchainManifest is the blob a scratch toolchain's action result points to.
type toolchainManifest struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Artifacts map[string]string `json:"artifacts"`
}

// readToolchainManifest returns the toolchain manifest when result is a
// toolchain receipt (a single output named "manifest").
func readToolchainManifest(cacheDir string, result *cas.ActionResult) (*toolchainManifest, bool) {
	manifestDgst, ok := result.Outputs["manifest"]
	if !ok || len(result.Outputs) != 1 {
		return nil, false
	}
	data, err := os.ReadFile(blobPath(cacheDir, manifestDgst.String()))
	if err != nil {
		return nil, false
	}
	var tc toolchainManifest
	if err := json.Unmarshal(data, &tc); err != nil || tc.Name == "" {
		return nil, false
	}
	return &tc, true
}

// blobPath returns the filesystem path for a digest in the local cache.
func blobPath(cacheDir, digest string) string {
	// digest is "sha256:abcdef..." → blobs/sha256/abcdef...
	// Only full identities name a blob file; anything else (prefixes, "..",
	// separators) could resolve outside the layout and yields no path.
	d, err := cas.ParseFullDigest(digest)
	if err != nil {
		return ""
	}
	return filepath.Join(cacheDir, "blobs", d.Algorithm, d.Hash)
}

// blobSize returns the file size of a blob by digest.
func blobSize(cacheDir, digest string) int64 {
	p := blobPath(cacheDir, digest)
	if p == "" {
		return 0
	}
	info, err := os.Stat(p)
	if err != nil {
		return 0
	}
	return info.Size()
}

func formatSize(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(bytes)/float64(1<<30))
	case bytes >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(bytes)/float64(1<<20))
	case bytes >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(bytes)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}

// --- mu cache ls ---

func runCacheLs(args []string) int {
	fs := flag.NewFlagSet("cache ls", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("cache ls", fs, useConfig)
	toolchains := fs.Bool("toolchains", false, "show only toolchain manifests")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if code, ok := parseFlags(fs, args, "cache ls [flags]"); !ok {
		return code
	}

	cacheDir, code, ok := cli.localCacheDir()
	if !ok {
		return code
	}
	store, err := openLocalCache(cacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache ls: %v\n", err)
		return 1
	}
	entries, err := readCachedActions(store, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache ls: %v\n", err)
		return 1
	}

	if *toolchains {
		return cacheLsToolchains(cacheDir, entries, *jsonOut)
	}
	return cacheLsActions(cacheDir, entries, *jsonOut)
}

func cacheLsActions(cacheDir string, entries []cachedAction, jsonOut bool) int {
	type actionEntry struct {
		Tag      string `json:"tag"`
		Outputs  int    `json:"outputs"`
		Size     string `json:"size"`
		SizeRaw  int64  `json:"size_bytes"`
		ExitCode int    `json:"exit_code"`
	}

	var items []actionEntry
	for _, e := range entries {
		result := e.Result
		var totalSize int64
		for _, dgst := range result.Outputs {
			totalSize += blobSize(cacheDir, dgst.String())
		}

		items = append(items, actionEntry{
			Tag:      e.Tag,
			Outputs:  len(result.Outputs),
			Size:     formatSize(totalSize),
			SizeRaw:  totalSize,
			ExitCode: result.ExitCode,
		})
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(items)
		return 0
	}

	if len(items) == 0 {
		fmt.Println("No cached actions.")
		return 0
	}

	fmt.Printf("%-72s %7s %8s\n", "ACTION", "OUTPUTS", "SIZE")
	for _, item := range items {
		tag := item.Tag
		if len(tag) > 72 {
			tag = tag[:69] + "..."
		}
		fmt.Printf("%-72s %7d %8s\n", tag, item.Outputs, item.Size)
	}
	return 0
}

func cacheLsToolchains(cacheDir string, entries []cachedAction, jsonOut bool) int {
	// Toolchain manifests are stored as action results with key "toolchain:<name>:<version>".
	// We need to read each result's manifest blob to get the toolchain name/version.
	type tcEntry struct {
		Name      string `json:"name"`
		Version   string `json:"version"`
		Artifacts int    `json:"artifacts"`
		Size      string `json:"size"`
		SizeRaw   int64  `json:"size_bytes"`
	}

	var items []tcEntry
	seen := map[string]bool{}

	for _, e := range entries {
		tcManifest, ok := readToolchainManifest(cacheDir, e.Result)
		if !ok {
			continue
		}

		key := tcManifest.Name + ":" + tcManifest.Version
		if seen[key] {
			continue
		}
		seen[key] = true

		var totalSize int64
		for _, dgst := range tcManifest.Artifacts {
			totalSize += blobSize(cacheDir, dgst)
		}

		items = append(items, tcEntry{
			Name:      tcManifest.Name,
			Version:   tcManifest.Version,
			Artifacts: len(tcManifest.Artifacts),
			Size:      formatSize(totalSize),
			SizeRaw:   totalSize,
		})
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(items)
		return 0
	}

	if len(items) == 0 {
		fmt.Println("No cached toolchains.")
		return 0
	}

	fmt.Printf("%-20s %-15s %10s %8s\n", "TOOLCHAIN", "VERSION", "ARTIFACTS", "SIZE")
	for _, item := range items {
		fmt.Printf("%-20s %-15s %10d %8s\n", item.Name, item.Version, item.Artifacts, item.Size)
	}
	return 0
}

// --- mu cache inspect ---

func runCacheInspect(args []string) int {
	fs := flag.NewFlagSet("cache inspect", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("cache inspect", fs, useConfig)
	jsonOut := fs.Bool("json", false, "output as JSON")
	if code, ok := parseFlags(fs, args, "cache inspect [flags] <ref>"); !ok {
		return code
	}

	ref := fs.Arg(0)
	if ref == "" {
		fmt.Fprintln(os.Stderr, "mu cache inspect: requires a reference (action tag, toolchain name, or blob digest)")
		return 2
	}

	cacheDir, code, ok := cli.localCacheDir()
	if !ok {
		return code
	}
	store, err := openLocalCache(cacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache inspect: %v\n", err)
		return 1
	}
	entries, err := readCachedActions(store, false)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache inspect: %v\n", err)
		return 1
	}

	// Try to match as a toolchain name first.
	for _, e := range entries {
		tcManifest, ok := readToolchainManifest(cacheDir, e.Result)
		if ok && tcManifest.Name == ref {
			return inspectToolchain(cacheDir, tcManifest.Name, tcManifest.Version, tcManifest.Artifacts, *jsonOut)
		}
	}

	// Try to match as an action tag (full or partial).
	for _, e := range entries {
		if e.Tag == ref || strings.Contains(e.Tag, ref) {
			return inspectAction(cacheDir, e.Tag, e.Result, *jsonOut)
		}
	}

	// Try as a raw blob digest.
	dgst := ref
	if !strings.Contains(dgst, ":") {
		dgst = "sha256:" + dgst
	}
	p := blobPath(cacheDir, dgst)
	if info, err := os.Stat(p); err == nil {
		return inspectBlob(dgst, info.Size(), *jsonOut)
	}

	fmt.Fprintf(os.Stderr, "mu cache inspect: %q not found\n", ref)
	return 1
}

func inspectToolchain(cacheDir, name, version string, artifacts map[string]string, jsonOut bool) int {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"type":      "toolchain",
			"name":      name,
			"version":   version,
			"artifacts": artifacts,
		})
		return 0
	}

	fmt.Printf("Toolchain: %s\n", name)
	fmt.Printf("Version:   %s\n", version)
	fmt.Printf("Artifacts:\n")
	for path, dgst := range artifacts {
		size := blobSize(cacheDir, dgst)
		fmt.Printf("  %-40s %s  %s\n", path, dgst, formatSize(size))
	}
	return 0
}

func inspectAction(cacheDir, tag string, result *cas.ActionResult, jsonOut bool) int {
	if jsonOut {
		// Flatten digests to strings for JSON output.
		outputs := make(map[string]string, len(result.Outputs))
		for k, v := range result.Outputs {
			outputs[k] = v.String()
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"type":      "action",
			"tag":       tag,
			"exit_code": result.ExitCode,
			"outputs":   outputs,
		})
		return 0
	}

	fmt.Printf("Action:    %s\n", tag)
	fmt.Printf("Exit code: %d\n", result.ExitCode)
	if len(result.Outputs) > 0 {
		fmt.Printf("Outputs:\n")
		for name, dgst := range result.Outputs {
			size := blobSize(cacheDir, dgst.String())
			fmt.Printf("  %-40s %s  %s\n", name, dgst.String(), formatSize(size))
		}
	} else {
		fmt.Printf("Outputs:   (none)\n")
	}
	return 0
}

func inspectBlob(digest string, size int64, jsonOut bool) int {
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"type":   "blob",
			"digest": digest,
			"size":   size,
		})
		return 0
	}

	fmt.Printf("Blob:   %s\n", digest)
	fmt.Printf("Size:   %s\n", formatSize(size))
	return 0
}

// --- mu cache size ---

func runCacheSize(args []string) int {
	fs := flag.NewFlagSet("cache size", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("cache size", fs, useConfig)
	jsonOut := fs.Bool("json", false, "output as JSON")
	if code, ok := parseFlags(fs, args, "cache size [flags]"); !ok {
		return code
	}

	cacheDir, code, ok := cli.localCacheDir()
	if !ok {
		return code
	}

	// Count blobs.
	blobDir := filepath.Join(cacheDir, "blobs", "sha256")
	var blobCount int
	var totalBytes int64
	filepath.Walk(blobDir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		blobCount++
		totalBytes += info.Size()
		return nil
	})

	// Count action results.
	actionCount := 0
	if store, err := openLocalCache(cacheDir); err == nil {
		store.WalkActionResults(func(cas.ActionKey, *cas.ActionResult, error) error {
			actionCount++
			return nil
		})
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"blobs":          blobCount,
			"action_results": actionCount,
			"total_bytes":    totalBytes,
			"total_size":     formatSize(totalBytes),
			"path":           cacheDir,
		})
		return 0
	}

	fmt.Printf("Blobs:          %d\n", blobCount)
	fmt.Printf("Action results: %d\n", actionCount)
	fmt.Printf("Total:          %s\n", formatSize(totalBytes))
	fmt.Printf("Path:           %s\n", cacheDir)
	return 0
}

// --- mu cache (dispatcher) ---

func runCache(args []string) int {
	if len(args) == 0 || isHelpArg(args[0]) {
		w, code := dispatcherUsage(args)
		fmt.Fprintln(w, `usage: mu cache <command>

Commands:
  ls        List cached action results and toolchains
  inspect   Show details of a cached item
  size      Show total cache disk usage
  clean     Remove unreachable blobs (GC)
  push      Copy cached actions to the remote OCI registry (cache.push)
  login     Log in to an OCI registry [host] (default: cache.push.registry; stored in ~/.mu/credentials.json)
  logout    Remove stored credentials for an OCI registry [host] (default: cache.push.registry)`)
		return code
	}

	switch args[0] {
	case "ls":
		return runCacheLs(args[1:])
	case "inspect":
		return runCacheInspect(args[1:])
	case "size":
		return runCacheSize(args[1:])
	case "clean":
		return runCacheClean(args[1:])
	case "push":
		return runCachePush(args[1:])
	case "login":
		return runCacheLogin(args[1:])
	case "logout":
		return runCacheLogout(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mu cache: unknown command %q\n", args[0])
		return 2
	}
}
