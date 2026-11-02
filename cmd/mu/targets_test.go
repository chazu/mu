package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// targetsFixture writes a project where //app -> //lib/a -> //lib/b, plus an
// unrelated //tool, and returns the config path.
func targetsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	config := `package mu
targets: [
 {target: "//app", toolchain: "shell", deps: ["//lib/a"], config: {command: ["true"]}},
 {target: "//lib/a", toolchain: "shell", deps: ["//lib/b"], config: {command: ["true"]}},
 {target: "//lib/b", toolchain: "shell", config: {command: ["true"]}},
 {target: "//tool", toolchain: "shell", config: {command: ["true"]}},
]
`
	path := filepath.Join(root, "mu.cue")
	if err := os.WriteFile(path, []byte(config), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runTargetsCapture(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var code int
	stdout, stderr := captureOutput(t, func() { code = run("targets", args) })
	return code, stdout, stderr
}

func TestTargetsListsAllAndFiltersByPattern(t *testing.T) {
	config := targetsFixture(t)
	code, out, stderr := runTargetsCapture(t, "--config", config)
	if code != exitOK {
		t.Fatalf("mu targets: exit %d, stderr %q", code, stderr)
	}
	for _, name := range []string{"//app", "//lib/a", "//lib/b", "//tool"} {
		if !strings.Contains(out, name) {
			t.Errorf("list output missing %s:\n%s", name, out)
		}
	}

	code, out, stderr = runTargetsCapture(t, "--config", config, "--json", "//lib/...")
	if code != exitOK {
		t.Fatalf("mu targets --json //lib/...: exit %d, stderr %q", code, stderr)
	}
	var listed []struct {
		Name string `json:"target"`
	}
	if err := json.Unmarshal([]byte(out), &listed); err != nil {
		t.Fatalf("decode JSON list: %v\n%s", err, out)
	}
	if len(listed) != 2 || listed[0].Name != "//lib/a" || listed[1].Name != "//lib/b" {
		t.Fatalf("filtered list = %+v, want //lib/a, //lib/b", listed)
	}

	if code, _, _ := runTargetsCapture(t, "--config", config, "//nope"); code != exitFail {
		t.Errorf("unknown target pattern: exit %d, want %d", code, exitFail)
	}
}

func TestTargetsDepsShowsDependencyChain(t *testing.T) {
	config := targetsFixture(t)
	code, out, stderr := runTargetsCapture(t, "--config", config, "--deps", "//app")
	if code != exitOK {
		t.Fatalf("mu targets --deps: exit %d, stderr %q", code, stderr)
	}
	want := "//app\n└── //lib/a  (shell)\n    └── //lib/b  (shell)\n"
	if out != want {
		t.Fatalf("--deps tree =\n%s\nwant\n%s", out, want)
	}

	code, out, stderr = runTargetsCapture(t, "--config", config, "--deps", "//app", "--json")
	if code != exitOK {
		t.Fatalf("mu targets --deps --json: exit %d, stderr %q", code, stderr)
	}
	var doc struct {
		Root    string `json:"root"`
		Reverse bool   `json:"reverse"`
		Nodes   []struct {
			Name     string   `json:"name"`
			Children []string `json:"children"`
		} `json:"nodes"`
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("decode --deps JSON: %v\n%s", err, out)
	}
	if doc.Root != "//app" || doc.Reverse || len(doc.Nodes) != 3 {
		t.Fatalf("--deps JSON = %+v", doc)
	}
}

func TestTargetsRdepsShowsReverseChain(t *testing.T) {
	config := targetsFixture(t)
	code, out, stderr := runTargetsCapture(t, "--config", config, "--rdeps", "//lib/b")
	if code != exitOK {
		t.Fatalf("mu targets --rdeps: exit %d, stderr %q", code, stderr)
	}
	want := "//lib/b\n└── //lib/a  (shell)\n    └── //app  (shell)\n"
	if out != want {
		t.Fatalf("--rdeps tree =\n%s\nwant\n%s", out, want)
	}
	if code, _, _ := runTargetsCapture(t, "--config", config, "--rdeps", "//missing"); code != exitFail {
		t.Errorf("unknown --rdeps target: exit %d, want %d", code, exitFail)
	}
}

func TestTargetsDot(t *testing.T) {
	config := targetsFixture(t)
	code, out, stderr := runTargetsCapture(t, "--config", config, "--dot", "--deps", "//app")
	if code != exitOK {
		t.Fatalf("mu targets --dot --deps: exit %d, stderr %q", code, stderr)
	}
	for _, edge := range []string{`"//app" -> "//lib/a";`, `"//lib/a" -> "//lib/b";`} {
		if !strings.Contains(out, edge) {
			t.Errorf("--dot --deps missing edge %s:\n%s", edge, out)
		}
	}
	if strings.Contains(out, "//tool") {
		t.Errorf("--dot --deps includes an unrelated target:\n%s", out)
	}

	code, out, stderr = runTargetsCapture(t, "--config", config, "--dot")
	if code != exitOK {
		t.Fatalf("mu targets --dot: exit %d, stderr %q", code, stderr)
	}
	if !strings.HasPrefix(out, "digraph mu {") || !strings.Contains(out, `"//tool";`) || !strings.Contains(out, `"//app" -> "//lib/a";`) {
		t.Errorf("--dot full graph =\n%s", out)
	}
}

func TestTargetsFlagConflicts(t *testing.T) {
	config := targetsFixture(t)
	cases := [][]string{
		{"--json", "--dot"},
		{"--json", "--dot", "--deps", "//app"},
		{"--deps", "//app", "--rdeps", "//lib/b"},
		{"--deps", "//app", "//tool"},
	}
	for _, args := range cases {
		code, _, stderr := runTargetsCapture(t, append([]string{"--config", config}, args...)...)
		if code != exitUsage {
			t.Errorf("mu targets %v: exit %d, want %d (stderr %q)", args, code, exitUsage, stderr)
		}
	}
	_, _, stderr := runTargetsCapture(t, "--config", config, "--json", "--dot")
	if !strings.Contains(stderr, "--json and --dot are mutually exclusive") {
		t.Errorf("exclusivity error = %q", stderr)
	}
}

func TestRemovedTargetAndGraphCommandsPointAtTargets(t *testing.T) {
	for _, cmd := range []string{"target", "graph"} {
		var code int
		_, stderr := captureOutput(t, func() { code = run(cmd, []string{"list"}) })
		if code != exitUsage || !strings.Contains(stderr, "mu targets") {
			t.Errorf("mu %s: exit %d, stderr %q", cmd, code, stderr)
		}
	}
}
