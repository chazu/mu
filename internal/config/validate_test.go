package config

import (
	"strings"
	"testing"
)

func TestValidate_ValidConfig(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"*.go"}},
			{Name: "//lib", Toolchain: "go", Sources: []string{"*.go"}},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid config, got error: %v", err)
	}
}

func TestValidate_EmptyConfig(t *testing.T) {
	cfg := &ProjectConfig{}
	if err := Validate(cfg); err != nil {
		t.Fatalf("empty config should be valid, got: %v", err)
	}
}

func TestValidate_MissingTargetName(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "", Toolchain: "go", Sources: []string{"*.go"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing target name")
	}
	if !strings.Contains(err.Error(), "missing required field \"target\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MissingToolchain(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "", Sources: []string{"*.go"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing toolchain")
	}
	if !strings.Contains(err.Error(), "missing required field \"toolchain\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_TargetNameMustStartWithDoubleSlash(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "app", Toolchain: "go", Sources: []string{"*.go"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for target name without // prefix")
	}
	if !strings.Contains(err.Error(), "must start with \"//\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_DuplicateTargetNames(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"*.go"}},
			{Name: "//app", Toolchain: "rust", Sources: []string{"*.rs"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate target names")
	}
	if !strings.Contains(err.Error(), "duplicate target name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "", Toolchain: ""},          // two errors: missing name and toolchain
			{Name: "bad-name", Toolchain: "go"}, // one error: no // prefix
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected multiple errors")
	}
	msg := err.Error()
	// Should report at least 3 issues.
	count := strings.Count(msg, "\n")
	if count < 3 {
		t.Fatalf("expected at least 3 error lines, got %d in:\n%s", count, msg)
	}
}

// --- Toolchain validation ---

func TestValidate_MissingToolchainName(t *testing.T) {
	cfg := &ProjectConfig{
		Toolchains: []Toolchain{
			{Name: "", From: "scratch"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing toolchain name")
	}
	if !strings.Contains(err.Error(), "missing required field \"toolchain\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MissingToolchainFrom(t *testing.T) {
	cfg := &ProjectConfig{
		Toolchains: []Toolchain{
			{Name: "go", From: ""},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing toolchain from")
	}
	if !strings.Contains(err.Error(), "missing required field \"from\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_DuplicateToolchainNames(t *testing.T) {
	cfg := &ProjectConfig{
		Toolchains: []Toolchain{
			{Name: "go", From: "scratch"},
			{Name: "go", From: "scratch"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate toolchain names")
	}
	if !strings.Contains(err.Error(), "duplicate toolchain name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// --- Plugin validation ---

func TestValidate_MissingPluginName(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "", Command: []string{"./plugin"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing plugin name")
	}
	if !strings.Contains(err.Error(), "missing required field \"name\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MissingPluginCommandAndScript(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing command and script")
	}
	if !strings.Contains(err.Error(), "must set either \"command\" or \"script\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_PluginCommandAndScriptConflict(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin", Command: []string{"./a"}, Script: "plugin.bb"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for both command and script")
	}
	if !strings.Contains(err.Error(), "cannot set both") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_PluginScriptOnly(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin", Script: "plugins/go/plugin.bb"},
		},
	}
	err := Validate(cfg)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestValidate_DuplicatePluginNames(t *testing.T) {
	cfg := &ProjectConfig{
		Plugins: []PluginDef{
			{Name: "my-plugin", Command: []string{"./a"}},
			{Name: "my-plugin", Command: []string{"./b"}},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate plugin names")
	}
	if !strings.Contains(err.Error(), "duplicate plugin name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

// --- Cache validation ---

func TestValidate_InvalidCacheBackendType(t *testing.T) {
	cfg := &ProjectConfig{
		Cache: &CacheConfig{
			Backends: []CacheBackend{
				{Type: "s3", Path: "/tmp/cache"},
			},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid cache backend type")
	}
	if !strings.Contains(err.Error(), "type must be \"disk\" or \"oci\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_DiskBackendMissingPath(t *testing.T) {
	cfg := &ProjectConfig{
		Cache: &CacheConfig{
			Backends: []CacheBackend{
				{Type: "disk", Path: ""},
			},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for disk backend missing path")
	}
	if !strings.Contains(err.Error(), "disk backend requires \"path\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_OCIBackendMissingRegistry(t *testing.T) {
	cfg := &ProjectConfig{
		Cache: &CacheConfig{
			Backends: []CacheBackend{
				{Type: "oci", Registry: ""},
			},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for oci backend missing registry")
	}
	if !strings.Contains(err.Error(), "oci backend requires \"registry\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_ValidCacheBackends(t *testing.T) {
	cfg := &ProjectConfig{
		Cache: &CacheConfig{
			Backends: []CacheBackend{
				{Type: "disk", Path: "/tmp/cache"},
				{Type: "oci", Registry: "ghcr.io/example"},
			},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid cache config, got: %v", err)
	}
}

// --- Preprocessor validation ---

func TestValidate_MissingPreprocessorExtension(t *testing.T) {
	cfg := &ProjectConfig{
		Preprocessor: &Preprocessor{
			Extension: "",
			Command:   []string{"jsonnet"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing preprocessor extension")
	}
	if !strings.Contains(err.Error(), "missing required field \"extension\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MissingPreprocessorCommand(t *testing.T) {
	cfg := &ProjectConfig{
		Preprocessor: &Preprocessor{
			Extension: ".jsonnet",
			Command:   nil,
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for missing preprocessor command")
	}
	if !strings.Contains(err.Error(), "missing required field \"command\"") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_ValidPreprocessor(t *testing.T) {
	cfg := &ProjectConfig{
		Preprocessor: &Preprocessor{
			Extension: ".jsonnet",
			Command:   []string{"jsonnet"},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid preprocessor config, got: %v", err)
	}
}

func TestValidate_FullValidConfig(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "//app", Toolchain: "go", Sources: []string{"*.go"}},
		},
		Toolchains: []Toolchain{
			{Name: "go", From: "scratch"},
		},
		Plugins: []PluginDef{
			{Name: "go-plugin", Command: []string{"./plugins/go"}},
		},
		Cache: &CacheConfig{
			Backends: []CacheBackend{
				{Type: "disk", Path: "/tmp/cache"},
			},
		},
		Preprocessor: &Preprocessor{
			Extension: ".jsonnet",
			Command:   []string{"jsonnet"},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected full valid config to pass, got: %v", err)
	}
}
