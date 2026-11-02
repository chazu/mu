package builtin

import (
	"context"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/dag"
	"github.com/chazu/mu/internal/plugin"
)

func TestSecretGenPlan_DefaultsToCreateIfAbsent(t *testing.T) {
	target := config.Target{
		Name:          "//secrets/admin-pass",
		Toolchain:     "secret-gen",
		SealedOutputs: map[string]string{"VALUE": "pass:registry/admin"},
		Config: map[string]any{
			"derivation": []any{"openssl", "rand", "-base64", "24"},
		},
	}
	actions, _, err := SecretGenPlan(target)
	if err != nil {
		t.Fatalf("SecretGenPlan: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	a := actions[0]
	if a.SealedOutputs["VALUE"] != "pass:registry/admin" {
		t.Errorf("SealedOutputs[VALUE] = %q, want pass:registry/admin", a.SealedOutputs["VALUE"])
	}
	if a.SealedOutputModes["VALUE"] != plugin.StoreSecretModeCreateIfAbsent {
		t.Errorf("SealedOutputModes[VALUE] = %q, want create_if_absent", a.SealedOutputModes["VALUE"])
	}
	if !a.Impure {
		t.Error("Impure = false, want true")
	}
}

func TestSecretGenPlan_ClaimsTargetSealedInputs(t *testing.T) {
	target := config.Target{
		Name:             "//secrets/htpasswd",
		Toolchain:        "secret-gen",
		SealedInputs:     map[string]string{"RAW_PASS": "pass:registry/raw"},
		SealedInputModes: map[string]string{"RAW_PASS": "env"},
		SealedOutputs:    map[string]string{"VALUE": "pass:registry/htpasswd"},
		Config: map[string]any{
			"derivation": []any{"sh", "-c", `printf '%s' "$RAW_PASS"`},
		},
	}
	actions, _, err := SecretGenPlan(target)
	if err != nil {
		t.Fatalf("SecretGenPlan: %v", err)
	}
	a := actions[0]
	if a.SealedInputs["RAW_PASS"] != "pass:registry/raw" || a.SealedInputModes["RAW_PASS"] != "env" {
		t.Fatalf("sealed input claims = %v / %v, want the target's RAW_PASS", a.SealedInputs, a.SealedInputModes)
	}
}

// The single sealed_outputs entry names the destination under any name;
// its sealed_output_modes entry, when present, overrides the default.
func TestSecretGenPlan_SealedOutputNameAndMode(t *testing.T) {
	target := config.Target{
		Name:              "//x",
		Toolchain:         "secret-gen",
		SealedOutputs:     map[string]string{"OUT": "pass:x/y"},
		SealedOutputModes: map[string]string{"OUT": "overwrite"},
		Config:            map[string]any{"derivation": []any{"echo", "hunter2"}},
	}
	actions, _, err := SecretGenPlan(target)
	if err != nil {
		t.Fatalf("SecretGenPlan: %v", err)
	}
	a := actions[0]
	if a.SealedOutputs["OUT"] != "pass:x/y" || a.SealedOutputModes["OUT"] != "overwrite" || len(a.SealedOutputs) != 1 {
		t.Fatalf("claims = %v / %v, want OUT -> pass:x/y overwrite", a.SealedOutputs, a.SealedOutputModes)
	}
	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), a.Command[0], a.Command[1:]...)
	cmd.Env = append([]string{}, "MU_SEALED_OUT_DIR="+dir, "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated script failed: %v\noutput: %s", err, out)
	}
	if got := readFile(t, dir+"/OUT"); got != "hunter2" {
		t.Errorf("stored value = %q, want hunter2", got)
	}
}

func TestSecretGenDefaults_DoesNotMutateTarget(t *testing.T) {
	modes := map[string]string{}
	target := config.Target{SealedOutputs: map[string]string{"VALUE": "pass:x"}, SealedOutputModes: modes}
	got := SecretGenDefaults(target)
	if got.SealedOutputModes["VALUE"] != plugin.StoreSecretModeCreateIfAbsent {
		t.Errorf("default mode = %q, want create_if_absent", got.SealedOutputModes["VALUE"])
	}
	if len(modes) != 0 {
		t.Errorf("caller's modes map mutated: %v", modes)
	}
}

func TestSecretGenPlan_Rejects(t *testing.T) {
	for _, tc := range []struct {
		name    string
		outputs map[string]string
		modes   map[string]string
		cfg     map[string]any
		want    string
	}{
		{
			name: "config.ref removed",
			cfg:  map[string]any{"ref": "pass:x/y", "derivation": []any{"true"}},
			want: `config.ref was removed; declare sealed_outputs: {VALUE: "pass:x/y"}`,
		},
		{
			name:    "config.ref removed even with sealed_outputs",
			outputs: map[string]string{"VALUE": "pass:x/y"},
			cfg:     map[string]any{"ref": "pass:x/y", "derivation": []any{"true"}},
			want:    "config.ref was removed",
		},
		{
			name:    "config.mode removed",
			outputs: map[string]string{"VALUE": "pass:x/y"},
			cfg:     map[string]any{"mode": "overwrite", "derivation": []any{"true"}},
			want:    `config.mode was removed; declare sealed_output_modes: {VALUE: "overwrite"}`,
		},
		{
			name: "no sealed_outputs",
			cfg:  map[string]any{"derivation": []any{"true"}},
			want: "exactly one sealed_outputs entry",
		},
		{
			name:    "two sealed_outputs",
			outputs: map[string]string{"A": "pass:a", "B": "pass:b"},
			cfg:     map[string]any{"derivation": []any{"true"}},
			want:    "exactly one sealed_outputs entry",
		},
		{
			name:    "ref without scheme",
			outputs: map[string]string{"VALUE": "no-scheme"},
			cfg:     map[string]any{"derivation": []any{"true"}},
			want:    "scheme:path",
		},
		{
			name:    "unknown mode",
			outputs: map[string]string{"VALUE": "pass:x/y"},
			modes:   map[string]string{"VALUE": "wat"},
			cfg:     map[string]any{"derivation": []any{"true"}},
			want:    "must be one of",
		},
		{
			name:    "path-like output name",
			outputs: map[string]string{"../VALUE": "pass:x/y"},
			cfg:     map[string]any{"derivation": []any{"true"}},
			want:    "plain file name",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := SecretGenPlan(config.Target{
				Name: "//x", Toolchain: "secret-gen",
				SealedOutputs: tc.outputs, SealedOutputModes: tc.modes, Config: tc.cfg,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

// TestSecretGenPlan_DerivationStdoutTrimmed runs the actual generated
// shell command and confirms that a trailing newline from the
// derivation (echo's default behavior) is stripped before storing.
func TestSecretGenPlan_DerivationStdoutTrimmed(t *testing.T) {
	target := config.Target{
		Name:          "//x",
		Toolchain:     "secret-gen",
		SealedOutputs: map[string]string{"VALUE": "pass:x/y"},
		Config: map[string]any{
			"derivation": []any{"echo", "hunter2"},
		},
	}
	actions, _, err := SecretGenPlan(target)
	if err != nil {
		t.Fatalf("SecretGenPlan: %v", err)
	}

	// Run the generated script in a temp dir with MU_SEALED_OUT_DIR set,
	// then read what got written.
	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), actions[0].Command[0], actions[0].Command[1:]...)
	cmd.Env = append([]string{}, "MU_SEALED_OUT_DIR="+dir, "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("generated script failed: %v\noutput: %s", err, out)
	}

	value := readFile(t, dir+"/VALUE")
	if value != "hunter2" {
		t.Errorf("stored value = %q, want %q (trailing newline must be trimmed)", value, "hunter2")
	}
}

func TestSecretGenPlan_KeepTrailingNewline(t *testing.T) {
	target := config.Target{
		Name:          "//x",
		Toolchain:     "secret-gen",
		SealedOutputs: map[string]string{"VALUE": "pass:x/y"},
		Config: map[string]any{
			"derivation":            []any{"echo", "hunter2"},
			"keep_trailing_newline": true,
		},
	}
	actions, _, err := SecretGenPlan(target)
	if err != nil {
		t.Fatalf("SecretGenPlan: %v", err)
	}

	dir := t.TempDir()
	cmd := exec.CommandContext(context.Background(), actions[0].Command[0], actions[0].Command[1:]...)
	cmd.Env = append([]string{}, "MU_SEALED_OUT_DIR="+dir, "PATH=/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generated script failed: %v\noutput: %s", err, out)
	}

	value := readFile(t, dir+"/VALUE")
	if !strings.HasSuffix(value, "hunter2\n") {
		t.Errorf("stored value = %q, want trailing newline preserved", value)
	}
}

// runSecretGen plans a secret-gen target and executes its action through
// the real executor, returning the values routed to store_secret and the
// failure error (nil on success).
func runSecretGen(t *testing.T, cfg map[string]any) ([]string, error) {
	t.Helper()
	actions, _, err := SecretGenPlan(config.Target{
		Name: "//x", Toolchain: "secret-gen",
		SealedOutputs: map[string]string{"VALUE": "pass:x/y"}, Config: cfg,
	})
	if err != nil {
		t.Fatalf("SecretGenPlan: %v", err)
	}
	spec := actions[0]
	env := map[string]string{"PATH": "/usr/bin:/bin:/usr/local/bin:/opt/homebrew/bin"}
	for k, v := range spec.Env {
		env[k] = v
	}
	g := dag.NewGraph()
	if err := g.AddAction(&dag.Action{
		ID:                spec.ID,
		Command:           spec.Command,
		Env:               env,
		WorkDir:           t.TempDir(),
		Impure:            true,
		SealedOutputs:     spec.SealedOutputs,
		SealedOutputModes: spec.SealedOutputModes,
	}); err != nil {
		t.Fatalf("AddAction: %v", err)
	}
	store, err := oci.NewLocal(filepath.Join(t.TempDir(), "cas"))
	if err != nil {
		t.Fatalf("oci.NewLocal: %v", err)
	}
	var stored []string
	ex := &dag.Executor{
		Store:   store,
		Workers: 1,
		SealedOutputWriter: func(_ context.Context, _, value, _ string) error {
			stored = append(stored, value)
			return nil
		},
	}
	res, err := ex.Execute(context.Background(), g)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(res.Failed) > 0 {
		return stored, res.Failed[0].Err
	}
	return stored, nil
}

func TestSecretGenExecute(t *testing.T) {
	cases := []struct {
		name        string
		derivation  []any
		keepNewline bool
		want        string // stored value; "" means the build must fail
	}{
		{"failing derivation", []any{"false"}, false, ""},
		{"failing derivation keep newline", []any{"false"}, true, ""},
		{"strips one newline", []any{"printf", `abc\n\n`}, false, "abc\n"},
		{"strips newline", []any{"printf", `abc\n`}, false, "abc"},
		{"keep newline", []any{"printf", `abc\n`}, true, "abc\n"},
		{"empty output", []any{"true"}, false, ""},
		{"empty output keep newline", []any{"true"}, true, ""},
		{"only newline", []any{"echo"}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored, err := runSecretGen(t, map[string]any{
				"derivation":            tc.derivation,
				"keep_trailing_newline": tc.keepNewline,
			})
			if tc.want == "" {
				if err == nil {
					t.Fatalf("build succeeded, want failure (stored %q)", stored)
				}
				if len(stored) != 0 {
					t.Fatalf("stored %q on failure, want nothing", stored)
				}
				return
			}
			if err != nil {
				t.Fatalf("build failed: %v", err)
			}
			if len(stored) != 1 || stored[0] != tc.want {
				t.Fatalf("stored = %q, want [%q]", stored, tc.want)
			}
		})
	}
}

// guard against parallel state leaking across builtin tests; the suite
// is small so a sync.Once for any shared init is unnecessary, but
// having the import keeps future additions easy.
var _ = sync.Once{}

func readFile(t *testing.T, path string) string {
	t.Helper()
	out, err := exec.Command("cat", path).Output()
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(out)
}
