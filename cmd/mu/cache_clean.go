package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// runCacheClean removes blobs from ~/.mu/cache that are not reachable from any
// tagged manifest in index.json. Tags are the GC roots. If any root is
// unreadable, we abort rather than risk deleting a live blob.
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

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"dry_run":      *dryRun,
			"removed":      removed,
			"removed_count": len(removed),
			"freed_bytes":  totalBytes,
			"freed":        formatSize(totalBytes),
		})
		return 0
	}

	verb := "Removed"
	if *dryRun {
		verb = "Would remove"
	}
	fmt.Printf("%s %d unreachable blob(s), %s\n", verb, len(removed), formatSize(totalBytes))
	return 0
}

// reachableBlobs walks every tagged manifest in index.json and returns the set
// of blob digests transitively referenced. Toolchain manifest blobs are opened
// so that toolchain-artifact digests inside them are also reachable.
func reachableBlobs(cacheDir string) (map[string]bool, error) {
	entries, err := readIndex(cacheDir)
	if err != nil {
		return nil, err
	}
	reachable := make(map[string]bool)

	for _, e := range entries {
		reachable[e.ManifestDigest] = true
		manifest, err := readManifest(cacheDir, e.ManifestDigest)
		if err != nil {
			return nil, fmt.Errorf("reading manifest for tag %q (%s): %w", e.Tag, e.ManifestDigest, err)
		}
		reachable[manifest.Config.Digest.String()] = true
		for _, layer := range manifest.Layers {
			reachable[layer.Digest.String()] = true
		}
		if err := walkActionReferences(cacheDir, manifest, reachable); err != nil {
			return nil, fmt.Errorf("walking tag %q: %w", e.Tag, err)
		}
	}
	return reachable, nil
}

// walkActionReferences reads the config blob as an ActionResult and marks all
// of its output digests reachable. If an output is named "manifest", it is
// opened as a toolchain manifest and its artifact digests are marked too.
func walkActionReferences(cacheDir string, manifest *ocispec.Manifest, reachable map[string]bool) error {
	result, err := readActionResult(cacheDir, manifest)
	if err != nil {
		// Config blob may not be an ActionResult (e.g. custom manifest types).
		// Non-fatal: we've already marked the config digest reachable above.
		return nil
	}
	for _, dgst := range result.Outputs {
		reachable[dgst.String()] = true
	}
	manifestDgst, ok := result.Outputs["manifest"]
	if !ok || len(result.Outputs) != 1 {
		return nil
	}
	data, err := os.ReadFile(blobPath(cacheDir, manifestDgst.String()))
	if err != nil {
		return nil
	}
	var tc struct {
		Artifacts map[string]string `json:"artifacts"`
	}
	if err := json.Unmarshal(data, &tc); err != nil {
		return nil
	}
	for _, dgst := range tc.Artifacts {
		if !strings.Contains(dgst, ":") {
			dgst = "sha256:" + dgst
		}
		reachable[dgst] = true
	}
	return nil
}
