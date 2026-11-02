package coordinator

import (
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
)

// realSchema is a JSON Schema config_schema as bundled plugins report it.
func realSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"namespace":   map[string]any{"type": "string"},
			"server_side": map[string]any{"type": "boolean", "default": true},
			"optimize":    map[string]any{"type": "string", "enum": []any{"Debug", "ReleaseSafe", "ReleaseFast"}},
			"count":       map[string]any{"type": "integer"},
			"tags":        map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			"semver":      map[string]any{"type": "string", "pattern": `^\d+\.\d+\.\d+$`},
		},
		"required":             []any{"namespace"},
		"additionalProperties": false,
	}
}

func validate(t *testing.T, schema map[string]any, cfg map[string]any) error {
	t.Helper()
	return ValidateTargetConfig(config.Target{Name: "//app", Toolchain: t.Name(), Config: cfg}, schema)
}

func wantErr(t *testing.T, err error, parts ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected an error containing %q", parts)
	}
	for _, p := range parts {
		if !strings.Contains(err.Error(), p) {
			t.Fatalf("error %q does not contain %q", err, p)
		}
	}
}

func TestValidateJSONSchema_Valid(t *testing.T) {
	buf := captureMissingSchemaWarnings(t)
	err := validate(t, realSchema(), map[string]any{
		"namespace": "myapp", "server_side": true, "optimize": "Debug",
		"count": 5, "tags": []any{"a"}, "semver": "1.2.3",
	})
	if err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	if buf.Len() != 0 {
		t.Fatalf("a JSON Schema should not warn, got %q", buf.String())
	}
}

func TestValidateJSONSchema_MissingRequired(t *testing.T) {
	wantErr(t, validate(t, realSchema(), nil), `target "//app"`, "missing properties", "namespace")
}

func TestValidateJSONSchema_UnknownFieldRejected(t *testing.T) {
	err := validate(t, realSchema(), map[string]any{"namespace": "x", "namespcae": "y"})
	wantErr(t, err, "additionalProperties", "namespcae", "not allowed")
}

func TestValidateJSONSchema_Violations(t *testing.T) {
	cases := []struct {
		name  string
		field string
		value any
		want  string
	}{
		{"wrong type", "server_side", "yes", "config/server_side: expected boolean, but got string"},
		{"enum", "optimize", "Turbo", "config/optimize: value must be one of"},
		{"non-integer", "count", 5.5, "config/count"},
		{"array items", "tags", []any{1}, "config/tags/0"},
		{"pattern", "semver", "nope", "config/semver"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			wantErr(t, validate(t, realSchema(), map[string]any{"namespace": "x", tc.field: tc.value}), tc.want)
		})
	}
}

func TestValidateJSONSchema_ExplicitDraft(t *testing.T) {
	schema := realSchema()
	schema["$schema"] = "https://json-schema.org/draft/2020-12/schema"
	wantErr(t, validate(t, schema, map[string]any{}), "missing properties")
}

func TestValidateJSONSchema_InvalidSchema(t *testing.T) {
	schema := map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"type": 7}}}
	wantErr(t, validate(t, schema, map[string]any{"x": 1}), "config_schema")
}

func TestValidateEmptySchema(t *testing.T) {
	for _, schema := range []map[string]any{nil, {}} {
		if err := validate(t, schema, map[string]any{"anything": "goes"}); err != nil {
			t.Fatalf("expected no error with empty schema, got: %v", err)
		}
	}
}

func TestIsJSONSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema map[string]any
		want   bool
	}{
		{"type object", map[string]any{"type": "object"}, true},
		{"$schema only", map[string]any{"$schema": "http://json-schema.org/draft-07/schema#"}, true},
		{"properties only", map[string]any{"properties": map[string]any{"a": map[string]any{"type": "string"}}}, true},
		{"properties with a field named type", map[string]any{"properties": map[string]any{"type": map[string]any{"type": "string"}}}, true},
		{"legacy", map[string]any{"a": map[string]any{"type": "string"}}, false},
		{"legacy field named type", map[string]any{"type": map[string]any{"type": "string"}}, false},
		{"legacy field named properties", map[string]any{"properties": map[string]any{"type": "object"}}, false},
	}
	for _, tc := range cases {
		if got := isJSONSchema(tc.schema); got != tc.want {
			t.Errorf("%s: isJSONSchema = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The deprecated field-map dialect still validates for one release, with
// a once-per-plugin deprecation warning and unknown fields only warned.
func TestValidateLegacySchema(t *testing.T) {
	buf := captureMissingSchemaWarnings(t)
	schema := map[string]any{
		"namespace": map[string]any{"type": "string", "required": true},
		"optimize":  map[string]any{"type": "string", "default": "Debug", "enum": []any{"Debug", "ReleaseFast"}},
		"tags":      map[string]any{"type": "array", "items": "string"},
	}

	if err := validate(t, schema, map[string]any{"namespace": "x", "tags": []any{"a"}, "extra": 1}); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	wantErr(t, validate(t, schema, map[string]any{}), "missing properties", "namespace")
	wantErr(t, validate(t, schema, map[string]any{"namespace": "x", "optimize": "Turbo"}), "config/optimize")
	wantErr(t, validate(t, schema, map[string]any{"namespace": "x", "tags": []any{1}}), "config/tags/0")

	out := buf.String()
	if n := strings.Count(out, "legacy field-map config_schema"); n != 1 {
		t.Fatalf("got %d deprecation warnings, want 1:\n%s", n, out)
	}
	if !strings.Contains(out, `plugin "`+t.Name()+`"`) {
		t.Fatalf("deprecation warning does not name the plugin: %q", out)
	}
	if !strings.Contains(out, `unknown config field "extra"`) {
		t.Fatalf("expected unknown-field warning, got %q", out)
	}
}

// A legacy schema with a field literally named "type" (keypair-gen's old
// schema) is still read as a field map.
func TestValidateLegacySchemaFieldNamedType(t *testing.T) {
	captureMissingSchemaWarnings(t)
	schema := map[string]any{"type": map[string]any{"type": "string", "enum": []any{"ed25519", "ecdsa"}}}
	if err := validate(t, schema, map[string]any{"type": "ed25519"}); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
	wantErr(t, validate(t, schema, map[string]any{"type": "rsa"}), "config/type")
}
