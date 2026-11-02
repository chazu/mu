package main

import (
	"bufio"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/plugin"
	"github.com/chazu/mu/plugins/internal/plugintest"
	"github.com/chazu/mu/sdk/muplugin"
)

// useFakePass points PATH at testdata/fakepass (plus the minimal system
// dirs the fake script needs) and gives it a fresh store seeded from
// testdata/seed. The real `pass` and ~/.password-store are never reachable.
// It returns the store directory.
func useFakePass(t *testing.T) string {
	t.Helper()
	fakeDir, err := filepath.Abs(filepath.Join("testdata", "fakepass"))
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	seed := filepath.Join("testdata", "seed")
	err = filepath.WalkDir(seed, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(seed, path)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		dst := filepath.Join(store, rel)
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		return os.WriteFile(dst, data, 0o600)
	})
	if err != nil {
		t.Fatalf("seed fake store: %v", err)
	}
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+"/usr/bin:/bin")
	t.Setenv("FAKE_PASS_STORE", store)
	t.Setenv("PASSWORD_STORE_DIR", filepath.Join(store, ".unused"))
	return store
}

// TestParityWithBB replays the request script the retired plugin.bb
// answered (captured in testdata/parity_bb_responses.ndjson against the
// same fake store) and requires semantically identical responses.
func TestParityWithBB(t *testing.T) {
	useFakePass(t)
	plugintest.Parity(t, newPlugin(),
		filepath.Join("testdata", "parity_requests.ndjson"),
		filepath.Join("testdata", "parity_bb_responses.ndjson"), nil)
}

func TestResolveFirstLineAndRaw(t *testing.T) {
	useFakePass(t)
	ctx := context.Background()
	got, err := passBackend{}.Resolve(ctx, "db/login")
	if err != nil || got != "hunter2" {
		t.Fatalf("Resolve(db/login) = %q, %v; want hunter2", got, err)
	}
	got, err = passBackend{}.Resolve(ctx, "raw:certs/key")
	if err != nil {
		t.Fatal(err)
	}
	if want := "-----BEGIN TEST KEY-----\nabc def\n-----END TEST KEY-----"; got != want {
		t.Fatalf("Resolve(raw:certs/key) = %q, want %q", got, want)
	}
	if _, err := (passBackend{}).Resolve(ctx, "missing"); err == nil ||
		!strings.Contains(err.Error(), "is not in the password store") {
		t.Fatalf("Resolve(missing) err = %v", err)
	}
}

func TestStoreModes(t *testing.T) {
	store := useFakePass(t)
	ctx := context.Background()
	b := passBackend{}
	read := func(path string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(store, path))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	if err := b.Store(ctx, "x/y", "one", "create"); err != nil {
		t.Fatal(err)
	}
	if err := b.Store(ctx, "x/y", "two", "create"); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("create over existing: err = %v", err)
	}
	if err := b.Store(ctx, "x/y", "three", "create_if_absent"); err != nil {
		t.Fatal(err)
	}
	if got := read("x/y"); got != "one" {
		t.Fatalf("after create_if_absent: %q, want one", got)
	}
	if err := b.Store(ctx, "raw:x/y", "four\nfive", "overwrite"); err != nil {
		t.Fatal(err)
	}
	if got := read("x/y"); got != "four\nfive" {
		t.Fatalf("after overwrite via raw: ref: %q", got)
	}
	if err := b.Store(ctx, "x/z", "dflt", ""); err != nil {
		t.Fatal(err)
	}
	if got := read("x/z"); got != "dflt" {
		t.Fatalf("empty mode should overwrite: %q", got)
	}
	if err := b.Store(ctx, "x/w", "v", "sideways"); err == nil || !strings.Contains(err.Error(), "unknown secret_mode") {
		t.Fatalf("bad mode: err = %v", err)
	}
	if err := b.Store(ctx, "x/w", "", "overwrite"); err == nil {
		t.Fatal("empty value should be rejected")
	}
}

// TestLocalPluginDirResolvesPassRefs is the end-to-end check for the Go
// plugin path: the plugin directory (mu.cue + compiled `plugin` binary) is
// registered with `script:` exactly as a project would, and a strict
// sealed-routing build reads a pass: ref and writes another one through it.
func TestLocalPluginDirResolvesPassRefs(t *testing.T) {
	if testing.Short() {
		t.Skip("builds the plugin binary")
	}
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}

	project := t.TempDir()
	pluginDir := filepath.Join(project, "plugins", "pass")
	if err := os.MkdirAll(pluginDir, 0o755); err != nil {
		t.Fatal(err)
	}
	build := exec.Command(goBin, "build", "-trimpath", "-o", filepath.Join(pluginDir, "plugin"), ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, out)
	}
	for _, f := range []string{"mu.cue", "GUIDE.md"} {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pluginDir, f), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("HOME", t.TempDir()) // plugin bundle cache + discover cache
	store := useFakePass(t)
	t.Setenv("MU_PASS_COPIER_HELPER", "1")

	cs, err := oci.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &coordinator.Coordinator{
		ProjectRoot: project,
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{
				{Name: "pass", Script: "plugins/pass"},
				{Name: "copier", Command: []string{os.Args[0], "-test.run=^TestCopierHelperProcess$"}},
			},
			Secrets: &config.SecretsConfig{WritableRefs: []string{"pass:dst/*"}},
			Targets: []config.Target{{
				Name: "//copy", Toolchain: "copier",
				SealedInputs:      map[string]string{"IN": "pass:db/login"},
				SealedInputModes:  map[string]string{"IN": "env"},
				SealedOutputs:     map[string]string{"OUT": "pass:dst/token"},
				SealedOutputModes: map[string]string{"OUT": "create"},
			}},
		},
		Store:   cs,
		Workers: 1,
	}
	ctx := context.Background()
	plan, err := c.Plan(ctx, []string{"//copy"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	result, err := c.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if n := len(result.ExecResult.Failed); n != 0 {
		t.Fatalf("%d actions failed: %+v", n, result.ExecResult.Failed)
	}
	got, err := os.ReadFile(filepath.Join(store, "dst", "token"))
	if err != nil {
		t.Fatalf("sealed output was not stored through the pass plugin: %v", err)
	}
	if string(got) != "hunter2" {
		t.Fatalf("stored %q, want first line of db/login", got)
	}
}

// TestCopierHelperProcess is a minimal planner launched as a subprocess by
// TestLocalPluginDirResolvesPassRefs: one action copies sealed input IN to
// sealed output OUT, claiming the target's refs and modes exactly.
func TestCopierHelperProcess(t *testing.T) {
	if os.Getenv("MU_PASS_COPIER_HELPER") != "1" {
		return
	}
	enc := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		var req plugin.Request
		if err := json.Unmarshal(sc.Bytes(), &req); err != nil {
			os.Exit(2)
		}
		switch req.Method {
		case "discover":
			_ = enc.Encode(plugin.DiscoverResponse{
				Name: "copier", Version: "test", ProtocolVersion: plugin.ProtocolVersion,
				Capabilities: []string{"discover", "plan"},
			})
		case "plan":
			_ = enc.Encode(plugin.PlanResponse{Actions: []muplugin.ActionSpec{{
				ID:                "copy",
				Command:           []string{"sh", "-c", `printf '%s' "$IN" > "$MU_SEALED_OUT_DIR/OUT"`},
				SealedInputs:      req.Target.SealedInputs,
				SealedInputModes:  req.Target.SealedInputModes,
				SealedOutputs:     req.Target.SealedOutputs,
				SealedOutputModes: req.Target.SealedOutputModes,
			}}})
		default:
			_ = enc.Encode(map[string]string{"error": "unsupported"})
		}
	}
	os.Exit(0)
}

// TestPassHelperProcess serves the pass plugin over stdio when launched by
// TestTargetConfigForPassRejectedAtPlan.
func TestPassHelperProcess(t *testing.T) {
	if os.Getenv("MU_PASS_PLUGIN_HELPER") != "1" {
		return
	}
	newPlugin().Main()
	os.Exit(0)
}

// pass is a secret provider: it takes no target config and cannot plan,
// so it declares no config_schema (mu warns about a missing schema only
// for plan-capable plugins). A target that names it as its toolchain and
// passes config is rejected at plan time rather than silently accepted.
func TestTargetConfigForPassRejectedAtPlan(t *testing.T) {
	useFakePass(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MU_PASS_PLUGIN_HELPER", "1")
	cs, err := oci.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &coordinator.Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Plugins: []config.PluginDef{{Name: "pass", Command: []string{os.Args[0], "-test.run=^TestPassHelperProcess$"}}},
			Targets: []config.Target{{Name: "//p", Toolchain: "pass", Config: map[string]any{"store_dir": 5}}},
		},
		Store:   cs,
		Workers: 1,
	}
	_, err = c.Plan(context.Background(), []string{"//p"})
	if err == nil || !strings.Contains(err.Error(), "plan: capability not supported") {
		t.Fatalf("Plan err = %v, want plan-capability rejection", err)
	}
}
