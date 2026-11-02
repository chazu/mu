package oci_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// PushActionResult builds the remote manifest and tag from a local per-key
// receipt, uploading only the output blobs the registry lacks and recording
// their real sizes on the layer descriptors.
func TestPushActionResultFromDiskStore(t *testing.T) {
	ctx := context.Background()
	local, err := cas.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a, _ := local.Put(ctx, strings.NewReader("alpha bytes"))
	b, _ := local.Put(ctx, strings.NewReader("bee"))
	key := cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("c", 64))}
	// Two names share one digest; the blob is uploaded once.
	result := &cas.ActionResult{Version: cas.ActionResultVersion, Outputs: map[string]cas.Digest{"a": a, "b": b, "a-copy": a}, ExitCode: 0}
	if err := local.PutActionResult(ctx, key, result); err != nil {
		t.Fatal(err)
	}

	remote, reg := newTestStore(t)
	// Pre-seed one blob: it must not be pushed again (Push would report
	// ErrAlreadyExists, which is tolerated, but Exists should short-circuit).
	if _, err := remote.Put(ctx, strings.NewReader("bee")); err != nil {
		t.Fatal(err)
	}
	if err := remote.PushActionResult(ctx, local, key, result); err != nil {
		t.Fatalf("PushActionResult: %v", err)
	}

	desc, err := reg.Resolve(ctx, oci.ActionTag(key))
	if err != nil {
		t.Fatalf("tag not created: %v", err)
	}
	var manifest ocispec.Manifest
	if err := json.Unmarshal(reg.blobs[desc.Digest], &manifest); err != nil {
		t.Fatal(err)
	}
	if len(manifest.Layers) != 3 {
		t.Fatalf("layers = %d, want 3", len(manifest.Layers))
	}
	for _, l := range manifest.Layers {
		want := int64(len("alpha bytes"))
		if l.Digest.Encoded() == b.Hash {
			want = int64(len("bee"))
		}
		if l.Size != want {
			t.Fatalf("layer %s size = %d, want %d", l.Annotations[ocispec.AnnotationTitle], l.Size, want)
		}
	}
	got, err := remote.GetActionResult(ctx, key)
	if err != nil || got == nil || got.Outputs["a"] != a || got.Outputs["b"] != b {
		t.Fatalf("remote result = %+v, %v", got, err)
	}
	rc, err := remote.Get(ctx, a)
	if err != nil {
		t.Fatalf("output blob not pushed: %v", err)
	}
	rc.Close()
}

func TestPushActionResultMissingLocalBlob(t *testing.T) {
	ctx := context.Background()
	local, err := cas.NewDiskStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	key := cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("d", 64))}
	result := &cas.ActionResult{Outputs: map[string]cas.Digest{"gone": cas.NewSHA256(strings.Repeat("e", 64))}}
	remote, reg := newTestStore(t)
	if err := remote.PushActionResult(ctx, local, key, result); err == nil {
		t.Fatal("pushed a result whose output blob is missing locally")
	}
	if _, err := reg.Resolve(ctx, oci.ActionTag(key)); err == nil {
		t.Fatal("tag created for an incomplete result")
	}
}
