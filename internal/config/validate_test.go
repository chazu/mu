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
		Services: []Service{
			{Name: "db", Runtime: "docker", Config: ServiceConfig{Image: "postgres:16"}},
			{Name: "api", Runtime: "host"},
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

func TestValidate_InvalidServiceRuntime(t *testing.T) {
	cfg := &ProjectConfig{
		Services: []Service{
			{Name: "db", Runtime: "podman"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for invalid service runtime")
	}
	if !strings.Contains(err.Error(), "runtime must be") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_DuplicateServiceNames(t *testing.T) {
	cfg := &ProjectConfig{
		Services: []Service{
			{Name: "db", Runtime: "docker"},
			{Name: "db", Runtime: "docker"},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected error for duplicate service names")
	}
	if !strings.Contains(err.Error(), "duplicate service name") {
		t.Fatalf("unexpected error message: %v", err)
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := &ProjectConfig{
		Targets: []Target{
			{Name: "", Toolchain: ""},          // two errors: missing name and toolchain
			{Name: "bad-name", Toolchain: "go"}, // one error: no // prefix
		},
		Services: []Service{
			{Name: "db", Runtime: "lxc"}, // invalid runtime
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected multiple errors")
	}
	msg := err.Error()
	// Should report at least 4 issues.
	count := strings.Count(msg, "\n")
	if count < 4 {
		t.Fatalf("expected at least 4 error lines, got %d in:\n%s", count, msg)
	}
}
