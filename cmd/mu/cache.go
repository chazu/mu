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

func cachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".mu", "cache")
}

// indexEntry is a manifest entry from the OCI index.json.
type indexEntry struct {
	Tag            string
	ManifestDigest string
	ManifestSize   int64
}

// digestJSON matches cas.Digest for JSON decoding.
type digestJSON struct {
	Algorithm string `json:"Algorithm"`
	Hash      string `json:"Hash"`
}

func (d digestJSON) String() string {
	return d.Algorithm + ":" + d.Hash
}

// actionResultJSON matches cas.ActionResult for JSON decoding.
type actionResultJSON struct {
	Outputs  map[string]digestJSON `json:"outputs"`
	ExitCode int                   `json:"exit_code"`
}

// readIndex reads the OCI index.json and returns tagged entries.
func readIndex(cacheDir string) ([]indexEntry, error) {
	data, err := os.ReadFile(filepath.Join(cacheDir, "index.json"))
	if err != nil {
		return nil, fmt.Errorf("reading index.json: %w", err)
	}

	var idx ocispec.Index
	if err := json.Unmarshal(data, &idx); err != nil {
		return nil, fmt.Errorf("parsing index.json: %w", err)
	}

	var entries []indexEntry
	for _, m := range idx.Manifests {
		tag := m.Annotations["org.opencontainers.image.ref.name"]
		if tag == "" {
			continue
		}
		entries = append(entries, indexEntry{
			Tag:            tag,
			ManifestDigest: m.Digest.String(),
			ManifestSize:   m.Size,
		})
	}
	return entries, nil
}

// blobPath returns the filesystem path for a digest in the OCI layout.
func blobPath(cacheDir, digest string) string {
	// digest is "sha256:abcdef..." → blobs/sha256/abcdef...
	parts := strings.SplitN(digest, ":", 2)
	if len(parts) != 2 {
		return ""
	}
	return filepath.Join(cacheDir, "blobs", parts[0], parts[1])
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

// readManifest reads and parses an OCI manifest by its digest.
func readManifest(cacheDir, digest string) (*ocispec.Manifest, error) {
	p := blobPath(cacheDir, digest)
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

// readActionResult reads the config blob of an action manifest as an ActionResult.
func readActionResult(cacheDir string, manifest *ocispec.Manifest) (*actionResultJSON, error) {
	p := blobPath(cacheDir, manifest.Config.Digest.String())
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var result actionResultJSON
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	return &result, nil
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
	toolchains := fs.Bool("toolchains", false, "show only toolchain manifests")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cacheDir := cachePath()
	entries, err := readIndex(cacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache ls: %v\n", err)
		return 1
	}

	if *toolchains {
		return cacheLsToolchains(cacheDir, entries, *jsonOut)
	}
	return cacheLsActions(cacheDir, entries, *jsonOut)
}

func cacheLsActions(cacheDir string, entries []indexEntry, jsonOut bool) int {
	type actionEntry struct {
		Tag      string `json:"tag"`
		Outputs  int    `json:"outputs"`
		Size     string `json:"size"`
		SizeRaw  int64  `json:"size_bytes"`
		ExitCode int    `json:"exit_code"`
	}

	var items []actionEntry
	for _, e := range entries {
		if !strings.HasPrefix(e.Tag, "action-") {
			continue
		}

		manifest, err := readManifest(cacheDir, e.ManifestDigest)
		if err != nil {
			continue
		}

		result, err := readActionResult(cacheDir, manifest)
		if err != nil {
			continue
		}

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

func cacheLsToolchains(cacheDir string, entries []indexEntry, jsonOut bool) int {
	// Toolchain manifests are stored as action results with key "toolchain:<name>:<version>".
	// We need to read each manifest's config blob to get the toolchain name/version.
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
		if !strings.HasPrefix(e.Tag, "action-") {
			continue
		}

		manifest, err := readManifest(cacheDir, e.ManifestDigest)
		if err != nil {
			continue
		}

		result, err := readActionResult(cacheDir, manifest)
		if err != nil {
			continue
		}

		// Toolchain manifests have a single output named "manifest".
		manifestDgst, ok := result.Outputs["manifest"]
		if !ok || len(result.Outputs) != 1 {
			continue
		}

		// Read the manifest blob to get toolchain name/version.
		p := blobPath(cacheDir, manifestDgst.String())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}

		var tcManifest struct {
			Name      string            `json:"name"`
			Version   string            `json:"version"`
			Artifacts map[string]string `json:"artifacts"`
		}
		if err := json.Unmarshal(data, &tcManifest); err != nil {
			continue
		}
		if tcManifest.Name == "" {
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
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	ref := fs.Arg(0)
	if ref == "" {
		fmt.Fprintln(os.Stderr, "mu cache inspect: requires a reference (action tag, toolchain name, or blob digest)")
		return 2
	}

	cacheDir := cachePath()
	entries, err := readIndex(cacheDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu cache inspect: %v\n", err)
		return 1
	}

	// Try to match as a toolchain name first.
	for _, e := range entries {
		if !strings.HasPrefix(e.Tag, "action-") {
			continue
		}
		manifest, err := readManifest(cacheDir, e.ManifestDigest)
		if err != nil {
			continue
		}
		result, err := readActionResult(cacheDir, manifest)
		if err != nil {
			continue
		}

		manifestDgst, ok := result.Outputs["manifest"]
		if !ok || len(result.Outputs) != 1 {
			continue
		}

		p := blobPath(cacheDir, manifestDgst.String())
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}

		var tcManifest struct {
			Name      string            `json:"name"`
			Version   string            `json:"version"`
			Artifacts map[string]string `json:"artifacts"`
		}
		if err := json.Unmarshal(data, &tcManifest); err != nil {
			continue
		}

		if tcManifest.Name == ref {
			return inspectToolchain(cacheDir, tcManifest.Name, tcManifest.Version, tcManifest.Artifacts, *jsonOut)
		}
	}

	// Try to match as an action tag (full or partial).
	for _, e := range entries {
		if e.Tag == ref || strings.Contains(e.Tag, ref) {
			manifest, err := readManifest(cacheDir, e.ManifestDigest)
			if err != nil {
				fmt.Fprintf(os.Stderr, "mu cache inspect: reading manifest: %v\n", err)
				return 1
			}
			result, err := readActionResult(cacheDir, manifest)
			if err != nil {
				fmt.Fprintf(os.Stderr, "mu cache inspect: reading action result: %v\n", err)
				return 1
			}
			return inspectAction(cacheDir, e.Tag, result, *jsonOut)
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

func inspectAction(cacheDir, tag string, result *actionResultJSON, jsonOut bool) int {
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
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	cacheDir := cachePath()

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

	// Count action results from index.
	entries, _ := readIndex(cacheDir)
	actionCount := 0
	for _, e := range entries {
		if strings.HasPrefix(e.Tag, "action-") {
			actionCount++
		}
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
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: mu cache <command>

Commands:
  ls        List cached action results and toolchains
  inspect   Show details of a cached item
  size      Show total cache disk usage
  clean     Remove unreachable blobs (GC)`)
		return 2
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
	default:
		fmt.Fprintf(os.Stderr, "mu cache: unknown command %q\n", args[0])
		return 2
	}
}

