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
		Name:      "//secrets/admin-pass",
		Toolchain: "secret-gen",
		Config: map[string]any{
			"ref":        "pass:registry/admin",
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

func TestSecretGenPlan_RejectsBadRef(t *testing.T) {
	target := config.Target{
		Name:      "//x",
		Toolchain: "secret-gen",
		Config: map[string]any{
			"ref":        "no-scheme",
			"derivation": []any{"true"},
		},
	}
	if _, _, err := SecretGenPlan(target); err == nil {
		t.Fatal("expected error for ref without scheme")
	}
}

func TestSecretGenPlan_RejectsUnknownMode(t *testing.T) {
	target := config.Target{
		Name:      "//x",
		Toolchain: "secret-gen",
		Config: map[string]any{
			"ref":        "pass:x/y",
			"derivation": []any{"true"},
			"mode":       "wat",
		},
	}
	if _, _, err := SecretGenPlan(target); err == nil {
		t.Fatal("expected error for invalid mode")
	}
}

// TestSecretGenPlan_DerivationStdoutTrimmed runs the actual generated
// shell command and confirms that a trailing newline from the
// derivation (echo's default behavior) is stripped before storing.
func TestSecretGenPlan_DerivationStdoutTrimmed(t *testing.T) {
	target := config.Target{
		Name:      "//x",
		Toolchain: "secret-gen",
		Config: map[string]any{
			"ref":        "pass:x/y",
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
		Name:      "//x",
		Toolchain: "secret-gen",
		Config: map[string]any{
			"ref":                   "pass:x/y",
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
	actions, _, err := SecretGenPlan(config.Target{Name: "//x", Toolchain: "secret-gen", Config: cfg})
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
				"ref":                   "pass:x/y",
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
