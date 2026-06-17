package builtin

import (
	"context"
	"os/exec"
	"strings"
	"sync"
	"testing"

	"github.com/chazu/mu/internal/config"
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
