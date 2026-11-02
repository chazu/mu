package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// A configured cache.path is the one cache every command uses: a build writes
// there, `mu cache size` reads there, and nothing appears in ~/.mu/cache.
func TestConfiguredCachePathIsHonouredEverywhere(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	t.Setenv("HOME", home)
	cacheDir := filepath.Join(root, "custom-cache")
	project := filepath.Join(root, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	config := fmt.Sprintf(`package mu
cache: path: %q
targets: [{
	target:    "//out"
	toolchain: "shell"
	config: {impure: false, command: ["sh", "-c", "printf hi > \"$MU_OUT/out\""], outputs: ["out"]}
}]
`, cacheDir)
	if err := os.WriteFile(filepath.Join(project, "mu.cue"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	if code := captureBuildCode(t, func() int { return runBuild([]string{"//out"}) }); code != exitOK {
		t.Fatalf("build exit = %d", code)
	}

	out := captureBuildStdout(t, func() int { return runCacheSize([]string{"--json"}) })
	var size struct {
		Blobs         int    `json:"blobs"`
		ActionResults int    `json:"action_results"`
		Path          string `json:"path"`
	}
	if err := json.Unmarshal(out, &size); err != nil {
		t.Fatalf("cache size output %q: %v", out, err)
	}
	if size.Path != cacheDir || size.Blobs == 0 || size.ActionResults == 0 {
		t.Fatalf("cache size = %+v, want blobs and results under %s", size, cacheDir)
	}
	if _, err := os.Stat(filepath.Join(home, ".mu", "cache")); !os.IsNotExist(err) {
		t.Fatalf("~/.mu/cache was created (stat err %v)", err)
	}
}
