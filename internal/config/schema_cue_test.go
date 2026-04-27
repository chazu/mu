package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
)

// schemaSource loads internal/config/schema.cue from disk relative to
// this test file. Once the schema is embedded via go:embed, this helper
// can be replaced with that embedded value.
func schemaSource(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("schema.cue")
	if err != nil {
		t.Fatalf("read schema.cue: %v", err)
	}
	return b
}

// compileSchema returns the root schema value and its two top-level defs.
func compileSchema(t *testing.T) (schema, projectDef, pluginDef cue.Value) {
	t.Helper()
	ctx := cuecontext.New()
	schema = ctx.CompileBytes(schemaSource(t), cue.Filename("schema.cue"))
	if err := schema.Err(); err != nil {
		t.Fatalf("compile schema.cue: %v", err)
	}
	projectDef = schema.LookupPath(cue.ParsePath("#ProjectConfig"))
	if err := projectDef.Err(); err != nil {
		t.Fatalf("lookup #ProjectConfig: %v", err)
	}
	pluginDef = schema.LookupPath(cue.ParsePath("#PluginConfig"))
	if err := pluginDef.Err(); err != nil {
		t.Fatalf("lookup #PluginConfig: %v", err)
	}
	return schema, projectDef, pluginDef
}

// jsonValue decodes raw JSON bytes into a CUE value via the shared context.
func jsonValue(t *testing.T, ctx *cue.Context, data []byte) cue.Value {
	t.Helper()
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal json: %v", err)
	}
	return ctx.Encode(v)
}

// validateAgainst unifies data with def and returns the concrete validation
// error, if any.
func validateAgainst(def, data cue.Value) error {
	u := def.Unify(data)
	if err := u.Err(); err != nil {
		return err
	}
	return u.Validate(cue.Concrete(true))
}

// repoRoot returns the repository root directory by walking upwards from
// this test file until it finds go.mod alongside a mu.cue.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "mu.cue")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not find repo root from %s", dir)
		}
		dir = parent
	}
}

// cueFileValue loads a mu.cue file, evaluates it as a package (ignoring
// the package clause), and returns the resulting CUE value suitable for
// schema unification. It is the CUE-native counterpart to jsonValue.
func cueFileValue(t *testing.T, ctx *cue.Context, path string) cue.Value {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Strip the package clause so the file compiles as an anonymous
	// struct. The schema test only cares about the value, not the
	// package identity.
	src := string(data)
	if idx := strings.Index(src, "package "); idx >= 0 {
		if nl := strings.IndexByte(src[idx:], '\n'); nl >= 0 {
			src = src[:idx] + src[idx+nl+1:]
		}
	}
	v := ctx.CompileString(src, cue.Filename(filepath.Base(path)))
	if err := v.Err(); err != nil {
		t.Fatalf("compile %s: %v", path, err)
	}
	return v
}

func TestSchemaCUE_ValidProjectConfig(t *testing.T) {
	_, projectDef, _ := compileSchema(t)
	ctx := projectDef.Context()

	root := repoRoot(t)
	val := cueFileValue(t, ctx, filepath.Join(root, "mu.cue"))
	if err := validateAgainst(projectDef, val); err != nil {
		t.Fatalf("root mu.cue failed validation: %v", err)
	}
}

func TestSchemaCUE_ValidPluginConfig(t *testing.T) {
	_, _, pluginDef := compileSchema(t)
	ctx := pluginDef.Context()

	root := repoRoot(t)
	val := cueFileValue(t, ctx, filepath.Join(root, "plugins", "go", "mu.cue"))
	if err := validateAgainst(pluginDef, val); err != nil {
		t.Fatalf("plugins/go/mu.cue failed validation: %v", err)
	}
}

func TestSchemaCUE_RejectsInvalid(t *testing.T) {
	_, projectDef, _ := compileSchema(t)
	ctx := projectDef.Context()

	cases := []struct {
		name    string
		json    string
		wantSub string // substring of the error message
	}{
		{
			name: "missing toolchain",
			json: `{
				"targets": [
					{"target": "//foo", "sources": ["a.go"]}
				]
			}`,
			wantSub: "toolchain",
		},
		{
			name: "bad target name",
			json: `{
				"targets": [
					{"target": "!not-a-label!", "toolchain": "go"}
				]
			}`,
			wantSub: "target",
		},
		{
			name: "bad sha256 in toolchain",
			json: `{
				"toolchains": [
					{
						"toolchain": "bb",
						"from": "scratch",
						"config": {"version": "1", "url": "https://x", "sha256": "not-hex"}
					}
				]
			}`,
			wantSub: "sha256",
		},
		{
			name: "plugin with zero source forms",
			json: `{
				"plugins": [
					{"name": "orphan"}
				]
			}`,
			wantSub: "plugins",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			val := jsonValue(t, ctx, []byte(tc.json))
			err := validateAgainst(projectDef, val)
			if err == nil {
				t.Fatalf("expected validation error, got nil")
			}
			if tc.wantSub != "" && !strings.Contains(err.Error(), tc.wantSub) {
				t.Fatalf("error %q does not contain %q", err.Error(), tc.wantSub)
			}
		})
	}
}
