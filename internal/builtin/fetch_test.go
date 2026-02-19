package builtin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func sha256Hex(data []byte) string {
	h := sha256.Sum256(data)
	return hex.EncodeToString(h[:])
}

func TestForgeFetch_Success(t *testing.T) {
	body := []byte("hello, world\n")
	hash := sha256Hex(body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := ForgeFetch(context.Background(), srv.URL, hash, dest)
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

func TestForgeFetch_HashMismatch(t *testing.T) {
	body := []byte("actual content")
	wrongHash := sha256Hex([]byte("different content"))

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := ForgeFetch(context.Background(), srv.URL, wrongHash, dest)
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

func TestForgeFetch_HTTP404(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := ForgeFetch(context.Background(), srv.URL, "abc123", dest)
	if err == nil {
		t.Fatal("expected error for 404, got nil")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404: %v", err)
	}
}

func TestForgeFetch_HTTP500_RetriesThenFails(t *testing.T) {
	var count atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "output.txt")
	err := ForgeFetch(context.Background(), srv.URL, "abc123", dest)
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	// 1 initial + 3 retries = 4 total.
	got := count.Load()
	if got != 4 {
		t.Errorf("expected 4 requests (1 + 3 retries), got %d", got)
	}
}

func TestForgeFetch_ContextCancellation(t *testing.T) {
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
	err := ForgeFetch(ctx, srv.URL, "abc123", dest)
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}

func TestForgeFetch_RetryAfterTransientError(t *testing.T) {
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
	err := ForgeFetch(context.Background(), srv.URL, hash, dest)
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

func TestForgeFetch_DestContentsMatch(t *testing.T) {
	// Large-ish body to ensure streaming works.
	body := []byte(fmt.Sprintf("line repeated 1000 times\n%s", strings.Repeat("data data data data\n", 1000)))
	hash := sha256Hex(body)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(body)
	}))
	defer srv.Close()

	dest := filepath.Join(t.TempDir(), "bigfile.bin")
	err := ForgeFetch(context.Background(), srv.URL, hash, dest)
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
