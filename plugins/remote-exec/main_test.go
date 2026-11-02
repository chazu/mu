package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/plugins/internal/plugintest"
	"github.com/chazu/mu/sdk/muplugin"
)

// TestParityWithBB replays the request script the retired plugin.bb
// answered (captured in testdata/parity_bb_responses.ndjson) and requires
// semantically identical responses. The one intentional difference: the
// Go plugin declares a config_schema, which the bb discover lacked.
func TestParityWithBB(t *testing.T) {
	schema := newPlugin().ConfigSchema
	plugintest.Parity(t, newPlugin(),
		filepath.Join("testdata", "parity_requests.ndjson"),
		filepath.Join("testdata", "parity_bb_responses.ndjson"),
		func(i int, golden map[string]any) {
			if i == 0 {
				golden["config_schema"] = plugintest.Normalize(roundTrip(t, schema))
			}
		})
}

// roundTrip converts a Go value into its decoded-JSON form.
func roundTrip(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func planReq(cfg map[string]any) muplugin.Request {
	return muplugin.Request{Method: "plan", Target: &muplugin.TargetInfo{Name: "//t", Toolchain: "remote-exec", Config: cfg}}
}

func planOne(t *testing.T, target muplugin.TargetInfo) muplugin.ActionSpec {
	t.Helper()
	var resp muplugin.PlanResponse
	if err := muplugin.ExchangeInto(context.Background(), newPlugin(), muplugin.Request{Method: "plan", Target: &target}, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Error != "" {
		t.Fatalf("plan error: %s", resp.Error)
	}
	if len(resp.Actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(resp.Actions))
	}
	return resp.Actions[0]
}

// The bb plugin split a string command into one argv element per
// character; the Go plugin rejects it.
func TestStringCommandRejected(t *testing.T) {
	resp, err := muplugin.Exchange(context.Background(), newPlugin(), planReq(map[string]any{"host": "h", "command": "uptime"}))
	if err != nil {
		t.Fatal(err)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "requires config.command") {
		t.Fatalf("string command: got %v, want a config.command error", resp)
	}
}

// The plan carries no host environment values: only fixed PATH and the
// names of the variables ssh needs.
func TestPlanEnvIsHostIndependent(t *testing.T) {
	t.Setenv("SSH_AUTH_SOCK", "/tmp/agent.sock")
	a := planOne(t, muplugin.TargetInfo{Name: "//t", Config: map[string]any{"host": "h", "command": []any{"true"}}})
	if len(a.Env) != 1 || a.Env["PATH"] == "" {
		t.Fatalf("env = %v, want only the fixed PATH", a.Env)
	}
	if !slices.Equal(a.EnvPassthrough, []string{"HOME", "SSH_AUTH_SOCK"}) {
		t.Fatalf("env_passthrough = %v", a.EnvPassthrough)
	}
	if strings.Contains(strings.Join(a.Command, " "), "/tmp/agent.sock") {
		t.Fatal("host SSH_AUTH_SOCK value leaked into the command")
	}
}

// TestWrapperRunsRemoteScript executes the planned wrapper against a fake
// ssh that runs the remote command locally: env exports, work_dir, sealed
// input forwarding and the sealed_output_files fetch all take effect, and
// ssh is invoked with the configured destination and port.
func TestWrapperRunsRemoteScript(t *testing.T) {
	fake := plugintest.NewFakeSSH(t)
	remote := t.TempDir()
	outDir := t.TempDir()
	a := planOne(t, muplugin.TargetInfo{
		Name: "//t",
		Config: map[string]any{
			"host":     "h.example",
			"user":     "deploy",
			"port":     float64(2222),
			"env":      map[string]any{"GREETING": "hello 'world'"},
			"work_dir": remote,
			"command":  []any{"sh", "-c", `printf '%s|%s' "$GREETING" "$API_TOKEN" > out; printf tok > token`},
			"sealed_output_files": map[string]any{
				"TOKEN": filepath.Join(remote, "token"),
			},
		},
		SealedInputs:  map[string]string{"API_TOKEN": "env:X"},
		SealedOutputs: map[string]string{"TOKEN": "pass:bootstrap/token"},
	})
	out, err := fake.Run(t, t.TempDir(), a.Command, map[string]string{
		"API_TOKEN":         "s3cr3t",
		"MU_SEALED_OUT_DIR": outDir,
	})
	if err != nil {
		t.Fatalf("wrapper: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(remote, "out"))
	if err != nil {
		t.Fatalf("remote command did not run in work_dir: %v\n%s", err, out)
	}
	if string(got) != "hello 'world'|s3cr3t" {
		t.Fatalf("remote command saw %q", got)
	}
	tok, err := os.ReadFile(filepath.Join(outDir, "TOKEN"))
	if err != nil || string(tok) != "tok" {
		t.Fatalf("sealed output fetch: %q, %v", tok, err)
	}
	if fi, _ := os.Stat(filepath.Join(outDir, "TOKEN")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("sealed output mode = %v, want 0600", fi.Mode().Perm())
	}
	calls := fake.Calls(t)
	if len(calls) != 2 {
		t.Fatalf("got %d ssh calls, want 2 (command + fetch)", len(calls))
	}
	for _, c := range calls {
		if c.Dest != "deploy@h.example" || c.Port != "2222" {
			t.Fatalf("ssh dest/port = %q/%q", c.Dest, c.Port)
		}
		if !slices.Contains(c.Argv, "BatchMode=yes") {
			t.Fatalf("without SSH_PASS ssh must use BatchMode: %v", c.Argv)
		}
	}
	if fake.UsedSSHPass() {
		t.Fatal("sshpass used without SSH_PASS")
	}
}

func TestWrapperCheckSkipsCommand(t *testing.T) {
	fake := plugintest.NewFakeSSH(t)
	dir := t.TempDir()
	marker := filepath.Join(dir, "marker")
	if err := os.WriteFile(marker, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	a := planOne(t, muplugin.TargetInfo{Name: "//t", Config: map[string]any{
		"host":    "h",
		"check":   []any{"test", "-e", marker},
		"command": []any{"touch", filepath.Join(dir, "ran")},
	}})
	out, err := fake.Run(t, dir, a.Command, nil)
	if err != nil {
		t.Fatalf("wrapper: %v\n%s", err, out)
	}
	if !strings.Contains(out, "[check passed, skipping]") {
		t.Fatalf("output %q", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "ran")); err == nil {
		t.Fatal("command ran although check passed")
	}
}

func TestWrapperUsesSSHPass(t *testing.T) {
	fake := plugintest.NewFakeSSH(t)
	a := planOne(t, muplugin.TargetInfo{
		Name:         "//t",
		Config:       map[string]any{"host": "h", "command": []any{"true"}},
		SealedInputs: map[string]string{"SSH_PASS": "env:P"},
	})
	if out, err := fake.Run(t, t.TempDir(), a.Command, map[string]string{"SSH_PASS": "pw"}); err != nil {
		t.Fatalf("wrapper: %v\n%s", err, out)
	}
	calls := fake.Calls(t)
	if !fake.UsedSSHPass() || len(calls) != 1 || calls[0].SSHPass != "pw" {
		t.Fatalf("sshpass not used with SSHPASS=pw: %+v", calls)
	}
	if slices.Contains(calls[0].Argv, "BatchMode=yes") {
		t.Fatal("password auth must not force BatchMode")
	}
}

// TestHelperProcess runs the plugin over stdio when launched by
// TestCoordinatorPlan.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("MU_REMOTE_EXEC_HELPER") != "1" {
		return
	}
	newPlugin().Main()
	os.Exit(0)
}

// coordinatorPlan plans one remote-exec target through the real
// coordinator, with this test binary serving as the plugin.
func coordinatorPlan(t *testing.T, target config.Target) (*coordinator.PlanResult, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MU_REMOTE_EXEC_HELPER", "1")
	cs, err := oci.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target.Toolchain = "remote-exec"
	c := &coordinator.Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{{Name: "remote-exec", Command: []string{os.Args[0], "-test.run=^TestHelperProcess$"}}},
			Targets: []config.Target{target},
		},
		Store:   cs,
		Workers: 1,
	}
	return c.Plan(context.Background(), []string{target.Name})
}

// A file-mode sealed input is claimed with its mode, so strict routing
// accepts it.
func TestCoordinatorPlanFileModeInput(t *testing.T) {
	plan, err := coordinatorPlan(t, config.Target{
		Name:             "//deploy",
		Config:           map[string]any{"host": "h", "command": []any{"true"}},
		SealedInputs:     map[string]string{"DEPLOY_KEY": "env:MU_TEST_DEPLOY_KEY"},
		SealedInputModes: map[string]string{"DEPLOY_KEY": "file"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	a := plan.Graph.Actions()[0]
	if a.SealedInputModes["DEPLOY_KEY"] != "file" {
		t.Fatalf("sealed_input_modes = %v", a.SealedInputModes)
	}
}

// Wrong-typed config is rejected at plan time by the declared schema.
func TestCoordinatorRejectsWrongTypedConfig(t *testing.T) {
	_, err := coordinatorPlan(t, config.Target{
		Name:   "//bad",
		Config: map[string]any{"host": "h", "command": "uptime"},
	})
	if err == nil || !strings.Contains(err.Error(), `config field "command" must be an array`) {
		t.Fatalf("string command: err = %v, want a schema error naming command", err)
	}
}
