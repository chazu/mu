package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
)

// runCacheVerify re-hashes every blob in the local cache and checks that
// every action result's outputs (and, for toolchain receipts, the artifacts
// its manifest lists) are present and intact. With --fix it deletes corrupt
// blobs and every action result that references a corrupt or missing blob
// (or cannot be read), so the next build re-executes those actions; it then
// exits 0 when what remains is consistent.
func runCacheVerify(args []string) int {
	fs := flag.NewFlagSet("cache verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("cache verify", fs, useConfig, useJSON)
	fix := fs.Bool("fix", false, "delete corrupt blobs and the action results that reference them")
	if code, ok := parseFlags(fs, args, "cache verify [flags]"); !ok {
		return code
	}
	if fs.NArg() != 0 {
		return cli.fail(exitUsage, "unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	cacheDir, code, ok := cli.localCacheDir()
	if !ok {
		return code
	}
	jsonOut := cli.JSON
	report := func(format string, args ...any) {
		if !jsonOut {
			fmt.Fprintf(os.Stderr, "  "+format+"\n", args...)
		}
	}

	if *fix {
		// --fix deletes results; keep builds out while it does.
		lock, err := cas.LockExclusive(cacheDir, func() {
			fmt.Fprintln(os.Stderr, "mu cache verify: waiting for running mu builds to release the cache...")
		})
		if err != nil {
			return cli.fail(exitFail, "%v", err)
		}
		defer lock.Release()
	}
	store, err := openLocalCache(cacheDir)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}

	// 1. Re-hash blobs.
	type corruptEntry struct {
		Digest string `json:"digest"`
		Actual string `json:"actual"`
		Size   int64  `json:"size"`
	}
	var okBlobs, errCount int
	var corrupted []corruptEntry
	bad := map[string]bool{} // digest → corrupt (removed with --fix)
	blobDir := filepath.Join(cacheDir, "blobs", "sha256")
	walkErr := filepath.Walk(blobDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if !os.IsNotExist(err) {
				errCount++
				report("error: %v", err)
			}
			return nil
		}
		if info.IsDir() {
			return nil
		}
		digest := "sha256:" + info.Name()
		actual, err := hashFile(path)
		if err != nil {
			errCount++
			report("error: %s: %v", digest, err)
			return nil
		}
		if actual == info.Name() {
			okBlobs++
			return nil
		}
		bad[digest] = true
		corrupted = append(corrupted, corruptEntry{Digest: digest, Actual: "sha256:" + actual, Size: info.Size()})
		report("CORRUPT %s (content hashes to sha256:%s)", digest, actual[:12])
		return nil
	})
	if walkErr != nil {
		return cli.fail(exitFail, "walking cache: %v", walkErr)
	}

	// 2. Check every action result against the blobs.
	type brokenResult struct {
		Tag    string   `json:"tag"`
		Reason string   `json:"reason"`
		Blobs  []string `json:"blobs,omitempty"`
	}
	var broken []brokenResult
	var brokenKeys []cas.ActionKey
	var missing int
	walkErr = store.WalkActionResults(func(key cas.ActionKey, result *cas.ActionResult, err error) error {
		tag := oci.ActionTag(key)
		if err != nil {
			broken = append(broken, brokenResult{Tag: tag, Reason: "unreadable: " + err.Error()})
			brokenKeys = append(brokenKeys, key)
			report("UNREADABLE %s: %v", tag, err)
			return nil
		}
		var refs []string
		for _, d := range result.Outputs {
			refs = append(refs, d.String())
		}
		if !bad[result.Outputs["manifest"].String()] {
			if tc, ok := readToolchainManifest(cacheDir, result); ok {
				for _, d := range tc.Artifacts {
					if !strings.Contains(d, ":") {
						d = "sha256:" + d
					}
					refs = append(refs, d)
				}
			}
		}
		var corruptRefs, missingRefs []string
		for _, d := range refs {
			switch p := blobPath(cacheDir, d); {
			case bad[d]:
				corruptRefs = append(corruptRefs, d)
			case p == "":
				missingRefs = append(missingRefs, d) // not a valid digest: never present
			default:
				if _, err := os.Stat(p); os.IsNotExist(err) {
					missingRefs = append(missingRefs, d)
				}
			}
		}
		if len(corruptRefs) == 0 && len(missingRefs) == 0 {
			return nil
		}
		missing += len(missingRefs)
		for _, d := range missingRefs {
			report("MISSING %s (referenced by %s)", d, tag)
		}
		reason := "references corrupt blobs"
		if len(corruptRefs) == 0 {
			reason = "references missing blobs"
		}
		sort.Strings(corruptRefs)
		sort.Strings(missingRefs)
		broken = append(broken, brokenResult{Tag: tag, Reason: reason, Blobs: append(corruptRefs, missingRefs...)})
		brokenKeys = append(brokenKeys, key)
		return nil
	})
	if walkErr != nil {
		return cli.fail(exitFail, "reading action results: %v", walkErr)
	}

	// 3. Repair: drop corrupt blobs, then the results that depended on them.
	var removedBlobs, removedResults, fixErrors int
	if *fix {
		for _, c := range corrupted {
			d, _ := cas.ParseFullDigest(c.Digest)
			if err := store.Delete(context.Background(), d); err != nil && !errors.Is(err, os.ErrNotExist) {
				fixErrors++
				report("error: %v", err)
				continue
			}
			removedBlobs++
			report("removed blob %s", c.Digest)
		}
		for i, key := range brokenKeys {
			p, err := store.ActionResultPath(key)
			if err == nil {
				err = os.Remove(p)
			}
			if err != nil && !os.IsNotExist(err) {
				fixErrors++
				report("error: removing %s: %v", broken[i].Tag, err)
				continue
			}
			removedResults++
			report("removed result %s", broken[i].Tag)
		}
	}

	problems := len(corrupted) > 0 || len(broken) > 0 || errCount > 0
	consistent := !problems || (*fix && fixErrors == 0 && errCount == 0)

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"path":            cacheDir,
			"ok":              okBlobs,
			"corrupt":         len(corrupted),
			"missing":         missing,
			"broken_results":  len(broken),
			"errors":          errCount,
			"corrupted":       corrupted,
			"broken":          broken,
			"fix":             *fix,
			"removed_blobs":   removedBlobs,
			"removed_results": removedResults,
			"consistent":      consistent,
		})
	} else {
		fmt.Printf("Verified %d blobs: %d ok", okBlobs+len(corrupted), okBlobs)
		if len(corrupted) > 0 {
			fmt.Printf(", %d corrupt", len(corrupted))
		}
		if missing > 0 {
			fmt.Printf(", %d missing", missing)
		}
		if len(broken) > 0 {
			fmt.Printf(", %d broken action result(s)", len(broken))
		}
		if errCount > 0 {
			fmt.Printf(", %d errors", errCount)
		}
		fmt.Println()
		switch {
		case !problems:
			fmt.Println("Cache is healthy.")
		case consistent:
			fmt.Printf("Removed %d blob(s) and %d action result(s); the cache is consistent. Affected actions rebuild on the next build.\n",
				removedBlobs, removedResults)
		case !*fix:
			fmt.Println("Run `mu cache verify --fix` to remove them.")
		}
	}

	if !consistent {
		return exitFail
	}
	return exitOK
}

// runVerifyMoved answers the removed top-level `mu verify`.
func runVerifyMoved() int {
	fmt.Fprintln(os.Stderr, "mu verify: moved to `mu cache verify` (use --fix to repair)")
	return exitUsage
}

// hashFile returns the hex sha256 of the file at path.
func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
