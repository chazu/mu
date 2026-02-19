package disk_test

import (
	"context"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/cas/disk"
)

func newTestStore(t *testing.T) *disk.DiskStore {
	t.Helper()
	s, err := disk.New(t.TempDir())
	if err != nil {
		t.Fatalf("disk.New: %v", err)
	}
	return s
}

func TestPutGetRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	content := "round-trip content"

	dgst, err := s.Put(ctx, strings.NewReader(content))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	rc, err := s.Get(ctx, dgst)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	defer rc.Close()

	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if string(got) != content {
		t.Errorf("Get returned %q, want %q", got, content)
	}
}

func TestHas(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dgst, err := s.Put(ctx, strings.NewReader("exists"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	ok, err := s.Has(ctx, dgst)
	if err != nil {
		t.Fatalf("Has (existing): %v", err)
	}
	if !ok {
		t.Error("Has returned false for existing blob")
	}

	missing := cas.NewSHA256("0000000000000000000000000000000000000000000000000000000000000000")
	ok, err = s.Has(ctx, missing)
	if err != nil {
		t.Fatalf("Has (missing): %v", err)
	}
	if ok {
		t.Error("Has returned true for missing blob")
	}
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	dgst, err := s.Put(ctx, strings.NewReader("to-delete"))
	if err != nil {
		t.Fatalf("Put: %v", err)
	}

	if err := s.Delete(ctx, dgst); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	ok, err := s.Has(ctx, dgst)
	if err != nil {
		t.Fatalf("Has after Delete: %v", err)
	}
	if ok {
		t.Error("Has returned true after Delete")
	}
}

func TestConcurrentPut(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	content := "concurrent-content"

	var wg sync.WaitGroup
	digests := make([]cas.Digest, 10)
	errs := make([]error, 10)

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			digests[idx], errs[idx] = s.Put(ctx, strings.NewReader(content))
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d Put error: %v", i, err)
		}
	}

	// All digests must be identical.
	for i := 1; i < 10; i++ {
		if digests[i] != digests[0] {
			t.Errorf("digest[%d] = %s, want %s", i, digests[i], digests[0])
		}
	}

	// Blob must exist exactly once.
	ok, err := s.Has(ctx, digests[0])
	if err != nil {
		t.Fatalf("Has: %v", err)
	}
	if !ok {
		t.Error("blob missing after concurrent Puts")
	}
}

func TestActionResultRoundtrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	key := cas.ActionKey{Digest: cas.NewSHA256("aabbccdd00112233445566778899aabbccdd00112233445566778899aabbccdd")}
	want := &cas.ActionResult{
		ExitCode: 0,
		Outputs: map[string]cas.Digest{
			"out": cas.NewSHA256("1122334455667788990011223344556677889900112233445566778899001122"),
		},
	}

	if err := s.PutActionResult(ctx, key, want); err != nil {
		t.Fatalf("PutActionResult: %v", err)
	}

	got, err := s.GetActionResult(ctx, key)
	if err != nil {
		t.Fatalf("GetActionResult: %v", err)
	}
	if got == nil {
		t.Fatal("GetActionResult returned nil")
	}
	if got.ExitCode != want.ExitCode {
		t.Errorf("ExitCode = %d, want %d", got.ExitCode, want.ExitCode)
	}
	if len(got.Outputs) != len(want.Outputs) {
		t.Fatalf("Outputs length = %d, want %d", len(got.Outputs), len(want.Outputs))
	}
	for k, v := range want.Outputs {
		if got.Outputs[k] != v {
			t.Errorf("Outputs[%q] = %s, want %s", k, got.Outputs[k], v)
		}
	}
}

func TestGetActionResultMiss(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	key := cas.ActionKey{Digest: cas.NewSHA256("ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00ff00")}
	got, err := s.GetActionResult(ctx, key)
	if err != nil {
		t.Fatalf("GetActionResult: %v", err)
	}
	if got != nil {
		t.Errorf("expected nil result for missing key, got %+v", got)
	}
}

func TestPathTraversalRejected(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	cases := []struct {
		name string
		hash string
	}{
		{"directory traversal", "../../../etc/passwd"},
		{"short hash", "a"},
		{"empty hash", ""},
		{"uppercase hex", "AABB"},
		{"non-hex chars", "zzzzzz"},
		{"mixed valid and slash", "aa/bb"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dgst := cas.NewSHA256(tc.hash)

			_, err := s.Get(ctx, dgst)
			if err == nil {
				t.Error("Get: expected error for invalid hash")
			}

			_, err = s.Has(ctx, dgst)
			if err == nil {
				t.Error("Has: expected error for invalid hash")
			}

			err = s.Delete(ctx, dgst)
			if err == nil {
				t.Error("Delete: expected error for invalid hash")
			}
		})
	}
}

func TestPutIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	content := "idempotent-content"

	d1, err := s.Put(ctx, strings.NewReader(content))
	if err != nil {
		t.Fatalf("first Put: %v", err)
	}

	d2, err := s.Put(ctx, strings.NewReader(content))
	if err != nil {
		t.Fatalf("second Put: %v", err)
	}

	if d1 != d2 {
		t.Errorf("digests differ: %s vs %s", d1, d2)
	}
}
