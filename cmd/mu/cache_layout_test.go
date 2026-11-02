package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
)

// The cache subcommands read action results from the per-key ac/ layout:
// clean keeps every blob a result (or a toolchain manifest) references and
// removes the rest.
func TestCacheCleanUsesPerKeyActionResults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir()) // outside any project: the default cache
	ctx := context.Background()
	store, err := cas.NewDiskStore(filepath.Join(home, ".mu", "cache"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(store.Root(), home) {
		t.Fatalf("cache path %s escaped test HOME", store.Root())
	}

	out, _ := store.Put(ctx, strings.NewReader("live output"))
	artifact, _ := store.Put(ctx, strings.NewReader("toolchain binary"))
	tc, _ := json.Marshal(toolchainManifest{Name: "tool", Version: "1.0", Artifacts: map[string]string{"bin/tool": artifact.String()}})
	tcManifest, _ := store.Put(ctx, strings.NewReader(string(tc)))
	garbage, _ := store.Put(ctx, strings.NewReader("garbage"))

	key := func(c string) cas.ActionKey { return cas.ActionKey{Digest: cas.NewSHA256(strings.Repeat(c, 64))} }
	if err := store.PutActionResult(ctx, key("a"), &cas.ActionResult{Outputs: map[string]cas.Digest{"out": out}}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutActionResult(ctx, key("b"), &cas.ActionResult{Outputs: map[string]cas.Digest{"manifest": tcManifest}}); err != nil {
		t.Fatal(err)
	}

	actions, err := readCachedActions(store, true)
	if err != nil || len(actions) != 2 || actions[0].Tag != "action-sha256-"+strings.Repeat("a", 64) {
		t.Fatalf("readCachedActions = %+v, %v", actions, err)
	}

	devnull, _ := os.Open(os.DevNull)
	stdout := os.Stdout
	os.Stdout = devnull
	code := runCacheClean([]string{"--json"})
	os.Stdout = stdout
	devnull.Close()
	if code != 0 {
		t.Fatalf("cache clean exit %d", code)
	}
	for _, d := range []cas.Digest{out, artifact, tcManifest} {
		if ok, _ := store.Has(ctx, d); !ok {
			t.Fatalf("clean removed reachable blob %s", d)
		}
	}
	if ok, _ := store.Has(ctx, garbage); ok {
		t.Fatal("clean kept unreachable blob")
	}

	// An unreadable root aborts clean rather than guessing.
	p, _ := store.ActionResultPath(key("c"))
	if err := os.WriteFile(p, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := reachableBlobs(store.Root()); err == nil {
		t.Fatal("reachableBlobs ignored a corrupt root")
	}
}
