package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func parseFS(t *testing.T, c *cliContext, fs *flag.FlagSet, args []string) {
	t.Helper()
	if err := fs.Parse(args); err != nil {
		t.Fatalf("parse: %v", err)
	}
	// Wire stderr capture after Parse (newCLIContext defaults to os.Stderr).
	_ = c
}

func TestRegistersOnlyRequestedSharedFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ctx := newCLIContext("test", fs, useConfig, useJSON)
	for _, name := range []string{"config", "json"} {
		if fs.Lookup(name) == nil {
			t.Errorf("flag --%s not registered", name)
		}
	}
	for _, name := range []string{"verbose", "offline", "no-color"} {
		if fs.Lookup(name) != nil {
			t.Errorf("flag --%s registered but not requested", name)
		}
	}
	// Unregistered shared flags read as their zero value.
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	ctx.Resolve(resolveOpts{})
	if ctx.Verbose || ctx.Offline || ctx.configPath() != "" {
		t.Errorf("unregistered flags not zero: verbose=%v offline=%v config=%q", ctx.Verbose, ctx.Offline, ctx.configPath())
	}
	if ctx.requireRegistry() != true {
		t.Error("requireRegistry without --offline registered should allow registry access")
	}
}

func TestResolveProjectRoot_FromConfigFlag(t *testing.T) {
	dir := t.TempDir()
	muCue := filepath.Join(dir, "mu.cue")
	if err := os.WriteFile(muCue, []byte(`toolchains: [{toolchain: "go", from: "scratch"}]
targets: [{target: "//x", toolchain: "go", sources: ["main.go"]}]
`), 0o644); err != nil {
		t.Fatal(err)
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ctx := newCLIContext("test", fs, useConfig)
	if err := fs.Parse([]string{"--config", muCue}); err != nil {
		t.Fatal(err)
	}
	if code, ok := ctx.Resolve(resolveOpts{NeedConfig: true}); !ok {
		t.Fatalf("resolve failed with exit %d", code)
	}
	if ctx.ProjectRoot != dir {
		t.Errorf("ProjectRoot = %q, want %q", ctx.ProjectRoot, dir)
	}
	if ctx.Config == nil {
		t.Fatal("expected Config populated")
	}
}

func TestResolveProjectRoot_DiscoveryFailureReturnsUsage(t *testing.T) {
	// Run from a temp dir with no mu.cue anywhere upward.
	t.Chdir(t.TempDir())

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ctx := newCLIContext("test", fs)
	var buf bytes.Buffer
	ctx.stderr = &buf
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}
	code, ok := ctx.Resolve(resolveOpts{NeedProjectRoot: true})
	if ok {
		t.Fatal("expected resolve failure")
	}
	if code != exitUsage {
		t.Errorf("exit code = %d, want %d", code, exitUsage)
	}
	if !strings.HasPrefix(buf.String(), "mu test: ") {
		t.Errorf("error prefix missing; got %q", buf.String())
	}
}

func TestFailFormatsConsistently(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ctx := newCLIContext("build", fs)
	var buf bytes.Buffer
	ctx.stderr = &buf
	code := ctx.fail(exitFail, "reading %s: %v", "foo", "bar")
	if code != exitFail {
		t.Errorf("fail returned %d, want %d", code, exitFail)
	}
	got := buf.String()
	want := "mu build: reading foo: bar\n"
	if got != want {
		t.Errorf("stderr = %q, want %q", got, want)
	}
}

func TestFlagValuesExposedAfterResolve(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	ctx := newCLIContext("test", fs, useJSON, useVerbose, useOffline)
	if err := fs.Parse([]string{"--json", "--verbose", "--offline"}); err != nil {
		t.Fatal(err)
	}
	ctx.Resolve(resolveOpts{}) // no resources; just surfaces flag values
	if !ctx.JSON || !ctx.Verbose || !ctx.Offline {
		t.Errorf("flags not surfaced: json=%v verbose=%v offline=%v", ctx.JSON, ctx.Verbose, ctx.Offline)
	}
}

// TestCommandFlagSets pins the exact flag set of every leaf command, so a
// shared flag a command does not read (--json on scratch, --verbose on
// plugin info, the never-read --no-color, ...) cannot creep back in.
func TestCommandFlagSets(t *testing.T) {
	tests := []struct {
		args []string
		want string // sorted, space-separated flag names
	}{
		{[]string{"build"}, "attach config emit-manifest jobs json no-cache offline plan publish verbose"},
		{[]string{"scratch"}, "config no-cache offline"},
		{[]string{"observe"}, "config json ndjson offline verbose"},
		{[]string{"targets"}, "config deps dot json offline rdeps"},
		{[]string{"verify"}, "fix json"},
		{[]string{"plugin", "list"}, "config json offline"},
		{[]string{"plugin", "info"}, "config digest json offline"},
		{[]string{"plugin", "install"}, "catalog config offline"},
		{[]string{"cache", "ls"}, "config json toolchains"},
		{[]string{"cache", "inspect"}, "config json"},
		{[]string{"cache", "size"}, "config json"},
		{[]string{"cache", "clean"}, "config dry-run json"},
		{[]string{"cache", "push"}, "config dry-run offline verbose"},
		{[]string{"cache", "login"}, "config offline password password-stdin username"},
		{[]string{"cache", "logout"}, "config offline"},
		{[]string{"guide", "plugin"}, "digest offline"},
	}
	for _, tt := range tests {
		got := captureParse(t, tt.args[0], tt.args[1:])
		names := make([]string, 0, len(got.Flags))
		for name := range got.Flags {
			names = append(names, name)
		}
		sort.Strings(names)
		if strings.Join(names, " ") != tt.want {
			t.Errorf("mu %s flags = %q, want %q", strings.Join(tt.args, " "), strings.Join(names, " "), tt.want)
		}
	}
}

// TestCacheSubcommandsHonourConfig checks that the cache inspection
// subcommands accept --config and read that mu.cue's cache.path.
func TestCacheSubcommandsHonourConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	dir := t.TempDir()
	cacheDir := filepath.Join(dir, "custom-cache")
	muCue := filepath.Join(dir, "mu.cue")
	if err := os.WriteFile(muCue, []byte(`cache: {path: "`+cacheDir+`"}
`), 0o644); err != nil {
		t.Fatal(err)
	}

	for _, sub := range []string{"ls", "size", "clean"} {
		var code int
		_, stderr := captureOutput(t, func() { code = run("cache", []string{sub, "--config", muCue}) })
		if code != exitOK {
			t.Errorf("mu cache %s --config: exit %d (stderr %q)", sub, code, stderr)
		}
	}
	if _, err := os.Stat(cacheDir); err != nil {
		t.Errorf("cache.path %s not used: %v", cacheDir, err)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("HOME"), ".mu", "cache")); !os.IsNotExist(err) {
		t.Errorf("default ~/.mu/cache touched despite cache.path (stat err %v)", err)
	}

	var code int
	_, stderr := captureOutput(t, func() {
		code = run("cache", []string{"inspect", "--config", muCue, "sha256:" + strings.Repeat("0", 64)})
	})
	if code == exitUsage || strings.Contains(stderr, "flag provided but not defined") {
		t.Errorf("mu cache inspect --config: exit %d (stderr %q)", code, stderr)
	}
}
