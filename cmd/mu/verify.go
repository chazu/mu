package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func runVerify(args []string) int {
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	ctx := newCLIContext("verify", fs)
	fix := fs.Bool("fix", false, "delete corrupt blobs")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if code, ok := ctx.Resolve(resolveOpts{}); !ok {
		return code
	}
	jsonOut := &ctx.JSON

	cacheDir := ctx.CachePath()
	blobDir := filepath.Join(cacheDir, "blobs", "sha256")

	if _, err := os.Stat(blobDir); os.IsNotExist(err) {
		return ctx.fail(exitFail, "no cache found")
	}

	var ok, corrupt, missing, errCount int
	type corruptEntry struct {
		Digest   string `json:"digest"`
		Expected string `json:"expected"`
		Actual   string `json:"actual"`
		Size     int64  `json:"size"`
	}
	var corrupted []corruptEntry

	walkErr := filepath.Walk(blobDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			errCount++
			return nil
		}
		if info.IsDir() {
			return nil
		}

		// The expected hash is the filename.
		expectedHash := info.Name()
		expectedDigest := "sha256:" + expectedHash

		f, err := os.Open(path)
		if err != nil {
			errCount++
			if !*jsonOut {
				fmt.Fprintf(os.Stderr, "  error: %s: %v\n", expectedDigest, err)
			}
			return nil
		}
		defer f.Close()

		h := sha256.New()
		if _, err := io.Copy(h, f); err != nil {
			errCount++
			if !*jsonOut {
				fmt.Fprintf(os.Stderr, "  error: %s: %v\n", expectedDigest, err)
			}
			return nil
		}

		actualHash := hex.EncodeToString(h.Sum(nil))
		if actualHash != expectedHash {
			corrupt++
			entry := corruptEntry{
				Digest:   expectedDigest,
				Expected: expectedHash,
				Actual:   actualHash,
				Size:     info.Size(),
			}
			corrupted = append(corrupted, entry)
			if !*jsonOut {
				fmt.Fprintf(os.Stderr, "  CORRUPT %s (expected %s, got %s)\n",
					expectedDigest, expectedHash[:12], actualHash[:12])
			}
			if *fix {
				if err := os.Remove(path); err != nil {
					fmt.Fprintf(os.Stderr, "  error removing %s: %v\n", expectedDigest, err)
				} else if !*jsonOut {
					fmt.Fprintf(os.Stderr, "  removed %s\n", expectedDigest)
				}
			}
		} else {
			ok++
		}

		return nil
	})
	if walkErr != nil {
		return ctx.fail(exitFail, "walking cache: %v", walkErr)
	}

	// Verify action results reference valid blobs.
	entries, _ := readIndex(cacheDir)
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
		for name, dgst := range result.Outputs {
			p := blobPath(cacheDir, dgst.String())
			if p == "" {
				continue
			}
			if _, err := os.Stat(p); os.IsNotExist(err) {
				missing++
				if !*jsonOut {
					fmt.Fprintf(os.Stderr, "  MISSING %s (referenced by %s output %q)\n",
						dgst.String(), e.Tag, name)
				}
			}
		}
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(map[string]any{
			"ok":        ok,
			"corrupt":   corrupt,
			"missing":   missing,
			"errors":    errCount,
			"fixed":     *fix && corrupt > 0,
			"corrupted": corrupted,
		})
	} else {
		fmt.Printf("Verified %d blobs: %d ok", ok+corrupt, ok)
		if corrupt > 0 {
			fmt.Printf(", %d corrupt", corrupt)
		}
		if missing > 0 {
			fmt.Printf(", %d missing", missing)
		}
		if errCount > 0 {
			fmt.Printf(", %d errors", errCount)
		}
		fmt.Println()
		if corrupt == 0 && missing == 0 && errCount == 0 {
			fmt.Println("Cache is healthy.")
		}
	}

	if corrupt > 0 || missing > 0 {
		return exitFail
	}
	return exitOK
}
