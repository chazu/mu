package coordinator_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chau/mu/internal/coordinator"
)

func TestLoadVendoredSchemas(t *testing.T) {
	dir := t.TempDir()

	muCue := `package mu

plugin: {
	entrypoint: "plugin.bb"
	toolchain:  "bb"
	files: ["plugin.bb"]
	schemas: [
		{module: "mu/aws", version: "v1", path: "schemas/mu/aws"},
	]
}
`
	if err := os.WriteFile(filepath.Join(dir, "mu.cue"), []byte(muCue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.bb"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	schemaDir := filepath.Join(dir, "schemas", "mu", "aws")
	if err := os.MkdirAll(schemaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(schemaDir, "ec2.cue"), []byte("package aws\n#EC2Instance: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(schemaDir, "vpc")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdir, "vpc.cue"), []byte("package vpc\n#VPC: {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Non-cue file should be ignored.
	if err := os.WriteFile(filepath.Join(schemaDir, "README.md"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := coordinator.LoadVendoredSchemas(dir)
	if err != nil {
		t.Fatalf("LoadVendoredSchemas: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d schemas, want 1", len(got))
	}
	s := got[0]
	if s.Module != "mu/aws" || s.Version != "v1" {
		t.Errorf("module/version = %s/%s, want mu/aws/v1", s.Module, s.Version)
	}
	if len(s.Files) != 2 {
		t.Fatalf("got %d files, want 2 (READ.md should be skipped)", len(s.Files))
	}
	if s.Files[0].RelPath != "ec2.cue" {
		t.Errorf("Files[0].RelPath = %q, want ec2.cue", s.Files[0].RelPath)
	}
	if s.Files[1].RelPath != "vpc/vpc.cue" {
		t.Errorf("Files[1].RelPath = %q, want vpc/vpc.cue", s.Files[1].RelPath)
	}
}

func TestLoadVendoredSchemasNoneDeclared(t *testing.T) {
	dir := t.TempDir()
	muCue := `package mu

plugin: {
	entrypoint: "plugin.bb"
	files: ["plugin.bb"]
}
`
	if err := os.WriteFile(filepath.Join(dir, "mu.cue"), []byte(muCue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.bb"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := coordinator.LoadVendoredSchemas(dir)
	if err != nil {
		t.Fatalf("LoadVendoredSchemas: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("got %d schemas, want 0", len(got))
	}
}

func TestLoadVendoredSchemasMissingDir(t *testing.T) {
	dir := t.TempDir()
	muCue := `package mu

plugin: {
	entrypoint: "plugin.bb"
	files: ["plugin.bb"]
	schemas: [
		{module: "mu/aws", version: "v1", path: "schemas/missing"},
	]
}
`
	if err := os.WriteFile(filepath.Join(dir, "mu.cue"), []byte(muCue), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plugin.bb"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.LoadVendoredSchemas(dir); err == nil {
		t.Fatal("expected error for missing schema dir")
	}
}
