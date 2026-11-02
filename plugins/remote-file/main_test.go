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
// semantically identical responses. Intentional differences, all in
// discover: the Go plugin declares a config_schema, no longer advertises
// observe (mu observe is being removed), and is version 0.2.0.
func TestParityWithBB(t *testing.T) {
	p := newPlugin()
	schemaJSON, err := json.Marshal(p.ConfigSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema any
	if err := json.Unmarshal(schemaJSON, &schema); err != nil {
		t.Fatal(err)
	}
	plugintest.Parity(t, p,
		filepath.Join("testdata", "parity_requests.ndjson"),
		filepath.Join("testdata", "parity_bb_responses.ndjson"),
		func(i int, golden map[string]any) {
			if i == 0 {
				golden["config_schema"] = schema
				golden["capabilities"] = []any{"discover", "plan"}
				golden["version"] = "0.2.0"
			}
		})
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

func TestNoObserve(t *testing.T) {
	resp, err := muplugin.Exchange(context.Background(), newPlugin(), muplugin.Request{Method: "observe", Target: &muplugin.TargetInfo{Name: "//t"}})
	if err != nil {
		t.Fatal(err)
	}
	if msg, _ := resp["error"].(string); !strings.Contains(msg, "not supported") {
		t.Fatalf("observe: %v", resp)
	}
}

// TestWrapperPushesFile executes the planned wrapper against a fake ssh
// that runs the remote script locally: the source bytes are piped over
// stdin and installed at path with the requested mode, creating parents.
func TestWrapperPushesFile(t *testing.T) {
	fake := plugintest.NewFakeSSH(t)
	project := t.TempDir()
	if err := os.MkdirAll(filepath.Join(project, "conf"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "conf", "app.conf"), []byte("key = 'value'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "etc", "app", "app.conf")
	a := planOne(t, muplugin.TargetInfo{
		Name:    "//t",
		Sources: []string{"conf/app.conf"},
		Config:  map[string]any{"host": "h.example", "port": float64(2200), "path": dest, "mode": "0640"},
	})
	if !slices.Equal(a.EnvPassthrough, []string{"HOME", "SSH_AUTH_SOCK"}) || len(a.Env) != 1 {
		t.Fatalf("env = %v, env_passthrough = %v", a.Env, a.EnvPassthrough)
	}
	out, err := fake.Run(t, project, a.Command, nil)
	if err != nil {
		t.Fatalf("wrapper: %v\n%s", err, out)
	}
	got, err := os.ReadFile(dest)
	if err != nil || string(got) != "key = 'value'\n" {
		t.Fatalf("pushed file = %q, %v\n%s", got, err, out)
	}
	if fi, _ := os.Stat(dest); fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, want 0640", fi.Mode().Perm())
	}
	calls := fake.Calls(t)
	if len(calls) != 1 || calls[0].Dest != "root@h.example" || calls[0].Port != "2200" {
		t.Fatalf("ssh calls = %+v", calls)
	}
	if calls[0].Stdin != "key = 'value'\n" {
		t.Fatalf("ssh stdin = %q", calls[0].Stdin)
	}
}

// TestHelperProcess runs the plugin over stdio when launched by the
// coordinator tests.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("MU_REMOTE_FILE_HELPER") != "1" {
		return
	}
	newPlugin().Main()
	os.Exit(0)
}

func coordinatorPlan(t *testing.T, target config.Target) (*coordinator.PlanResult, error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MU_REMOTE_FILE_HELPER", "1")
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "motd"), []byte("hi\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cs, err := oci.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target.Toolchain = "remote-file"
	target.Sources = []string{"motd"}
	c := &coordinator.Coordinator{
		ProjectRoot: project,
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{{Name: "remote-file", Command: []string{os.Args[0], "-test.run=^TestHelperProcess$"}}},
			Targets: []config.Target{target},
		},
		Store:   cs,
		Workers: 1,
	}
	return c.Plan(context.Background(), []string{target.Name})
}

// The bb plugin forwarded sealed_inputs without their modes, so a
// file-mode input failed strict routing with a mode mismatch. The Go
// plugin claims modes too.
func TestCoordinatorPlanFileModeInput(t *testing.T) {
	plan, err := coordinatorPlan(t, config.Target{
		Name:             "//motd",
		Config:           map[string]any{"host": "h", "path": "/etc/motd"},
		SealedInputs:     map[string]string{"SSH_PASS": "env:MU_TEST_PASS", "DEPLOY_KEY": "env:MU_TEST_KEY"},
		SealedInputModes: map[string]string{"DEPLOY_KEY": "file"},
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	a := plan.Graph.Actions()[0]
	if a.SealedInputModes["DEPLOY_KEY"] != "file" || a.SealedInputs["SSH_PASS"] != "env:MU_TEST_PASS" {
		t.Fatalf("sealed claims = %v / %v", a.SealedInputs, a.SealedInputModes)
	}
}

func TestCoordinatorRejectsWrongTypedConfig(t *testing.T) {
	_, err := coordinatorPlan(t, config.Target{
		Name:   "//motd",
		Config: map[string]any{"host": "h", "path": "/etc/motd", "make_parents": "yes"},
	})
	if err == nil || !strings.Contains(err.Error(), `config field "make_parents" must be a boolean`) {
		t.Fatalf("err = %v, want a schema error naming make_parents", err)
	}
}
