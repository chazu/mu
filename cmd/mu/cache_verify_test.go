package main

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
)

// verifyFixture writes a local cache under a temp HOME holding two results:
// one whose output is intact and one whose output blob is then corrupted.
func verifyFixture(t *testing.T) (store *cas.DiskStore, good, bad cas.ActionKey, corrupt cas.Digest) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	store, err := cas.NewDiskStore(filepath.Join(home, ".mu", "cache"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	goodOut, _ := store.Put(ctx, strings.NewReader("good output"))
	corrupt, _ = store.Put(ctx, strings.NewReader("soon corrupt"))
	good = cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("1", 64))}
	bad = cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat("2", 64))}
	if err := store.PutActionResult(ctx, good, &cas.ActionResult{Outputs: map[string]cas.Digest{"out": goodOut}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutActionResult(ctx, bad, &cas.ActionResult{Outputs: map[string]cas.Digest{"out": corrupt, "also": goodOut}}); err != nil {
		t.Fatal(err)
	}
	p, _ := store.BlobPath(corrupt)
	if err := os.WriteFile(p, []byte("tampered"), 0o644); err != nil {
		t.Fatal(err)
	}
	return store, good, bad, corrupt
}

func verifyJSON(t *testing.T, args ...string) (int, map[string]any) {
	t.Helper()
	var code int
	out := captureStdout(t, func() { code = runCacheVerify(append([]string{"--json"}, args...)) })
	var report map[string]any
	if err := json.Unmarshal(out, &report); err != nil {
		t.Fatalf("verify output %q: %v", out, err)
	}
	return code, report
}

func captureStdout(t *testing.T, fn func()) []byte {
	t.Helper()
	original := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan []byte)
	go func() { b, _ := io.ReadAll(r); r.Close(); done <- b }()
	fn()
	w.Close()
	os.Stdout = original
	return <-done
}

func TestCacheVerifyReportsAndFixesCorruptBlob(t *testing.T) {
	store, good, bad, corrupt := verifyFixture(t)
	ctx := context.Background()

	code, report := verifyJSON(t)
	if code != exitFail || report["corrupt"].(float64) != 1 || report["broken_results"].(float64) != 1 {
		t.Fatalf("verify = %d, %v; want exit 1 with one corrupt blob and one broken result", code, report)
	}
	if r, _ := store.GetActionResult(ctx, bad); r == nil {
		t.Fatal("verify without --fix removed a result")
	}

	code, report = verifyJSON(t, "--fix")
	if code != exitOK || report["removed_blobs"].(float64) != 1 || report["removed_results"].(float64) != 1 {
		t.Fatalf("verify --fix = %d, %v; want exit 0 after removing the blob and its result", code, report)
	}
	if p, _ := store.BlobPath(corrupt); exists(p) {
		t.Fatal("--fix kept the corrupt blob")
	}
	if p, _ := store.ActionResultPath(bad); exists(p) {
		t.Fatal("--fix kept the result referencing the corrupt blob")
	}
	if r, _ := store.GetActionResult(ctx, good); r == nil {
		t.Fatal("--fix removed an intact result")
	}

	code, report = verifyJSON(t)
	if code != exitOK || report["corrupt"].(float64) != 0 || report["broken_results"].(float64) != 0 {
		t.Fatalf("verify after --fix = %d, %v; want a clean cache", code, report)
	}
}

// A result whose output blob is gone is inconsistent too: reported, and
// removed by --fix.
func TestCacheVerifyFixRemovesResultsWithMissingBlobs(t *testing.T) {
	store, _, bad, corrupt := verifyFixture(t)
	if err := store.Delete(context.Background(), corrupt); err != nil {
		t.Fatal(err)
	}
	if code, report := verifyJSON(t); code != exitFail || report["missing"].(float64) != 1 {
		t.Fatalf("verify = %d, %v; want exit 1 with one missing blob", code, report)
	}
	if code, _ := verifyJSON(t, "--fix"); code != exitOK {
		t.Fatalf("verify --fix exit = %d", code)
	}
	if p, _ := store.ActionResultPath(bad); exists(p) {
		t.Fatal("--fix kept the result referencing the missing blob")
	}
}

func TestTopLevelVerifyIsMoved(t *testing.T) {
	if code := run("verify", nil); code != exitUsage {
		t.Fatalf("mu verify exit = %d, want %d", code, exitUsage)
	}
}
