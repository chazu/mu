package registryhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"github.com/chazu/mu/internal/archive"
)

var downloadClient = NewClient(Options{})

// maxRetries is the number of retry attempts after a transient failure.
const maxRetries = 3

// retryBaseDelay is the first backoff delay; it doubles per attempt, plus up
// to 50% jitter. Tests shorten it.
var retryBaseDelay = time.Second

// downloadLimits caps one download at the per-file limit mu applies to any
// extracted toolchain file (4 GiB, overridable with MU_ARCHIVE_MAX_BYTES).
var downloadLimits = archive.Toolchain

// Fetch downloads rawURL, verifies its SHA-256 matches expectedSHA256, and
// atomically places it at destPath. Network errors and 5xx/429 responses are
// retried up to 3 times with exponential backoff and jitter; any other
// non-200 status, a hash mismatch, or an oversized body fails immediately.
// Errors name the URL without its query string or credentials.
func Fetch(ctx context.Context, rawURL string, expectedSHA256 string, destPath string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return fmt.Errorf("invalid download URL: %w", err)
	}
	shown := Redact(parsed)
	if IsOffline(ctx) {
		return fmt.Errorf("fetch dependency %s: %w", shown, ErrOffline)
	}
	switch parsed.Scheme {
	case "http", "https":
	default:
		return &UnsupportedSchemeError{Scheme: parsed.Scheme, URL: shown}
	}

	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			base := retryBaseDelay << (attempt - 1)
			delay := base + time.Duration(rand.Int63n(int64(base/2)+1))
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(delay):
			}
		}
		lastErr = fetchOnce(ctx, rawURL, shown, expectedSHA256, destPath)
		var transient *transientError
		if lastErr == nil || !errors.As(lastErr, &transient) || ctx.Err() != nil {
			return lastErr
		}
	}
	return fmt.Errorf("fetch %s: all %d retries exhausted: %w", shown, maxRetries, lastErr)
}

// Redact renders u without query, fragment, or user info, any of which may
// carry credentials (presigned URLs, tokens).
func Redact(u *url.URL) string {
	c := *u
	c.User = nil
	c.RawQuery = ""
	c.ForceQuery = false
	c.Fragment = ""
	c.RawFragment = ""
	return c.String()
}

// UnsupportedSchemeError is returned when the URL has a scheme other than
// http or https. URL is redacted.
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

// StatusError is a non-200 HTTP response.
type StatusError struct {
	URL        string // redacted
	StatusCode int
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP GET %s: status %d %s", e.URL, e.StatusCode, http.StatusText(e.StatusCode))
}

// transientError marks a failure worth retrying.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// fetchOnce performs a single download attempt. rawURL is requested; shown is
// the redacted form used in errors.
func fetchOnce(ctx context.Context, rawURL, shown, expectedSHA256, destPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("creating request for %s: %w", shown, stripURL(err))
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return &transientError{fmt.Errorf("HTTP GET %s: %w", shown, stripURL(err))}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		statusErr := &StatusError{URL: shown, StatusCode: resp.StatusCode}
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return &transientError{statusErr}
		}
		return statusErr
	}

	budget, err := archive.NewBudget(downloadLimits)
	if err != nil {
		return err
	}
	if err := budget.Declared(shown, resp.ContentLength); err != nil {
		return err
	}

	// Create temp file in the same directory as destPath so os.Rename is atomic.
	tmp, err := os.CreateTemp(filepath.Dir(destPath), ".fetch-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	success := false
	defer func() {
		if !success {
			tmp.Close()
			os.Remove(tmpPath)
		}
	}()

	hasher := sha256.New()
	if _, err := budget.Copy(io.MultiWriter(tmp, hasher), resp.Body, shown); err != nil {
		if errors.Is(err, archive.ErrExceeded) || ctx.Err() != nil {
			return err
		}
		return &transientError{fmt.Errorf("downloading %s: %w", shown, stripURL(err))}
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

// stripURL drops the full URL that net/http embeds in *url.Error, keeping the
// operation's underlying cause.
func stripURL(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
