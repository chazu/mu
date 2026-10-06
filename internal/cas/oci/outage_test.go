package oci

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chazu/mu/internal/cas"
)

func TestOptionalRegistryLookupHasOwnBoundAndQuarantinesOutage(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { requests.Add(1); <-r.Context().Done() }))
	defer server.Close()
	store, err := buildOCIStore(cas.BackendSpec{Registry: strings.TrimPrefix(server.URL, "http://") + "/cache"})
	if err != nil {
		t.Fatal(err)
	}
	key := cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("a", 64))}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		start := time.Now()
		_, err := store.GetActionResult(ctx, key)
		elapsed := time.Since(start)
		cancel()
		if err == nil {
			t.Fatal("expected unavailable registry")
		}
		if elapsed > 2700*time.Millisecond {
			t.Errorf("lookup %d used caller watchdog instead of a bounded cache probe: %s", i, elapsed)
		}
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("unhealthy registry probed %d times, want one", got)
	}
}

func TestCallerCancellationDoesNotQuarantineHealthyRegistry(t *testing.T) {
	var stall atomic.Bool
	stall.Store(true)
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if stall.Load() {
			<-r.Context().Done()
			return
		}
		w.WriteHeader(404)
	}))
	defer server.Close()
	store, err := buildOCIStore(cas.BackendSpec{Registry: strings.TrimPrefix(server.URL, "http://") + "/cache"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	_, err = store.GetActionResult(ctx, cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("a", 64))})
	cancel()
	if err == nil {
		t.Fatal("expected caller cancellation")
	}
	stall.Store(false)
	result, err := store.GetActionResult(context.Background(), cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("a", 64))})
	if err != nil || result != nil || requests.Load() != 2 {
		t.Fatalf("caller cancellation poisoned registry: %v, %v, calls=%d", result, err, requests.Load())
	}
}
