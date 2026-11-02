package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/config"
)

// TestSelfBuildSourcesCoverDeps guards the root mu.cue `//cmd/mu` target
// against going stale: every Go source and go:embed file that `go list
// -deps ./cmd/mu` reports from this module must be matched by the target's
// sources. A file missing from sources does not contribute to the action
// key, so editing it would produce a stale cache hit for `mu build //cmd/mu`.
func TestSelfBuildSourcesCoverDeps(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the root project config and shells out to go list")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not on PATH")
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	cfg, err := config.LoadOffline(repoRoot, true)
	if err != nil {
		t.Fatalf("loading root config: %v", err)
	}
	var sources []string
	found := false
	for _, tgt := range cfg.Targets {
		if tgt.Name == "//cmd/mu" {
			sources, found = tgt.Sources, true
			break
		}
	}
	if !found {
		t.Fatal("root mu.cue has no //cmd/mu target")
	}
	covered := make(map[string]bool, len(sources))
	for _, s := range sources {
		covered[filepath.ToSlash(filepath.Clean(s))] = true
	}

	cmd := exec.Command("go", "list", "-deps", "-json=Dir,GoFiles,EmbedFiles,Module", "./cmd/mu")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list: %v\n%s", err, stderr.String())
	}

	type pkg struct {
		Dir        string
		GoFiles    []string
		EmbedFiles []string
		Module     *struct{ Main bool }
	}
	dec := json.NewDecoder(bufio.NewReader(bytes.NewReader(out)))
	checked := 0
	for dec.More() {
		var p pkg
		if err := dec.Decode(&p); err != nil {
			t.Fatalf("decoding go list output: %v", err)
		}
		if p.Module == nil || !p.Module.Main {
			continue // stdlib or third-party: pinned by go.mod/go.sum
		}
		rel, err := filepath.Rel(repoRoot, p.Dir)
		if err != nil {
			t.Fatal(err)
		}
		files := append(append([]string{}, p.GoFiles...), p.EmbedFiles...)
		for _, f := range files {
			path := filepath.ToSlash(filepath.Join(rel, f))
			checked++
			if !covered[path] {
				t.Errorf("//cmd/mu sources in root mu.cue do not cover %s", path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("go list reported no module files for ./cmd/mu")
	}
	for _, f := range []string{"go.mod", "go.sum"} {
		if !covered[f] {
			t.Errorf("//cmd/mu sources in root mu.cue do not include %s", f)
		}
	}
}
