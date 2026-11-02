package oci

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/registryhttp"
)

func TestNewRemote_RejectsEmptyRef(t *testing.T) {
	if _, err := NewRemote(""); err == nil {
		t.Fatal("expected error for empty reference")
	}
}

func TestNewRemote_AcceptsValidReference(t *testing.T) {
	// Construction must not contact the registry — it's lazy until the
	// first fetch. Any parseable reference should succeed.
	store, err := NewRemote("ghcr.io/example/mu-cache")
	if err != nil {
		t.Fatalf("expected valid reference to succeed: %v", err)
	}
	if store == nil {
		t.Fatal("nil store for valid reference")
	}
}

func TestIsLocalRegistry(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{"localhost:5000/repo", true},
		{"localhost/repo", true},
		{"127.0.0.1:5000/repo", true},
		{"ghcr.io/acme/cache", false},
		{"registry.example.com:5000/repo", false},
	}
	for _, c := range cases {
		if got := IsLocalRegistry(c.ref); got != c.want {
			t.Errorf("IsLocalRegistry(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}

// A silent registry must not stall a build: metadata lookups carry their own
// bound, shorter than any caller deadline.
func TestRemoteLookupHasOwnBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer server.Close()
	store, err := NewRemote(strings.TrimPrefix(server.URL, "http://") + "/cache")
	if err != nil {
		t.Fatal(err)
	}
	if store.lookupTimeout != registryhttp.LookupTimeout {
		t.Fatalf("remote lookup bound = %s, want %s", store.lookupTimeout, registryhttp.LookupTimeout)
	}
	store.lookupTimeout = 500 * time.Millisecond // keep the test fast
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	_, err = store.GetActionResult(ctx, cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("a", 64))})
	if err == nil {
		t.Fatal("expected unavailable registry")
	}
	if elapsed := time.Since(start); elapsed > 2700*time.Millisecond {
		t.Errorf("lookup used caller watchdog instead of its own bound: %s", elapsed)
	}
}

// A missing blob is a confirmed miss (cas.ErrNotFound), distinct from an
// unreachable registry.
func TestRemoteGetMissingBlobIsNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }))
	defer server.Close()
	store, err := NewRemote(strings.TrimPrefix(server.URL, "http://") + "/cache")
	if err != nil {
		t.Fatal(err)
	}
	_, err = store.Get(context.Background(), cas.NewSHA256(strings.Repeat("b", 64)))
	if !errors.Is(err, cas.ErrNotFound) {
		t.Fatalf("Get missing = %v, want cas.ErrNotFound", err)
	}
}

// Registry bytes that do not hash to the requested digest fail the read at
// EOF; a hostile or broken registry cannot pass content through Get.
func TestRemoteGetRejectsTamperedBlob(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("tampered")) }))
	defer server.Close()
	store, err := NewRemote(strings.TrimPrefix(server.URL, "http://") + "/cache")
	if err != nil {
		t.Fatal(err)
	}
	rc, err := store.Get(context.Background(), cas.NewSHA256(strings.Repeat("c", 64)))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()
	if _, err := io.ReadAll(rc); !errors.Is(err, cas.ErrDigestMismatch) {
		t.Fatalf("ReadAll = %v, want cas.ErrDigestMismatch", err)
	}
}
