package oci

import (
	"testing"

	"github.com/chau/mu/internal/cas"
)

func TestBuildDiskStore_RejectsEmptyPath(t *testing.T) {
	if _, err := buildDiskStore(cas.BackendSpec{Type: "disk"}); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestBuildOCIStore_RejectsEmptyRegistry(t *testing.T) {
	if _, err := buildOCIStore(cas.BackendSpec{Type: "oci"}); err == nil {
		t.Fatal("expected error for empty registry")
	}
}

func TestBuildOCIStore_AcceptsValidReference(t *testing.T) {
	// Construction must not contact the registry — it's lazy until the
	// first push/fetch. Any parseable reference should succeed.
	store, err := buildOCIStore(cas.BackendSpec{
		Type:     "oci",
		Registry: "ghcr.io/example/mu-cache",
	})
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
		if got := isLocalRegistry(c.ref); got != c.want {
			t.Errorf("isLocalRegistry(%q) = %v, want %v", c.ref, got, c.want)
		}
	}
}
