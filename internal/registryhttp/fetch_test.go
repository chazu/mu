package registryhttp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chazu/mu/internal/archive"
)

func TestMain(m *testing.M) {
	retryBaseDelay = time.Millisecond
	os.Exit(m.Run())
}

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestFetch_UnsupportedScheme(t *testing.T) {
	tests := []struct {
		name string
		url  string
	}{
		{"ftp", "ftp://example.com/file.tar.gz"},
		{"file", "file:///etc/passwd"},
		{"gopher", "gopher://example.com/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dest := filepath.Join(t.TempDir(), "output.txt")
			err := Fetch(context.Background(), tt.url, "abc123", dest)
			if err == nil {
				t.Fatal("expected error for unsupported scheme, got nil")
			}

			useErr, ok := err.(*UnsupportedSchemeError)
			if !ok {
				t.Fatalf("expected *UnsupportedSchemeError, got %T: %v", err, err)
			}
			if useErr.URL != tt.url {
				t.Errorf("expected URL %q, got %q", tt.url, useErr.URL)
			}
		})
	}
}

func TestFetch_Success(t *testing.T) {
	body := []byte("hello, world\n")
	hash := sha256Hex(body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := Fetch(context.Background(), srv.URL, hash, dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("content mismatch: got %q, want %q", got, body)
	}
}

func TestFetch_HashMismatch(t *testing.T) {
	body := []byte("actual content")
	wrongHash := sha256Hex([]byte("different content"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := Fetch(context.Background(), srv.URL, wrongHash, dest)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	hmErr, ok := err.(*HashMismatchError)
	if !ok {
		t.Fatalf("expected *HashMismatchError, got %T: %v", err, err)
	}
	if hmErr.Expected != wrongHash {
		t.Errorf("expected hash %s, got %s", wrongHash, hmErr.Expected)
	}
	actualHash := sha256Hex(body)
	if hmErr.Actual != actualHash {
		t.Errorf("actual hash %s, got %s", actualHash, hmErr.Actual)
	}

	// Dest file should not exist.
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Error("dest file should not exist after hash mismatch")
	}
}

func TestFetch_HTTP404(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := Fetch(context.Background(), srv.URL, "abc123", dest)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404: %v", err)
	}
	if c := count.Load(); c != 1 {
		t.Errorf("4xx retried: %d requests, want 1", c)
	}
}

func TestFetch_HTTP500_RetriesThenFails(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := Fetch(context.Background(), srv.URL, "abc123", dest)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// 1 initial + 3 retries = 4 total.
	got := count.Load()
	if got != 4 {
		t.Errorf("expected 4 requests (1 + 3 retries), got %d", got)
	}
}

func TestFetch_ContextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Slow server — will be cancelled before completing.
		time.Sleep(10 * time.Second)
		w.Write([]byte("too late"))
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	// Cancel quickly.
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := Fetch(ctx, srv.URL, "abc123", dest)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestFetch_RetryAfterTransientError(t *testing.T) {
	body := []byte("retry success content")
	hash := sha256Hex(body)

	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := count.Add(1)
		if n == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := Fetch(context.Background(), srv.URL, hash, dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if string(got) != string(body) {
		t.Errorf("content mismatch: got %q, want %q", got, body)
	}

	if c := count.Load(); c != 2 {
		t.Errorf("expected 2 requests (1 fail + 1 success), got %d", c)
	}
}

func TestFetch_DestContentsMatch(t *testing.T) {
	// Large-ish body to ensure streaming works.
	body := []byte(fmt.Sprintf("line repeated 1000 times\n%s", strings.Repeat("data data data data\n", 1000)))
	hash := sha256Hex(body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "bigfile.bin")
	err := Fetch(context.Background(), srv.URL, hash, dest)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("reading dest: %v", err)
	}
	if len(got) != len(body) {
		t.Fatalf("size mismatch: got %d bytes, want %d bytes", len(got), len(body))
	}
	if string(got) != string(body) {
		t.Error("dest file contents do not match server response")
	}
}

func TestFetch_RetriesOnlyTransientStatuses(t *testing.T) {
	for status, wantRequests := range map[int]int32{
		http.StatusForbidden:          1,
		http.StatusUnauthorized:       1,
		http.StatusGone:               1,
		http.StatusTooManyRequests:    maxRetries + 1,
		http.StatusServiceUnavailable: maxRetries + 1,
	} {
		var count atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			w.WriteHeader(status)
		}))
		err := Fetch(context.Background(), srv.URL, "abc123", filepath.Join(t.TempDir(), "out"))
		srv.Close()
		var se *StatusError
		if !errors.As(err, &se) || se.StatusCode != status {
			t.Errorf("status %d: err = %v, want StatusError", status, err)
		}
		if got := count.Load(); got != wantRequests {
			t.Errorf("status %d: %d requests, want %d", status, got, wantRequests)
		}
	}
}

func TestFetch_503ThenSuccess(t *testing.T) {
	body := []byte("eventually")
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if count.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Write(body)
	}))
	defer srv.Close()
	dest := filepath.Join(t.TempDir(), "out")
	if err := Fetch(context.Background(), srv.URL, sha256Hex(body), dest); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if got, _ := os.ReadFile(dest); string(got) != string(body) {
		t.Fatalf("dest = %q", got)
	}
}

func TestFetch_OversizeBodyFails(t *testing.T) {
	t.Setenv(archive.EnvMaxBytes, "16")
	for _, declare := range []bool{true, false} {
		var count atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			count.Add(1)
			if !declare {
				w.Header().Set("Transfer-Encoding", "chunked") // no Content-Length
				w.(http.Flusher).Flush()
			}
			w.Write(make([]byte, 17))
		}))
		dir := t.TempDir()
		err := Fetch(context.Background(), srv.URL, "abc123", filepath.Join(dir, "out"))
		srv.Close()
		if !errors.Is(err, archive.ErrExceeded) {
			t.Fatalf("declare=%v: err = %v, want ErrExceeded", declare, err)
		}
		if c := count.Load(); c != 1 {
			t.Errorf("declare=%v: oversize download retried %d times", declare, c)
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("declare=%v: left files behind: %v", declare, entries)
		}
	}
}

func TestFetch_ErrorsOmitQueryString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	for _, raw := range []string{
		srv.URL + "/a.tgz?X-Amz-Signature=sekrit",
		"http://user:sekrit@127.0.0.1:1/a.tgz?token=sekrit", // connection refused: transient path
		"ftp://example.com/a.tgz?token=sekrit",
	} {
		err := Fetch(context.Background(), raw, "abc123", filepath.Join(t.TempDir(), "out"))
		if err == nil || strings.Contains(err.Error(), "sekrit") {
			t.Errorf("Fetch(%q) error leaks credentials: %v", raw, err)
		}
	}
}
