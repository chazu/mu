package oci_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// Malformed identities are rejected before any registry call and are not
// treated as outages, so the store stays usable for valid lookups.
func TestStoreRejectsIncompleteDigestsBeforeRegistry(t *testing.T) {
	s, reg := newTestStore(t)
	ctx := context.Background()
	bad := []cas.Digest{
		cas.NewSHA256("abc123"),
		cas.NewSHA256("../../" + strings.Repeat("a", 58)),
		{Algorithm: "sha512", Hash: strings.Repeat("a", 128)},
		{},
	}
	for _, d := range bad {
		if _, err := s.Has(ctx, d); !errors.Is(err, cas.ErrInvalidDigest) {
			t.Errorf("Has(%q) = %v, want ErrInvalidDigest", d, err)
		}
		if _, err := s.Get(ctx, d); !errors.Is(err, cas.ErrInvalidDigest) {
			t.Errorf("Get(%q) = %v, want ErrInvalidDigest", d, err)
		}
		if err := s.Delete(ctx, d); !errors.Is(err, cas.ErrInvalidDigest) {
			t.Errorf("Delete(%q) = %v, want ErrInvalidDigest", d, err)
		}
		if _, err := s.GetActionResult(ctx, cas.ActionKey{Digest: d}); !errors.Is(err, cas.ErrInvalidDigest) {
			t.Errorf("GetActionResult(%q) = %v, want ErrInvalidDigest", d, err)
		}
	}
	if len(reg.deleted) != 0 {
		t.Fatalf("registry saw deletes for invalid digests: %v", reg.deleted)
	}

	good, err := s.Put(ctx, strings.NewReader("still healthy"))
	if err != nil {
		t.Fatalf("Put after invalid lookups: %v", err)
	}
	if ok, err := s.Has(ctx, good); err != nil || !ok {
		t.Fatalf("Has(valid) = %v, %v", ok, err)
	}
}

func TestPutActionResultRejectsIncompleteOutputDigest(t *testing.T) {
	s, _ := newTestStore(t)
	key := cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("1", 64))}
	err := s.PutActionResult(context.Background(), key, &cas.ActionResult{Outputs: map[string]cas.Digest{"out": cas.NewSHA256("short")}})
	if !errors.Is(err, cas.ErrInvalidDigest) {
		t.Fatalf("PutActionResult = %v, want ErrInvalidDigest", err)
	}
}

// A receipt written by another client is untrusted: an output identity that
// is not a full digest must never reach restore.
func TestGetActionResultRejectsMalformedRemoteReceipt(t *testing.T) {
	s, reg := newTestStore(t)
	ctx := context.Background()
	keyHash := strings.Repeat("2", 64)

	config, _ := json.Marshal(map[string]any{
		"outputs":   map[string]any{"out": map[string]string{"Algorithm": "sha256", "Hash": "../../etc/passwd"}},
		"exit_code": 0,
	})
	configDesc := ocispec.Descriptor{MediaType: "application/vnd.mu.action.v1+json", Digest: godigest.FromBytes(config), Size: int64(len(config))}
	if err := reg.Push(ctx, configDesc, bytes.NewReader(config)); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(ocispec.Manifest{MediaType: ocispec.MediaTypeImageManifest, Config: configDesc, Layers: []ocispec.Descriptor{}})
	manifestDesc := ocispec.Descriptor{MediaType: ocispec.MediaTypeImageManifest, Digest: godigest.FromBytes(manifest), Size: int64(len(manifest))}
	if err := reg.Push(ctx, manifestDesc, bytes.NewReader(manifest)); err != nil {
		t.Fatal(err)
	}
	if err := reg.Tag(ctx, manifestDesc, "action-sha256-"+keyHash); err != nil {
		t.Fatal(err)
	}

	got, err := s.GetActionResult(ctx, cas.ActionKey{Digest: cas.NewSHA256(keyHash)})
	if !errors.Is(err, cas.ErrInvalidDigest) || got != nil {
		t.Fatalf("GetActionResult = %+v, %v; want ErrInvalidDigest", got, err)
	}
	if errors.Is(err, cas.ErrUnavailable) {
		t.Fatalf("corrupt receipt reported as outage: %v", err)
	}
}
