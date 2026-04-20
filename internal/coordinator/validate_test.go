package coordinator

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/chau/mu/internal/config"
)

func TestValidateTargetConfig_Valid(t *testing.T) {
	target := config.Target{
		Name: "//app",
		Config: map[string]any{
			"namespace":   "myapp",
			"server_side": true,
			"optimize":    "Debug",
		},
	}
	schema := map[string]any{
		"namespace":   map[string]any{"type": "string"},
		"server_side": map[string]any{"type": "boolean", "default": true},
		"optimize":    map[string]any{"type": "string", "default": "Debug", "enum": []any{"Debug", "ReleaseSafe", "ReleaseFast"}},
	}

	if err := ValidateTargetConfig(target, schema); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}
}

func TestValidateTargetConfig_MissingRequired(t *testing.T) {
	target := config.Target{
		Name:   "//app",
		Config: map[string]any{},
	}
	schema := map[string]any{
		"namespace": map[string]any{"type": "string", "required": true},
	}

	err := ValidateTargetConfig(target, schema)
	if err == nil {
		t.Fatal("expected error for missing required field")
	}
	if !strings.Contains(err.Error(), "missing required") {
		t.Fatalf("expected 'missing required' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "namespace") {
		t.Fatalf("expected field name in error, got: %v", err)
	}
}

func TestValidateTargetConfig_WrongType(t *testing.T) {
	target := config.Target{
		Name: "//app",
		Config: map[string]any{
			"server_side": "yes", // should be boolean
		},
	}
	schema := map[string]any{
		"server_side": map[string]any{"type": "boolean", "default": true},
	}

	err := ValidateTargetConfig(target, schema)
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
	if !strings.Contains(err.Error(), "must be a boolean") {
		t.Fatalf("expected type error, got: %v", err)
	}
}

func TestValidateTargetConfig_InvalidEnum(t *testing.T) {
	target := config.Target{
		Name: "//app",
		Config: map[string]any{
			"optimize": "Turbo",
		},
	}
	schema := map[string]any{
		"optimize": map[string]any{"type": "string", "default": "Debug", "enum": []any{"Debug", "ReleaseSafe", "ReleaseFast"}},
	}

	err := ValidateTargetConfig(target, schema)
	if err == nil {
		t.Fatal("expected error for invalid enum value")
	}
	if !strings.Contains(err.Error(), "must be one of") {
		t.Fatalf("expected enum error, got: %v", err)
	}
}

func TestValidateTargetConfig_UnknownField(t *testing.T) {
	// Capture stderr to check for warning.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	target := config.Target{
		Name: "//app",
		Config: map[string]any{
			"namespace": "myapp",
			"unknown":   "value",
		},
	}
	schema := map[string]any{
		"namespace": map[string]any{"type": "string"},
	}

	err := ValidateTargetConfig(target, schema)

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stderr = oldStderr

	if err != nil {
		t.Fatalf("expected no error for unknown field, got: %v", err)
	}
	if !strings.Contains(buf.String(), "unknown config field") {
		t.Fatalf("expected warning on stderr, got: %q", buf.String())
	}
}

func TestValidateTargetConfig_EmptySchema(t *testing.T) {
	target := config.Target{
		Name: "//app",
		Config: map[string]any{
			"anything": "goes",
		},
	}

	if err := ValidateTargetConfig(target, map[string]any{}); err != nil {
		t.Fatalf("expected no error with empty schema, got: %v", err)
	}
	if err := ValidateTargetConfig(target, nil); err != nil {
		t.Fatalf("expected no error with nil schema, got: %v", err)
	}
}

func TestValidateTargetConfig_NilConfig_AllDefaults(t *testing.T) {
	target := config.Target{
		Name:   "//app",
		Config: nil,
	}
	schema := map[string]any{
		"optimize": map[string]any{"type": "string", "default": "Debug"},
	}

	if err := ValidateTargetConfig(target, schema); err != nil {
		t.Fatalf("expected no error when all fields have defaults, got: %v", err)
	}
}

func TestValidateTargetConfig_NilConfig_RequiredField(t *testing.T) {
	target := config.Target{
		Name:   "//app",
		Config: nil,
	}
	schema := map[string]any{
		"namespace": map[string]any{"type": "string", "required": true},
	}

	err := ValidateTargetConfig(target, schema)
	if err == nil {
		t.Fatal("expected error for missing required field with nil config")
	}
}

func TestValidateTargetConfig_ExtraFields(t *testing.T) {
	// Extra fields should only produce a warning, not an error.
	oldStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	target := config.Target{
		Name: "//app",
		Config: map[string]any{
			"namespace": "myapp",
			"extra1":    "val1",
			"extra2":    float64(42),
		},
	}
	schema := map[string]any{
		"namespace": map[string]any{"type": "string"},
	}

	err := ValidateTargetConfig(target, schema)

	w.Close()
	var buf bytes.Buffer
	buf.ReadFrom(r)
	os.Stderr = oldStderr

	if err != nil {
		t.Fatalf("expected no error for extra fields, got: %v", err)
	}
	warnings := buf.String()
	if !strings.Contains(warnings, "extra1") || !strings.Contains(warnings, "extra2") {
		t.Fatalf("expected warnings for both extra fields, got: %q", warnings)
	}
}

func TestValidateTargetConfig_IntegerType(t *testing.T) {
	// Valid integer.
	target := config.Target{
		Name:   "//app",
		Config: map[string]any{"count": float64(5)},
	}
	schema := map[string]any{
		"count": map[string]any{"type": "integer"},
	}
	if err := ValidateTargetConfig(target, schema); err != nil {
		t.Fatalf("expected no error for valid integer, got: %v", err)
	}

	// Float (not whole number) should fail.
	target.Config["count"] = float64(5.5)
	err := ValidateTargetConfig(target, schema)
	if err == nil {
		t.Fatal("expected error for non-integer float")
	}

	// String should fail.
	target.Config["count"] = "five"
	err = ValidateTargetConfig(target, schema)
	if err == nil {
		t.Fatal("expected error for string as integer")
	}
}

func TestValidateTargetConfig_ArrayType(t *testing.T) {
	target := config.Target{
		Name:   "//app",
		Config: map[string]any{"tags": []any{"a", "b"}},
	}
	schema := map[string]any{
		"tags": map[string]any{"type": "array"},
	}
	if err := ValidateTargetConfig(target, schema); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	target.Config["tags"] = "not-an-array"
	if err := ValidateTargetConfig(target, schema); err == nil {
		t.Fatal("expected error for non-array")
	}
}

func TestValidateTargetConfig_EnumSuggestsDidYouMean(t *testing.T) {
	target := config.Target{
		Name:   "//app",
		Config: map[string]any{"goos": "linx"},
	}
	schema := map[string]any{
		"goos": map[string]any{"type": "string", "enum": []any{"linux", "darwin", "windows"}},
	}
	err := ValidateTargetConfig(target, schema)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "did you mean") || !strings.Contains(err.Error(), "linux") {
		t.Fatalf("expected suggestion for typo, got: %v", err)
	}
}

func TestValidateTargetConfig_PatternEnforced(t *testing.T) {
	target := config.Target{
		Name:   "//app",
		Config: map[string]any{"semver": "not-a-version"},
	}
	schema := map[string]any{
		"semver": map[string]any{"type": "string", "pattern": `^\d+\.\d+\.\d+$`},
	}
	if err := ValidateTargetConfig(target, schema); err == nil {
		t.Fatal("expected error for pattern mismatch (new in jsonschema validator)")
	}
}

func TestValidateTargetConfig_ObjectType(t *testing.T) {
	target := config.Target{
		Name:   "//app",
		Config: map[string]any{"meta": map[string]any{"key": "val"}},
	}
	schema := map[string]any{
		"meta": map[string]any{"type": "object"},
	}
	if err := ValidateTargetConfig(target, schema); err != nil {
		t.Fatalf("expected no error, got: %v", err)
	}

	target.Config["meta"] = "not-an-object"
	if err := ValidateTargetConfig(target, schema); err == nil {
		t.Fatal("expected error for non-object")
	}
}
