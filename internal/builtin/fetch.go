// Package builtin provides built-in operations for mu.
package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// maxRetries is the number of retry attempts for transient network errors.
const maxRetries = 3

// ForgeFetch downloads a file from url, verifies its SHA-256 hash matches
// expectedSHA256, and atomically places it at destPath. On transient network
// errors it retries up to 3 times with exponential backoff and jitter.
func ForgeFetch(ctx context.Context, rawURL string, expectedSHA256 string, destPath string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	switch parsed.Scheme {
	case "http", "https":
		// allowed
	default:
		return &UnsupportedSchemeError{Scheme: parsed.Scheme, URL: rawURL}
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			base := time.Duration(1<<uint(attempt-1)) * time.Second
			jitter := time.Duration(rand.Int63n(int64(base / 2)))
			delay := base + jitter

			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}

		lastErr = forgeFetchOnce(ctx, rawURL, expectedSHA256, destPath)
		if lastErr == nil {
			return nil
		}

		// Hash mismatches are not transient; don't retry.
		if _, ok := lastErr.(*HashMismatchError); ok {
			return lastErr
		}
	}
	return fmt.Errorf("fetch %s: all %d retries exhausted: %w", rawURL, maxRetries, lastErr)
}

// UnsupportedSchemeError is returned when the URL has a scheme other than
// http or https.
type UnsupportedSchemeError struct {
	Scheme string
	URL    string
}

func (e *UnsupportedSchemeError) Error() string {
	return fmt.Sprintf("unsupported URL scheme %q in %s: only http and https are allowed", e.Scheme, e.URL)
}

// HashMismatchError is returned when the downloaded content does not match the
// expected SHA-256 hash.
type HashMismatchError struct {
	Expected string
	Actual   string
}

func (e *HashMismatchError) Error() string {
	return fmt.Sprintf("sha256 mismatch: expected %s, got %s", e.Expected, e.Actual)
}

// forgeFetchOnce performs a single download attempt.
func forgeFetchOnce(ctx context.Context, rawURL string, expectedSHA256 string, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("HTTP GET %s: %w", rawURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP GET %s: status %d", rawURL, resp.StatusCode)
	}

	// Create temp file in the same directory as destPath so os.Rename is atomic.
	dir := filepath.Dir(destPath)
	tmp, err := os.CreateTemp(dir, ".fetch-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()

	// Ensure cleanup on any error path.
	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	reader := io.TeeReader(resp.Body, hasher)

	if _, err := io.Copy(tmp, reader); err != nil {
		return fmt.Errorf("downloading %s: %w", rawURL, err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing temp file: %w", err)
	}

	actual := hex.EncodeToString(hasher.Sum(nil))
	if actual != expectedSHA256 {
		return &HashMismatchError{Expected: expectedSHA256, Actual: actual}
	}

	if err := os.Rename(tmpPath, destPath); err != nil {
		return fmt.Errorf("renaming temp file to %s: %w", destPath, err)
	}

	success = true
	return nil
}
