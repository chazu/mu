package main

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
)

func TestPluginHelpListsExactlyListInfoInstall(t *testing.T) {
	var code int
	stdout, _ := captureOutput(t, func() { code = runPlugin([]string{"--help"}) })
	if code != exitOK {
		t.Fatalf("exit %d", code)
	}
	_, commands, ok := strings.Cut(stdout, "Commands:\n")
	if !ok {
		t.Fatalf("no Commands section in %q", stdout)
	}
	var got []string
	for _, line := range strings.Split(strings.TrimSpace(commands), "\n") {
		got = append(got, strings.Fields(line)[0])
	}
	if want := []string{"list", "info", "install"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %v, want %v", got, want)
	}
	for _, removed := range []string{"add", "search", "update", "lock", "status", "push", "test"} {
		if code := runPlugin([]string{removed}); code != exitUsage {
			t.Errorf("mu plugin %s: exit %d, want %d", removed, code, exitUsage)
		}
	}
}

func TestCollectPluginListMergesProjectAndInstalled(t *testing.T) {
	pluginsDir := t.TempDir()
	hashA := strings.Repeat("a", 64)
	hashB := strings.Repeat("b", 64)
	mkdir := func(parts ...string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(append([]string{pluginsDir}, parts...)...), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	mkdir("pinned", "bundle-"+hashA)
	mkdir("drifted", "bundle-"+hashB)
	mkdir("orphan", "bundle-"+hashA)
	mkdir(".bundle-staging")
	if err := os.WriteFile(filepath.Join(pluginsDir, "orphan", "plugin-"+hashB+".bb"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg := &config.ProjectConfig{Plugins: []config.PluginDef{
		{Name: "pinned", Digest: "sha256:" + hashA},
		{Name: "drifted", Digest: "sha256:" + hashA},
		{Name: "absent", Digest: "sha256:" + hashA},
		{Name: "local", Script: "plugins/local/plugin.bb"},
		{Name: "cmd", Command: []string{"sh", "-c", "true"}},
	}}
	items, err := collectPluginList(cfg, pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	type row struct{ name, typ, state, installed string }
	var got []row
	for _, it := range items {
		got = append(got, row{it.Name, it.Type, it.State, strings.Join(it.Installed, ",")})
	}
	want := []row{
		{"pinned", "digest", "ok", "sha256:" + hashA},
		{"drifted", "digest", "stale", "sha256:" + hashB},
		{"absent", "digest", "missing", ""},
		{"local", "script", "local", ""},
		{"cmd", "command", "local", ""},
		{"orphan", "", "", "sha256:" + hashA + ",sha256:" + hashB},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rows:\n got  %v\n want %v", got, want)
	}
	if items[4].Ref != "sh -c true" || items[5].Declared || !items[0].Declared {
		t.Fatalf("unexpected row detail: %+v", items)
	}

	// Outside a project only installed plugins are listed.
	items, err = collectPluginList(nil, pluginsDir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, it := range items {
		names = append(names, it.Name)
	}
	if want := []string{"drifted", "orphan", "pinned"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("names = %v, want %v", names, want)
	}
}

func TestPluginListHonoursConfigAndReportsLoadErrors(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	project := t.TempDir()
	mustWrite(t, filepath.Join(project, "mu.cue"), `plugins: [{name: "mine", command: ["true"]}]
`)
	var code int
	stdout, stderr := captureOutput(t, func() {
		code = runPluginList([]string{"--config", filepath.Join(project, "mu.cue"), "--json"})
	})
	if code != exitOK {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	if !regexp.MustCompile(`"name": "mine"`).MatchString(stdout) {
		t.Fatalf("project plugin missing from %s", stdout)
	}

	broken := t.TempDir()
	mustWrite(t, filepath.Join(broken, "mu.cue"), "plugins: [{name: \n")
	_, stderr = captureOutput(t, func() {
		code = runPluginList([]string{"--config", filepath.Join(broken, "mu.cue")})
	})
	if code != exitFail {
		t.Fatalf("broken mu.cue: exit %d, want %d (stderr %q)", code, exitFail, stderr)
	}
}
