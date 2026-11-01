package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBlobPathRequiresFullDigest(t *testing.T) {
	dir := t.TempDir()
	full := strings.Repeat("c", 64)
	if got, want := blobPath(dir, "sha256:"+full), filepath.Join(dir, "blobs", "sha256", full); got != want {
		t.Fatalf("blobPath(full) = %q, want %q", got, want)
	}
	for _, ref := range []string{
		"sha256:abc",
		"sha256:../../../etc/passwd",
		"../..:" + full,
		"sha256:" + full + "/x",
		full,
	} {
		if got := blobPath(dir, ref); got != "" {
			t.Errorf("blobPath(%q) = %q, want empty", ref, got)
		}
	}
}
