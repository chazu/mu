package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/plugins/internal/plugintest"
	"github.com/chazu/mu/sdk/muplugin"
)

// TestParityWithBB replays the request script the retired plugin.bb
// answered (captured in testdata/parity_bb_responses.ndjson) and requires
// semantically identical responses. Intentional differences: discover
// lists capabilities (the bb discover omitted them) and reports a JSON
// Schema config_schema and a later version, and the capture
// action's env is PATH=/usr/bin:/bin instead of {} (see captureEnv). The
// other actions' bb "env": {} is dropped by Normalize; both forms give a
// pure action an empty environment.
func TestParityWithBB(t *testing.T) {
	p := newPlugin()
	plugintest.Parity(t, p,
		filepath.Join("testdata", "parity_requests.ndjson"),
		filepath.Join("testdata", "parity_bb_responses.ndjson"),
		func(i int, golden map[string]any) {
			if i == 0 {
				golden["capabilities"] = []any{"discover", "plan"}
				golden["config_schema"] = plugintest.Schema(t, p)
				golden["version"] = p.Version
			}
			actions, _ := golden["actions"].([]any)
			for _, a := range actions {
				if a := a.(map[string]any); a["id"] == "capture" {
					a["env"] = map[string]any{"PATH": "/usr/bin:/bin"}
				}
			}
		})
}

func TestConfigSchemaClosed(t *testing.T) {
	plugintest.ClosedSchema(t, newPlugin(), map[string]any{
		"path": "/tmp/x", "content": "hi", "mode": "0600", "owner": "root", "group": "wheel",
	})
}

func planFor(t *testing.T, target muplugin.TargetInfo) muplugin.PlanResponse {
	t.Helper()
	var resp muplugin.PlanResponse
	if err := muplugin.ExchangeInto(context.Background(), newPlugin(), muplugin.Request{Method: "plan", Target: &target}, &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

// run executes every planned action in order in dir, the way mu would for
// pure actions (no host environment beyond a system PATH).
func run(t *testing.T, dir string, resp muplugin.PlanResponse, extraEnv ...string) {
	t.Helper()
	if resp.Error != "" {
		t.Fatalf("plan error: %s", resp.Error)
	}
	for _, a := range resp.Actions {
		cmd := exec.Command(a.Command[0], a.Command[1:]...)
		cmd.Dir = dir
		cmd.Env = append([]string{"PATH=/usr/bin:/bin"}, extraEnv...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("action %s: %v\n%s", a.ID, err, out)
		}
	}
}

func TestWriteContentAndMode(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "it's", "motd")
	run(t, dir, planFor(t, muplugin.TargetInfo{Name: "//f", Config: map[string]any{
		"path": dest, "content": "hello\n'world'", "mode": "0600",
	}}))
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "hello\n'world'\n" {
		t.Fatalf("content = %q, %v", got, err)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

func TestEmptyContentWritesFile(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "empty")
	run(t, dir, planFor(t, muplugin.TargetInfo{Name: "//f", Config: map[string]any{"path": dest, "content": ""}}))
	if _, err := os.Stat(dest); err != nil {
		t.Fatal(err)
	}
}

func TestCopySourceSymlinkAbsent(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "src.conf"), []byte("a=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "out", "dst.conf")
	run(t, dir, planFor(t, muplugin.TargetInfo{Name: "//f", Sources: []string{"src.conf"}, Config: map[string]any{"path": dest}}))
	if got, err := os.ReadFile(dest); err != nil || string(got) != "a=1\n" {
		t.Fatalf("copied = %q, %v", got, err)
	}

	link := filepath.Join(dir, "links", "l")
	run(t, dir, planFor(t, muplugin.TargetInfo{Name: "//f", Config: map[string]any{"path": link, "symlink": dest}}))
	if target, err := os.Readlink(link); err != nil || target != dest {
		t.Fatalf("symlink -> %q, %v", target, err)
	}

	run(t, dir, planFor(t, muplugin.TargetInfo{Name: "//f", Config: map[string]any{"path": dest, "absent": true}}))
	if _, err := os.Stat(dest); !os.IsNotExist(err) {
		t.Fatalf("absent: stat err = %v", err)
	}
}

func TestCaptureCopiesIntoSealedOutDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "it's.key")
	if err := os.WriteFile(src, []byte("KEY"), 0o644); err != nil {
		t.Fatal(err)
	}
	resp := planFor(t, muplugin.TargetInfo{
		Name:          "//f",
		Config:        map[string]any{"sealed_output_files": map[string]any{"KEY": src}},
		SealedOutputs: map[string]string{"KEY": "pass:bootstrap/key"},
	})
	if len(resp.Actions) != 1 || resp.Actions[0].Env["PATH"] != "/usr/bin:/bin" {
		t.Fatalf("capture must declare a hermetic env: %+v", resp.Actions)
	}
	out := t.TempDir()
	run(t, dir, resp, "MU_SEALED_OUT_DIR="+out)
	got, err := os.ReadFile(filepath.Join(out, "KEY"))
	if err != nil || string(got) != "KEY" {
		t.Fatalf("captured = %q, %v", got, err)
	}
	if fi, _ := os.Stat(filepath.Join(out, "KEY")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v", fi.Mode().Perm())
	}
}

// The bb plugin threw on a missing path, which killed the plugin process;
// the Go plugin answers with an error.
func TestMissingPathIsAnError(t *testing.T) {
	resp := planFor(t, muplugin.TargetInfo{Name: "//f", Config: map[string]any{"content": "x"}})
	if !strings.Contains(resp.Error, `requires "path"`) {
		t.Fatalf("error = %q", resp.Error)
	}
}
