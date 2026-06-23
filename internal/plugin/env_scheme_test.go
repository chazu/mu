package plugin

import (
	"context"
	"testing"
)

func TestBuiltinEnvScheme(t *testing.T) {
	m := NewManager(t.TempDir())
	t.Setenv("MU_TEST_SECRET", "s3cr3t")

	// No plugin registered: the built-in "env" scheme resolves from the env.
	got, err := m.ResolveSecret(context.Background(), "env", "MU_TEST_SECRET")
	if err != nil {
		t.Fatalf("ResolveSecret env: %v", err)
	}
	if got != "s3cr3t" {
		t.Errorf("got %q, want %q", got, "s3cr3t")
	}

	// Unset var -> clear error, no value.
	if _, err := m.ResolveSecret(context.Background(), "env", "MU_TEST_UNSET_XYZ"); err == nil {
		t.Error("expected error for unset env var")
	}

	// A non-builtin, unregistered scheme still errors.
	if _, err := m.ResolveSecret(context.Background(), "pass", "x/y"); err == nil {
		t.Error("expected error for unregistered scheme")
	}
}
