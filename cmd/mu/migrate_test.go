package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	cuejson "cuelang.org/go/encoding/json"
)

// semanticEqual reports whether the emitted CUE bytes round-trip to the
// same canonical-JSON shape as the original JSON bytes. This is a
// slightly lighter version of verifyParity intended for use from tests:
// both docs are normalized via cue.Value.MarshalJSON and then
// JSON-unmarshalled into generic Go maps.
func semanticEqual(t *testing.T, jsonBytes, cueBytes []byte) bool {
	t.Helper()
	ctx := cuecontext.New()
	expr, err := cuejson.Extract("a.json", jsonBytes)
	if err != nil {
		t.Fatalf("cuejson.Extract: %v", err)
	}
	v1 := ctx.BuildExpr(expr)
	if err := v1.Err(); err != nil {
		t.Fatalf("build JSON value: %v", err)
	}
	v2 := ctx.CompileBytes(cueBytes, cue.Filename("b.cue"))
	if err := v2.Err(); err != nil {
		t.Fatalf("compile CUE bytes: %v", err)
	}
	a, err := v1.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal v1: %v", err)
	}
	b, err := v2.MarshalJSON()
	if err != nil {
		t.Fatalf("marshal v2: %v", err)
	}
	var ax, bx any
	if err := json.Unmarshal(a, &ax); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &bx); err != nil {
		t.Fatal(err)
	}
	return reflect.DeepEqual(ax, bx)
}

// TestConvertJSONToCUE_Fixtures exercises the core emitter against a
// matrix of representative JSON inputs: root project config, plugin dir
// config, and tricky corner cases (empty arrays, nested maps, bool
// tri-state, preprocessor declaration).
func TestConvertJSONToCUE_Fixtures(t *testing.T) {
	cases := []struct {
		name      string
		jsonInput string
		isPlugin  bool
		wantKeys  []string
	}{
		{
			name: "root_project",
			jsonInput: `{
				"cache": {"backends": [{"type":"disk","path":"/tmp/a"}], "read_repair": true},
				"toolchains": [{"toolchain":"go","from":"scratch","config":{"version":"1.25.0"}}],
				"plugins": [{"name":"go","script":"plugins/go"}],
				"targets": [
					{"target":"//a","toolchain":"go","sources":["a.go"],"config":{"pkg":"./a"}}
				]
			}`,
			wantKeys: []string{"cache", "toolchains", "plugins", "targets"},
		},
		{
			name: "plugin_dir",
			jsonInput: `{
				"plugin": {"entrypoint":"plugin.bb","toolchain":"bb","files":["plugin.bb"],"guide":"GUIDE.md"},
				"targets": [{"target":"build","toolchain":"shell","sources":["plugin.bb"],"config":{"command":["true"],"impure":false}}]
			}`,
			isPlugin: true,
			wantKeys: []string{"plugin", "targets"},
		},
		{
			name: "empty_arrays",
			jsonInput: `{
				"targets": [
					{"target":"//x","toolchain":"shell","sources":[],"deps":[],"config":{"command":["true"]}}
				]
			}`,
			wantKeys: []string{"targets"},
		},
		{
			name: "nil_bool_tristate",
			jsonInput: `{
				"cache": {"backends": [
					{"type":"disk","path":"/tmp/a"},
					{"type":"oci","registry":"r1","write":false},
					{"type":"oci","registry":"r2","read":true,"write":false}
				]}
			}`,
			wantKeys: []string{"cache"},
		},
		{
			name: "nested_config_map",
			jsonInput: `{
				"targets": [{"target":"//n","toolchain":"go","config":{"env":{"FOO":"bar","BAZ":"qux"},"nested":{"a":{"b":{"c":1}}}}}]
			}`,
			wantKeys: []string{"targets"},
		},
		{
			name: "preprocessor_declared",
			jsonInput: `{
				"preprocessor": {"extension":"edn","command":["bb","pp.bb"]},
				"targets": []
			}`,
			wantKeys: []string{"preprocessor", "targets"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := convertJSONToCUE("test.json", []byte(tc.jsonInput))
			if err != nil {
				t.Fatalf("convertJSONToCUE: %v", err)
			}
			if !bytes.HasPrefix(bytes.TrimLeftFunc(out, isSpaceOrComment), []byte("package mu")) {
				// Check that "package mu" appears near the top.
				if !bytes.Contains(out, []byte("package mu")) {
					t.Errorf("output missing 'package mu' header:\n%s", out)
				}
			}
			if tc.isPlugin {
				if !bytes.Contains(out, []byte("PluginConfig")) {
					t.Errorf("expected plugin header mention, got:\n%s", out)
				}
			} else {
				if !bytes.Contains(out, []byte("ProjectConfig")) {
					t.Errorf("expected project header mention, got:\n%s", out)
				}
			}
			for _, k := range tc.wantKeys {
				if !bytes.Contains(out, []byte(k+":")) {
					t.Errorf("output missing expected key %q:\n%s", k, out)
				}
			}
			if !semanticEqual(t, []byte(tc.jsonInput), out) {
				t.Errorf("semantic mismatch between JSON and emitted CUE:\n--- json\n%s\n--- cue\n%s", tc.jsonInput, out)
			}
		})
	}
}

// isSpaceOrComment is used by TrimLeftFunc to skip over leading
// whitespace and comment lines when asserting the package header.
func isSpaceOrComment(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r' || r == '/'
}

// TestMigrateOne_RepoMuJSON is the integration acceptance test: copy
// this repository's own mu.json into a temp dir, migrate it, assert
// parity, and assert the emitted CUE is loadable + compilable.
func TestMigrateOne_RepoMuJSON(t *testing.T) {
	// Find repo root by walking up until we see a go.mod.
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for dir := root; dir != "/"; dir = filepath.Dir(dir) {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			root = dir
			break
		}
	}
	src := filepath.Join(root, "mu.json")
	data, err := os.ReadFile(src)
	if err != nil {
		t.Skipf("repo mu.json not available: %v", err)
	}

	tmp := t.TempDir()
	inPath := filepath.Join(tmp, "mu.json")
	outPath := filepath.Join(tmp, "mu.cue")
	if err := os.WriteFile(inPath, data, 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := migrateOne(inPath, outPath, false, true /*keepJSON*/, &buf); err != nil {
		t.Fatalf("migrateOne: %v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Fatalf("output CUE not written: %v", err)
	}
	if _, err := os.Stat(inPath); err != nil {
		t.Errorf("keep-json=true but mu.json disappeared: %v", err)
	}

	// Assert the emitted file compiles cleanly in a fresh CUE context,
	// which is the same operation cueDecoder performs.
	cueBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	ctx := cuecontext.New()
	v := ctx.CompileBytes(cueBytes, cue.Filename(outPath))
	if err := v.Err(); err != nil {
		t.Fatalf("emitted mu.cue fails to compile: %v", err)
	}

	if !semanticEqual(t, data, cueBytes) {
		t.Errorf("repo mu.json did not parity-check against emitted mu.cue")
	}
}

// TestMigrateOne_DryRun asserts that --dry-run neither writes the output
// CUE file nor deletes the source, while still producing non-empty diff
// output on the provided writer.
func TestMigrateOne_DryRun(t *testing.T) {
	tmp := t.TempDir()
	inPath := filepath.Join(tmp, "mu.json")
	outPath := filepath.Join(tmp, "mu.cue")
	js := `{"targets":[{"target":"//x","toolchain":"go","sources":["a.go"]}]}`
	if err := os.WriteFile(inPath, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := migrateOne(inPath, outPath, true /*dryRun*/, false /*keepJSON*/, &buf); err != nil {
		t.Fatalf("migrateOne dry-run: %v", err)
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("dry-run must not write output; got err=%v", err)
	}
	if _, err := os.Stat(inPath); err != nil {
		t.Errorf("dry-run must not delete source; got err=%v", err)
	}
	diff := buf.String()
	if diff == "" {
		t.Error("dry-run produced empty diff")
	}
	if !strings.Contains(diff, "---") || !strings.Contains(diff, "+++") {
		t.Errorf("diff output missing unified diff markers: %q", diff)
	}
}

// TestMigrateOne_DeletesJSONByDefault asserts that a successful
// migration without --keep-json removes the source mu.json.
func TestMigrateOne_DeletesJSONByDefault(t *testing.T) {
	tmp := t.TempDir()
	inPath := filepath.Join(tmp, "mu.json")
	outPath := filepath.Join(tmp, "mu.cue")
	js := `{"targets":[{"target":"//x","toolchain":"go"}]}`
	if err := os.WriteFile(inPath, []byte(js), 0o644); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := migrateOne(inPath, outPath, false, false, &buf); err != nil {
		t.Fatalf("migrateOne: %v", err)
	}
	if _, err := os.Stat(inPath); !os.IsNotExist(err) {
		t.Errorf("default path must delete mu.json; got err=%v", err)
	}
	if _, err := os.Stat(outPath); err != nil {
		t.Errorf("default path must write mu.cue; got err=%v", err)
	}
}

// TestMigrateRecursive walks a small synthetic project with two mu.json
// files and asserts both get converted.
func TestMigrateRecursive(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "proj")
	sub := filepath.Join(root, "plugins", "p1")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "mu.json"),
		[]byte(`{"targets":[{"target":"//a","toolchain":"go"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "mu.json"),
		[]byte(`{"plugin":{"entrypoint":"p.bb","toolchain":"bb"},"targets":[{"target":"build","toolchain":"shell"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := migrateRecursive(root, false, true /*keepJSON*/, &buf); err != nil {
		t.Fatalf("migrateRecursive: %v", err)
	}
	for _, p := range []string{
		filepath.Join(root, "mu.cue"),
		filepath.Join(sub, "mu.cue"),
	} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("expected %s to exist: %v", p, err)
		}
	}
}
