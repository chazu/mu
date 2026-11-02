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

// MU_HOME relocates the whole state directory: a build, `mu cache ls`,
// `mu plugin list`, `mu login` and the staging sweep write under MU_HOME and
// nothing under ~/.mu.
func TestMuHomeRelocatesAllState(t *testing.T) {
	host, _ := loginFixture(t) // HOME is a fresh temp dir; cwd is outside any project
	home, _ := os.UserHomeDir()
	muHome := filepath.Join(t.TempDir(), "mu-home")
	t.Setenv("MU_HOME", muHome)
	t.Setenv("MU_NO_STAGING_SWEEP", "")

	project := t.TempDir()
	config := `package mu
targets: [{
	target:    "//out"
	toolchain: "shell"
	config: {impure: false, command: ["sh", "-c", "printf hi > \"$MU_OUT/out\""], outputs: ["out"]}
}]
`
	if err := os.WriteFile(filepath.Join(project, "mu.cue"), []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	startStagingSweep()
	if code := captureBuildCode(t, func() int { return runBuild([]string{"//out"}) }); code != exitOK {
		t.Fatalf("build exit = %d", code)
	}
	if code := captureBuildCode(t, func() int { return runCacheLs(nil) }); code != exitOK {
		t.Fatalf("cache ls exit = %d", code)
	}
	if code := captureBuildCode(t, func() int { return runPluginList(nil) }); code != exitOK {
		t.Fatalf("plugin list exit = %d", code)
	}
	code := withStdin(t, "s3cret\n", func() int {
		return runLogin([]string{"--username", "alice", "--password-stdin", host})
	})
	if code != exitOK {
		t.Fatalf("login exit = %d", code)
	}

	if _, err := os.Stat(filepath.Join(home, ".mu")); !os.IsNotExist(err) {
		t.Fatalf("~/.mu was created (stat err %v)", err)
	}
	for _, p := range []string{
		filepath.Join(muHome, "cache", ".lock"),
		filepath.Join(muHome, "credentials.json"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("expected %s: %v", p, err)
		}
	}
}
