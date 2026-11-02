package cas

import (
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

func TestVerifyReaderAcceptsMatchingContent(t *testing.T) {
	want, _ := ComputeDigest(strings.NewReader("hello"))
	got, err := io.ReadAll(VerifyReader(io.NopCloser(strings.NewReader("hello")), want, func() {
		t.Fatal("onMismatch called for matching content")
	}))
	if err != nil || string(got) != "hello" {
		t.Fatalf("ReadAll = %q, %v", got, err)
	}
}

func TestVerifyReaderRejectsMismatchAtEOF(t *testing.T) {
	want, _ := ComputeDigest(strings.NewReader("hello"))
	calls := 0
	r := VerifyReader(io.NopCloser(strings.NewReader("HELLO")), want, func() { calls++ })
	if _, err := io.ReadAll(r); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("ReadAll err = %v, want ErrDigestMismatch", err)
	}
	// The failure is sticky: a second read must not report a clean EOF.
	if _, err := r.Read(make([]byte, 1)); !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("second Read err = %v, want ErrDigestMismatch", err)
	}
	if calls != 1 {
		t.Fatalf("onMismatch called %d times, want 1", calls)
	}
}

func TestDiskStoreGetRejectsAndRemovesCorruptBlob(t *testing.T) {
	ctx := context.Background()
	s := mustDiskStore(t, t.TempDir())
	dgst, err := s.Put(ctx, strings.NewReader("good bytes"))
	if err != nil {
		t.Fatal(err)
	}
	p, _ := s.BlobPath(dgst)
	if err := os.WriteFile(p, []byte("rotten bytes"), 0o644); err != nil {
		t.Fatal(err)
	}

	rc, err := s.Get(ctx, dgst)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	_, err = io.ReadAll(rc)
	rc.Close()
	if !errors.Is(err, ErrDigestMismatch) {
		t.Fatalf("reading corrupt blob: err = %v, want ErrDigestMismatch", err)
	}
	if ok, _ := s.Has(ctx, dgst); ok {
		t.Fatal("corrupt blob still present after a failed read")
	}

	// The next Put of the real content restores the blob.
	if _, err := s.Put(ctx, strings.NewReader("good bytes")); err != nil {
		t.Fatal(err)
	}
	rc, err = s.Get(ctx, dgst)
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	if got, err := io.ReadAll(rc); err != nil || string(got) != "good bytes" {
		t.Fatalf("after repair: %q, %v", got, err)
	}
}
