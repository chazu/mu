package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeInfoProject writes a project whose only plugin is a shell command
// answering discover, and returns the path of its mu.cue.
func writeInfoProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "plugin.sh")
	mustWrite(t, script, `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"discover"'*) echo '{"name":"mine","version":"1.2.3","protocol_version":1,"capabilities":["discover"]}' ;;
  esac
done
`)
	muCue := filepath.Join(dir, "mu.cue")
	mustWrite(t, muCue, `plugins: [{name: "mine", command: ["sh", "`+script+`"]}]
`)
	return muCue
}

func TestPluginInfoHonoursConfigFromUnrelatedCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	muCue := writeInfoProject(t)
	t.Chdir(t.TempDir()) // no mu.cue here or above

	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runPluginInfo([]string{"--config", muCue, "--json", "mine"})
	})
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var out pluginInfoOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if out.Source != "project" || out.Name != "mine" || out.Version != "1.2.3" {
		t.Fatalf("info = %+v, want project plugin mine@1.2.3", out)
	}
}

func TestPluginInfoBrokenConfigIsAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "mu.cue"), "plugins: [{name: \n")

	var code int
	_, stderr := captureOutput(t, func() {
		code = runPluginInfo([]string{"--config", filepath.Join(dir, "mu.cue"), "mine"})
	})
	if code != exitFail {
		t.Fatalf("--config broken mu.cue: exit %d, want %d (stderr %q)", code, exitFail, stderr)
	}
	if strings.Contains(stderr, "cached") {
		t.Fatalf("fell back to the plugin cache: %q", stderr)
	}

	// The same applies to a project discovered from the working directory.
	t.Chdir(dir)
	_, stderr = captureOutput(t, func() { code = runPluginInfo([]string{"mine"}) })
	if code != exitFail {
		t.Fatalf("discovered broken mu.cue: exit %d, want %d (stderr %q)", code, exitFail, stderr)
	}
}

func TestPluginInfoOutsideProjectFallsBackToInstalled(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Chdir(t.TempDir())
	hash := strings.Repeat("c", 64)
	bundle := filepath.Join(home, ".mu", "plugins", "mine", "bundle-"+hash)
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(bundle, "plugin"), `#!/bin/sh
while IFS= read -r line; do
  case "$line" in
    *'"method":"discover"'*) echo '{"name":"mine","version":"0.0.1","protocol_version":1,"capabilities":["discover"]}' ;;
  esac
done
`)
	if err := os.Chmod(filepath.Join(bundle, "plugin"), 0o755); err != nil {
		t.Fatal(err)
	}

	var code int
	stdout, stderr := captureOutput(t, func() { code = runPluginInfo([]string{"--json", "mine"}) })
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	var out pluginInfoOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode %q: %v", stdout, err)
	}
	if out.Source != "cache" || out.Digest != "sha256:"+hash {
		t.Fatalf("info = %+v, want cached sha256:%s", out, hash)
	}
}
