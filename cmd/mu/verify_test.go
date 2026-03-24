package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	specs "github.com/opencontainers/image-spec/specs-go"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// setupTestCache creates a minimal OCI layout cache with blobs and an index.
func setupTestCache(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	blobDir := filepath.Join(dir, "blobs", "sha256")
	if err := os.MkdirAll(blobDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Write a valid blob.
	content := []byte("hello world")
	h := sha256.Sum256(content)
	hash := hex.EncodeToString(h[:])
	if err := os.WriteFile(filepath.Join(blobDir, hash), content, 0o644); err != nil {
		t.Fatal(err)
	}

	// Write an empty OCI index.
	idx := ocispec.Index{
		Versioned: specs.Versioned{SchemaVersion: 2},
		MediaType: ocispec.MediaTypeImageIndex,
	}
	idxBytes, _ := json.Marshal(idx)
	if err := os.WriteFile(filepath.Join(dir, "index.json"), idxBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	// OCI layout file.
	if err := os.WriteFile(filepath.Join(dir, "oci-layout"),
		[]byte(`{"imageLayoutVersion":"1.0.0"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func TestVerifyHealthyCache(t *testing.T) {
	dir := setupTestCache(t)

	// Override cachePath for test.
	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	// Create the cache at tmpHome/.mu/cache by symlinking.
	muDir := filepath.Join(tmpHome, ".mu")
	os.MkdirAll(muDir, 0o755)
	os.Symlink(dir, filepath.Join(muDir, "cache"))
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	code := runVerify([]string{})
	if code != 0 {
		t.Errorf("runVerify() = %d, want 0 for healthy cache", code)
	}
}

func TestVerifyCorruptBlob(t *testing.T) {
	dir := setupTestCache(t)

	// Corrupt a blob by writing wrong content under a valid hash name.
	blobDir := filepath.Join(dir, "blobs", "sha256")
	content := []byte("original")
	h := sha256.Sum256(content)
	hash := hex.EncodeToString(h[:])
	// Write different content under this hash.
	if err := os.WriteFile(filepath.Join(blobDir, hash), []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	muDir := filepath.Join(tmpHome, ".mu")
	os.MkdirAll(muDir, 0o755)
	os.Symlink(dir, filepath.Join(muDir, "cache"))
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	code := runVerify([]string{})
	if code != 1 {
		t.Errorf("runVerify() = %d, want 1 for corrupt cache", code)
	}
}

func TestVerifyFixCorruptBlob(t *testing.T) {
	dir := setupTestCache(t)

	blobDir := filepath.Join(dir, "blobs", "sha256")
	content := []byte("to-corrupt")
	h := sha256.Sum256(content)
	hash := hex.EncodeToString(h[:])
	corruptPath := filepath.Join(blobDir, hash)
	if err := os.WriteFile(corruptPath, []byte("bad"), 0o644); err != nil {
		t.Fatal(err)
	}

	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	muDir := filepath.Join(tmpHome, ".mu")
	os.MkdirAll(muDir, 0o755)
	os.Symlink(dir, filepath.Join(muDir, "cache"))
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	code := runVerify([]string{"--fix"})
	if code != 1 {
		t.Errorf("runVerify() = %d, want 1 for corrupt cache", code)
	}

	// Corrupt blob should be deleted.
	if _, err := os.Stat(corruptPath); !os.IsNotExist(err) {
		t.Error("corrupt blob was not removed by --fix")
	}
}

func TestVerifyJsonOutput(t *testing.T) {
	dir := setupTestCache(t)

	origHome := os.Getenv("HOME")
	tmpHome := t.TempDir()
	muDir := filepath.Join(tmpHome, ".mu")
	os.MkdirAll(muDir, 0o755)
	os.Symlink(dir, filepath.Join(muDir, "cache"))
	os.Setenv("HOME", tmpHome)
	defer os.Setenv("HOME", origHome)

	// Capture stdout.
	origStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	code := runVerify([]string{"--json"})

	w.Close()
	os.Stdout = origStdout

	if code != 0 {
		t.Errorf("runVerify(--json) = %d, want 0", code)
	}

	var result map[string]any
	if err := json.NewDecoder(r).Decode(&result); err != nil {
		t.Fatalf("failed to parse JSON output: %v", err)
	}

	if result["ok"].(float64) < 1 {
		t.Errorf("expected at least 1 ok blob, got %v", result["ok"])
	}
}
