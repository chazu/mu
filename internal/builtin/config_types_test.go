package builtin

import (
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
)

// TestBuiltinConfig_WrongTypesRejected checks that a wrongly typed shell or
// secret-gen config field is an error, not silently defaulted or dropped.
func TestBuiltinConfig_WrongTypesRejected(t *testing.T) {
	cases := []struct {
		name    string
		plan    func(config.Target) error
		tool    string
		cfg     map[string]any
		wantSub string
	}{
		{"shell network string", shellErr, "shell", map[string]any{"command": []any{"true"}, "network": "no"}, `"network" must be a bool`},
		{"shell impure string", shellErr, "shell", map[string]any{"command": []any{"true"}, "impure": "false"}, `"impure" must be a bool`},
		{"shell env not map", shellErr, "shell", map[string]any{"command": []any{"true"}, "env": []any{"A=b"}}, `"env" must be a map of strings`},
		{"shell env non-string value", shellErr, "shell", map[string]any{"command": []any{"true"}, "env": map[string]any{"A": int64(1)}}, `"env" entry "A" must be a string`},
		{"shell outputs not list", shellErr, "shell", map[string]any{"command": []any{"true"}, "outputs": "out"}, `"outputs" must be an array of strings`},
		{"secret-gen keep_trailing_newline string", secretGenErr, "secret-gen", map[string]any{"derivation": []any{"true"}, "keep_trailing_newline": "yes"}, `"keep_trailing_newline" must be a bool`},
		{"secret-gen env non-string value", secretGenErr, "secret-gen", map[string]any{"derivation": []any{"true"}, "env": map[string]any{"A": true}}, `"env" entry "A" must be a string`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.plan(config.Target{
				Name:          "//t",
				Toolchain:     tc.tool,
				Config:        tc.cfg,
				SealedOutputs: map[string]string{"VALUE": "pass:x/y"},
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("error %q does not contain %q", err, tc.wantSub)
			}
		})
	}
}

func shellErr(t config.Target) error {
	t.SealedOutputs = nil
	_, _, err := ShellPlan(t)
	return err
}

func secretGenErr(t config.Target) error {
	_, _, err := SecretGenPlan(t)
	return err
}
