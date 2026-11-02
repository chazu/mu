package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// loadSnippet writes body as a project root mu.cue in a fresh directory and
// loads it.
func loadSnippet(t *testing.T, body string) (*ProjectConfig, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mu.cue"), []byte("package mu\n\n"+body+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return Load(dir)
}

// TestClosedSchema_RejectsUnknownFields covers the typos that used to load
// silently: each must now fail, naming the offending field.
func TestClosedSchema_RejectsUnknownFields(t *testing.T) {
	cases := []struct {
		name string
		body string
		want []string // substrings of the error
	}{
		{
			name: "secrets writable_ref typo",
			body: `secrets: {writable_ref: ["pass:bootstrap/*"]}`,
			want: []string{"secrets.writable_ref", "unknown field", `did you mean "writable_refs"`},
		},
		{
			name: "target sealed_routng typo",
			body: `targets: [{target: "//a", toolchain: "go", sealed_routng: "strict"}]`,
			want: []string{"target //a: field sealed_routng", "unknown field"},
		},
		{
			name: "shell network wrong type",
			body: `targets: [{target: "//a", toolchain: "shell", config: {command: ["true"], network: "no"}}]`,
			want: []string{"target //a: field config.network", "bool"},
		},
		{
			name: "shell config typo",
			body: `targets: [{target: "//a", toolchain: "shell", config: {comand: ["true"]}}]`,
			want: []string{"target //a: field config.comand", `did you mean "command"`},
		},
		{
			name: "shell missing command",
			body: `targets: [{target: "//a", toolchain: "shell", config: {impure: false}}]`,
			want: []string{"target //a: field config.command", "missing required field"},
		},
		{
			name: "shell missing config",
			body: `targets: [{target: "//a", toolchain: "shell"}]`,
			want: []string{"target //a: field config", "missing required field"},
		},
		{
			name: "shell env wrong value type",
			body: `targets: [{target: "//a", toolchain: "shell", config: {command: ["true"], env: {A: 1}}}]`,
			want: []string{"target //a: field config.env.A"},
		},
		{
			name: "secret-gen config typo",
			body: `targets: [{target: "//a", toolchain: "secret-gen", sealed_outputs: {V: "pass:x"}, config: {derivation: ["true"], keep_trailing_newlines: true}}]`,
			want: []string{"field config.keep_trailing_newlines", `did you mean "keep_trailing_newline"`},
		},
		{
			name: "toolchain config typo",
			body: `toolchains: [{toolchain: "x", from: "scratch", config: {versoin: "1"}}]`,
			want: []string{"toolchains[0].config.versoin", `did you mean "version"`},
		},
		{
			name: "top-level typo",
			body: `toolchain: []`,
			want: []string{"toolchain", "unknown field", `did you mean "toolchains"`},
		},
		{
			name: "cache typo",
			body: `cache: {remot: "h/n/r"}`,
			want: []string{"cache.remot", `did you mean "remote"`},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadSnippet(t, tc.body)
			if err == nil {
				t.Fatal("expected load error, got nil")
			}
			msg := err.Error()
			for _, w := range tc.want {
				if !strings.Contains(msg, w) {
					t.Errorf("error does not contain %q:\n%s", w, msg)
				}
			}
			if !strings.Contains(msg, "mu.cue:") {
				t.Errorf("error does not name the file:\n%s", msg)
			}
		})
	}
}

// TestClosedSchema_PluginConfigStaysOpen checks that a plugin target's
// config is still free-form: the plugin validates it, not the schema.
func TestClosedSchema_PluginConfigStaysOpen(t *testing.T) {
	cfg, err := loadSnippet(t, `targets: [{target: "//a", toolchain: "go", config: {anything: 1, nested: {x: true}}}]`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Targets[0].Config["anything"]; got == nil {
		t.Fatalf("config.anything not decoded: %#v", cfg.Targets[0].Config)
	}
}

// TestClosedSchema_BuiltinConfigsAccepted loads every documented shell and
// secret-gen config field.
func TestClosedSchema_BuiltinConfigsAccepted(t *testing.T) {
	_, err := loadSnippet(t, `targets: [{
	target:    "//sh"
	toolchain: "shell"
	config: {
		command: ["true"]
		env: {A: "b"}
		network: true
		impure:  false
		outputs: ["out"]
		host_paths: ["/usr/bin"]
		env_passthrough: ["HOME"]
	}
}, {
	target:    "//gen"
	toolchain: "secret-gen"
	sealed_outputs: {VALUE: "pass:bootstrap/x"}
	config: {
		derivation: ["openssl", "rand", "-hex", "8"]
		env: {A: "b"}
		keep_trailing_newline: true
	}
}]`)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
}

// TestClosedSchema_RemovedFieldsKeepFriendlyErrors checks that a field mu
// removed still gets its migration message rather than the generic
// unknown-field error.
func TestClosedSchema_RemovedFieldsKeepFriendlyErrors(t *testing.T) {
	cases := []struct {
		name, body, want string
	}{
		{"sealed_routing", `targets: [{target: "//a", toolchain: "go", sealed_routing: "strict"}]`, "sealed_routing was removed"},
		{"observe_command", `targets: [{target: "//a", toolchain: "shell", config: {command: ["true"], observe_command: ["x"]}}]`, "observe_command was removed"},
		{"secret-gen ref", `targets: [{target: "//a", toolchain: "secret-gen", config: {derivation: ["true"], ref: "pass:x"}}]`, `config.ref was removed; declare sealed_outputs: {VALUE: "pass:x"}`},
		{"secret-gen mode", `targets: [{target: "//a", toolchain: "secret-gen", sealed_outputs: {V: "pass:x"}, config: {derivation: ["true"], mode: "overwrite"}}]`, `config.mode was removed; declare sealed_output_modes: {VALUE: "overwrite"}`},
		{"advice", `advice: []`, "advice was removed"},
		{"cache backends", `cache: {backends: []}`, "cache.backends was removed"},
		{"plan", `targets: [{target: "//a", plan: "x"}]`, "inline pith programs were removed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadSnippet(t, tc.body)
			if err == nil {
				t.Fatal("expected load error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error does not contain %q:\n%s", tc.want, err)
			}
		})
	}
}
