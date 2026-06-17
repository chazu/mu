package coordinator

import (
	"context"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
)

func newTestStore(t *testing.T) cas.Store {
	t.Helper()
	s, err := oci.NewLocal(t.TempDir())
	if err != nil {
		t.Fatalf("oci.NewLocal: %v", err)
	}
	return s
}

func TestRegisterAndLookupRoundtrip(t *testing.T) {
	store := newTestStore(t)
	reg := NewToolchainRegistry(store)
	ctx := context.Background()

	manifest := &ToolchainManifest{
		Name:    "go",
		Version: "1.25.7",
		Artifacts: map[string]string{
			"bin/go":    "sha256:aaa111",
			"bin/gofmt": "sha256:bbb222",
		},
	}

	if err := reg.Register(ctx, manifest); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got, err := reg.Lookup(ctx, "go", "1.25.7")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("Lookup returned nil")
	}
	if got.Name != "go" {
		t.Errorf("Name = %q, want %q", got.Name, "go")
	}
	if got.Version != "1.25.7" {
		t.Errorf("Version = %q, want %q", got.Version, "1.25.7")
	}
	if len(got.Artifacts) != 2 {
		t.Errorf("Artifacts length = %d, want 2", len(got.Artifacts))
	}
	if got.Artifacts["bin/go"] != "sha256:aaa111" {
		t.Errorf("Artifacts[bin/go] = %q, want %q", got.Artifacts["bin/go"], "sha256:aaa111")
	}
}

func TestLookupMiss(t *testing.T) {
	store := newTestStore(t)
	reg := NewToolchainRegistry(store)
	ctx := context.Background()

	got, err := reg.Lookup(ctx, "rust", "1.80.0")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got != nil {
		t.Fatalf("expected nil, got %+v", got)
	}
}

func TestInMemoryGet(t *testing.T) {
	store := newTestStore(t)
	reg := NewToolchainRegistry(store)
	ctx := context.Background()

	manifest := &ToolchainManifest{
		Name:    "go",
		Version: "1.25.7",
		Artifacts: map[string]string{
			"bin/go": "sha256:aaa111",
		},
	}

	if err := reg.Register(ctx, manifest); err != nil {
		t.Fatalf("Register: %v", err)
	}

	got := reg.Get("go")
	if got == nil {
		t.Fatal("Get returned nil after Register")
	}
	if got.Name != "go" || got.Version != "1.25.7" {
		t.Errorf("Get returned unexpected manifest: %+v", got)
	}

	// Unregistered toolchain returns nil.
	if reg.Get("rust") != nil {
		t.Error("Get(rust) should return nil")
	}
}

func TestArtifactsMap(t *testing.T) {
	store := newTestStore(t)
	reg := NewToolchainRegistry(store)
	ctx := context.Background()

	manifest := &ToolchainManifest{
		Name:    "go",
		Version: "1.25.7",
		Artifacts: map[string]string{
			"bin/go":    "sha256:aaa111",
			"bin/gofmt": "sha256:bbb222",
		},
	}

	if err := reg.Register(ctx, manifest); err != nil {
		t.Fatalf("Register: %v", err)
	}

	arts := reg.ArtifactsMap("go")
	if arts == nil {
		t.Fatal("ArtifactsMap returned nil")
	}
	if len(arts) != 2 {
		t.Errorf("ArtifactsMap length = %d, want 2", len(arts))
	}
	if arts["bin/go"] != "sha256:aaa111" {
		t.Errorf("ArtifactsMap[bin/go] = %q, want %q", arts["bin/go"], "sha256:aaa111")
	}

	// Unknown toolchain returns nil.
	if reg.ArtifactsMap("rust") != nil {
		t.Error("ArtifactsMap(rust) should return nil")
	}
}

func TestPersistenceAcrossInstances(t *testing.T) {
	store := newTestStore(t)
	ctx := context.Background()

	// Register with the first registry instance.
	reg1 := NewToolchainRegistry(store)
	manifest := &ToolchainManifest{
		Name:    "go",
		Version: "1.25.7",
		Artifacts: map[string]string{
			"bin/go": "sha256:aaa111",
		},
	}
	if err := reg1.Register(ctx, manifest); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Create a second registry with the same CAS store — no in-memory cache.
	reg2 := NewToolchainRegistry(store)

	// In-memory Get should return nil on the new instance.
	if reg2.Get("go") != nil {
		t.Error("new registry Get should return nil before Lookup")
	}

	// Lookup should find it via CAS.
	got, err := reg2.Lookup(ctx, "go", "1.25.7")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if got == nil {
		t.Fatal("Lookup returned nil on second registry instance")
	}
	if got.Name != "go" || got.Version != "1.25.7" {
		t.Errorf("unexpected manifest: %+v", got)
	}
	if got.Artifacts["bin/go"] != "sha256:aaa111" {
		t.Errorf("Artifacts[bin/go] = %q, want %q", got.Artifacts["bin/go"], "sha256:aaa111")
	}
}
