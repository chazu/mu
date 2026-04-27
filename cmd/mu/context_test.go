package main

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
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

func TestRegistersSharedFlags(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	_ = newCLIContext("test", fs)
	for _, name := range []string{"config", "json", "verbose", "no-color"} {
		if fs.Lookup(name) == nil {
			t.Errorf("flag --%s not registered", name)
		}
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
	ctx := newCLIContext("test", fs)
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
	ctx := newCLIContext("test", fs)
	if err := fs.Parse([]string{"--json", "--verbose", "--no-color"}); err != nil {
		t.Fatal(err)
	}
	ctx.Resolve(resolveOpts{}) // no resources; just surfaces flag values
	if !ctx.JSON || !ctx.Verbose || !ctx.NoColor {
		t.Errorf("flags not surfaced: json=%v verbose=%v nocolor=%v", ctx.JSON, ctx.Verbose, ctx.NoColor)
	}
}
