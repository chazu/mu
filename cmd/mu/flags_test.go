package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// parsedSnapshot records a parsed FlagSet: every flag's value plus the
// positionals.
type parsedSnapshot struct {
	Name  string
	Flags map[string]string
	Args  []string
}

func snapshot(fs *flag.FlagSet) parsedSnapshot {
	s := parsedSnapshot{Name: fs.Name(), Flags: map[string]string{}, Args: append([]string{}, fs.Args()...)}
	fs.VisitAll(func(f *flag.Flag) { s.Flags[f.Name] = f.Value.String() })
	return s
}

// captureParse runs a real mu command with parseFlagsHook set to record the
// parsed FlagSet and stop before the command does any work.
func captureParse(t *testing.T, cmd string, args []string) parsedSnapshot {
	t.Helper()
	var got *parsedSnapshot
	parseFlagsHook = func(fs *flag.FlagSet) bool {
		s := snapshot(fs)
		got = &s
		return true
	}
	defer func() { parseFlagsHook = nil }()
	if code := run(cmd, args); code != exitOK {
		t.Fatalf("mu %s %v: exit %d, want %d", cmd, args, code, exitOK)
	}
	if got == nil {
		t.Fatalf("mu %s %v: flags were never parsed", cmd, args)
	}
	return *got
}

// TestFlagsAnywhere checks that flags before, between and after positionals
// give identical parsed results for the real command flag sets.
func TestFlagsAnywhere(t *testing.T) {
	tests := []struct {
		cmd      string
		variants [][]string
		wantArgs []string
		wantFlag map[string]string
	}{
		{
			cmd: "build",
			variants: [][]string{
				{"--plan", "--jobs", "3", "//a", "//b"},
				{"//a", "//b", "--plan", "--jobs", "3"},
				{"//a", "--plan", "//b", "--jobs=3"},
				{"-plan", "//a", "-jobs", "3", "//b"},
			},
			wantArgs: []string{"//a", "//b"},
			wantFlag: map[string]string{"plan": "true", "jobs": "3"},
		},
		{
			cmd: "observe",
			variants: [][]string{
				{"--ndjson", "--config", "x/mu.cue", "//a"},
				{"//a", "--ndjson", "--config", "x/mu.cue"},
				{"--config=x/mu.cue", "//a", "--ndjson"},
			},
			wantArgs: []string{"//a"},
			wantFlag: map[string]string{"ndjson": "true", "config": "x/mu.cue"},
		},
		{
			cmd: "targets",
			variants: [][]string{
				{"--json", "--dot", "//cmd/..."},
				{"//cmd/...", "--json", "--dot"},
				{"--dot", "//cmd/...", "--json"},
			},
			wantArgs: []string{"//cmd/..."},
			wantFlag: map[string]string{"json": "true", "dot": "true"},
		},
		{
			cmd: "plugin",
			variants: [][]string{
				{"info", "--json", "--digest", "sha256:ab", "go"},
				{"info", "go", "--json", "--digest", "sha256:ab"},
				{"info", "--digest=sha256:ab", "go", "--json"},
			},
			wantArgs: []string{"go"},
			wantFlag: map[string]string{"json": "true", "digest": "sha256:ab"},
		},
		{
			cmd: "guide",
			variants: [][]string{
				{"plugin", "--offline", "go"},
				{"plugin", "go", "--offline"},
			},
			wantArgs: []string{"go"},
			wantFlag: map[string]string{"offline": "true"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.cmd, func(t *testing.T) {
			var first parsedSnapshot
			for i, v := range tt.variants {
				got := captureParse(t, tt.cmd, v)
				if !reflect.DeepEqual(got.Args, tt.wantArgs) {
					t.Errorf("%v: positionals = %q, want %q", v, got.Args, tt.wantArgs)
				}
				for name, want := range tt.wantFlag {
					if got.Flags[name] != want {
						t.Errorf("%v: --%s = %q, want %q", v, name, got.Flags[name], want)
					}
				}
				if i == 0 {
					first = got
				} else if !reflect.DeepEqual(got, first) {
					t.Errorf("%v parsed differently from %v:\n got  %+v\n want %+v", v, tt.variants[0], got, first)
				}
			}
		})
	}
}

func newTestFlagSet() (*flag.FlagSet, *bool, *string) {
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	b := fs.Bool("plan", false, "")
	s := fs.String("config", "", "")
	return fs, b, s
}

func TestParseInterspersed(t *testing.T) {
	tests := []struct {
		args       []string
		wantPos    []string
		wantPlan   bool
		wantConfig string
		wantErr    error // nil, flag.ErrHelp, or errAny
	}{
		{args: nil, wantPos: nil},
		{args: []string{"a", "--plan", "b"}, wantPos: []string{"a", "b"}, wantPlan: true},
		{args: []string{"a", "--config", "c", "b"}, wantPos: []string{"a", "b"}, wantConfig: "c"},
		{args: []string{"a", "--config=--plan"}, wantPos: []string{"a"}, wantConfig: "--plan"},
		{args: []string{"a", "--", "--plan", "-x"}, wantPos: []string{"a", "--plan", "-x"}},
		{args: []string{"--plan=false", "-", "a"}, wantPos: []string{"-", "a"}},
		{args: []string{"a", "--config", "--", "b"}, wantPos: []string{"a", "b"}, wantConfig: "--"},
		{args: []string{"a", "-h"}, wantErr: flag.ErrHelp},
		{args: []string{"--help", "a"}, wantErr: flag.ErrHelp},
		{args: []string{"a", "--", "--help"}, wantPos: []string{"a", "--help"}},
		{args: []string{"a", "--nope"}, wantErr: errAny},
		{args: []string{"a", "--config"}, wantErr: errAny},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprint(tt.args), func(t *testing.T) {
			fs, plan, config := newTestFlagSet()
			pos, err := parseInterspersed(fs, tt.args)
			switch {
			case tt.wantErr == errAny:
				if err == nil || errors.Is(err, flag.ErrHelp) {
					t.Fatalf("err = %v, want a parse error", err)
				}
				return
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("err = %v, want %v", err, tt.wantErr)
				}
				return
			case err != nil:
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(pos, tt.wantPos) {
				t.Errorf("positionals = %q, want %q", pos, tt.wantPos)
			}
			if len(tt.wantPos) > 0 && !reflect.DeepEqual(fs.Args(), tt.wantPos) {
				t.Errorf("fs.Args() = %q, want %q", fs.Args(), tt.wantPos)
			}
			if *plan != tt.wantPlan || *config != tt.wantConfig {
				t.Errorf("plan=%v config=%q, want plan=%v config=%q", *plan, *config, tt.wantPlan, tt.wantConfig)
			}
		})
	}
}

var errAny = errors.New("any parse error")

// TestHelpExitsZero checks that every help spelling, at the top level, on
// sub-dispatchers and on leaf commands, exits 0.
func TestHelpExitsZero(t *testing.T) {
	cases := []struct {
		args       []string
		wantStdout string // substring
	}{
		{[]string{"help"}, "usage: mu <command>"},
		{[]string{"-h"}, "usage: mu <command>"},
		{[]string{"--help"}, "usage: mu <command>"},
		{[]string{"help", "build"}, "usage: mu build [flags] <target>..."},
		{[]string{"help", "cache", "ls"}, "usage: mu cache ls [flags]"},
		{[]string{"help", "plugin", "info"}, "usage: mu plugin info [flags] <name>"},
		{[]string{"build", "-h"}, "usage: mu build [flags] <target>..."},
		{[]string{"build", "//x", "--help"}, "-plan"},
		{[]string{"cache", "--help"}, "usage: mu cache <command>"},
		{[]string{"cache", "help"}, "usage: mu cache <command>"},
		{[]string{"plugin", "--help"}, "usage: mu plugin <command>"},
		{[]string{"targets", "-h"}, "usage: mu targets [flags] [pattern...]"},
		{[]string{"guide", "plugin", "-h"}, "usage: mu guide plugin [flags] <name>"},
		{[]string{"plugin", "info", "--help"}, "usage: mu plugin info [flags] <name>"},
	}
	for _, c := range cases {
		var code int
		stdout, stderr := captureOutput(t, func() { code = run(c.args[0], c.args[1:]) })
		if code != exitOK {
			t.Errorf("mu %v: exit %d, want %d (stderr %q)", c.args, code, exitOK, stderr)
		}
		if !strings.Contains(stdout, c.wantStdout) {
			t.Errorf("mu %v: stdout %q does not contain %q", c.args, stdout, c.wantStdout)
		}
	}

	var code int
	captureOutput(t, func() { code = run("guide", []string{"--help"}) })
	if code != exitOK {
		t.Errorf("mu guide --help: exit %d, want %d", code, exitOK)
	}
	captureOutput(t, func() { code = run("help", []string{"no-such-command"}) })
	if code != exitUsage {
		t.Errorf("mu help no-such-command: exit %d, want %d", code, exitUsage)
	}
}

// captureOutput runs fn with os.Stdout and os.Stderr redirected to temp
// files and returns what was written to each.
func captureOutput(t *testing.T, fn func()) (string, string) {
	t.Helper()
	dir := t.TempDir()
	outF, err := os.Create(filepath.Join(dir, "stdout"))
	if err != nil {
		t.Fatal(err)
	}
	errF, err := os.Create(filepath.Join(dir, "stderr"))
	if err != nil {
		t.Fatal(err)
	}
	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outF, errF
	defer func() {
		os.Stdout, os.Stderr = origOut, origErr
		outF.Close()
		errF.Close()
	}()
	fn()
	out, _ := os.ReadFile(outF.Name())
	errOut, _ := os.ReadFile(errF.Name())
	return string(out), string(errOut)
}
