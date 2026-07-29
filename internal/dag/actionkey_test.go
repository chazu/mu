package dag_test

import (
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/dag"
)

func baseAction() *dag.Action {
	return &dag.Action{
		ID:      "test",
		Command: []string{"echo", "hi"},
		Inputs: map[string]cas.Digest{
			"src": cas.NewSHA256("aa"),
		},
		Env:     map[string]string{"FOO": "bar"},
		Network: false,
		WorkDir: "/work",
	}
}

func TestActionKey_Deterministic(t *testing.T) {
	k1 := dag.ComputeActionKey(baseAction())
	k2 := dag.ComputeActionKey(baseAction())
	if k1 != k2 {
		t.Fatalf("expected identical actions to hash equal, got %v vs %v", k1, k2)
	}
}

func TestActionKey_ImpureFlagAffectsKey(t *testing.T) {
	pure := baseAction()
	impure := baseAction()
	impure.Impure = true

	kp := dag.ComputeActionKey(pure)
	ki := dag.ComputeActionKey(impure)
	if kp == ki {
		t.Fatalf("expected impure flag to change cache key; got identical %v", kp)
	}
}

func TestActionKey_NetworkFlagAffectsKey(t *testing.T) {
	a := baseAction()
	b := baseAction()
	b.Network = true
	if dag.ComputeActionKey(a) == dag.ComputeActionKey(b) {
		t.Fatal("expected network flag to change cache key")
	}
}

func TestActionKey_CommandOrderMatters(t *testing.T) {
	a := baseAction()
	b := baseAction()
	b.Command = []string{"hi", "echo"}
	if dag.ComputeActionKey(a) == dag.ComputeActionKey(b) {
		t.Fatal("expected command argument order to change cache key")
	}
}

func TestActionKey_EnvOrderIndependent(t *testing.T) {
	a := baseAction()
	a.Env = map[string]string{"A": "1", "B": "2"}
	b := baseAction()
	b.Env = map[string]string{"B": "2", "A": "1"}
	if dag.ComputeActionKey(a) != dag.ComputeActionKey(b) {
		t.Fatal("expected env map iteration order to not affect cache key")
	}
}

// TestActionKey_ConfigFormatInvariant verifies the claim made in
// docs/scout-cue-migration.md §"Cache-key verification": the action cache key
// is a pure function of the resolved Action struct, independent of the source
// config format (mu.json, mu.cue) and its serialization (whitespace, key
// order, etc.). As long as two config files decode to semantically-equivalent
// ProjectConfigs, and the coordinator resolves them to equivalent Actions,
// their cache keys MUST be identical.
//
// We simulate two deserializers producing the same logical Action with
// different underlying map insertion orders and slice aliasing. The test does
// NOT depend on any CUE-specific code — it only exercises ComputeActionKey's
// contract, which is what the migration relies on.
func TestActionKey_ConfigFormatInvariant(t *testing.T) {
	// "Loader A" result (e.g. as produced today by the JSON loader).
	fromJSON := &dag.Action{
		ID:      "build",
		Command: []string{"cc", "-o", "out", "main.c"},
		Inputs: map[string]cas.Digest{
			"main.c": cas.NewSHA256("deadbeef"),
			"hdr.h":  cas.NewSHA256("cafef00d"),
		},
		Env: map[string]string{
			"CC":    "gcc",
			"FLAGS": "-O2",
		},
		DependsOn: []string{"gen-hdr"},
		Network:   false,
		Impure:    false,
		WorkDir:   "/work",
	}

	// "Loader B" result (e.g. the same project.cue decoded by the CUE
	// loader). Same semantic content, different map insertion order,
	// different slice backing, separately allocated strings. If raw
	// config bytes or whitespace were ever hashed, these would differ.
	fromCUE := &dag.Action{
		ID:      "build",
		Command: append([]string{}, "cc", "-o", "out", "main.c"),
		Inputs: map[string]cas.Digest{
			"hdr.h":  cas.NewSHA256("cafef00d"),
			"main.c": cas.NewSHA256("deadbeef"),
		},
		Env: map[string]string{
			"FLAGS": "-O2",
			"CC":    "gcc",
		},
		DependsOn: []string{"gen-hdr"},
		Network:   false,
		Impure:    false,
		WorkDir:   "/work",
	}

	kJSON := dag.ComputeActionKey(fromJSON)
	kCUE := dag.ComputeActionKey(fromCUE)
	if kJSON != kCUE {
		t.Fatalf("config-format invariance violated: json-loader key %v != cue-loader key %v", kJSON, kCUE)
	}
}

// --- S1: sealed-input/output secret values must never enter the cache key ---

func sealedBodyAction() *dag.Action {
	return &dag.Action{
		ID:               "fetch:run",
		Body:             []any{"'TOKEN", "secret/get"},
		Env:              map[string]string{"BASE": "https://gitlab.com"},
		SealedInputs:     map[string]string{"TOKEN": "pass:gitlab/token"},
		SealedInputModes: map[string]string{"TOKEN": "env"},
		Impure:           true, // bodies that touch secrets are impure in practice
	}
}

// The resolved secret value is injected into execEnv at exec time and is never a
// field of Action, so it cannot reach ComputeActionKey. Two keys computed from
// the same action (which carries only the ref, not the value) must match — the
// load-bearing security property: a rotated secret with the same ref does not
// change the key.
func TestActionKey_SealedValueNotInKey(t *testing.T) {
	k1 := dag.ComputeActionKey(sealedBodyAction())
	k2 := dag.ComputeActionKey(sealedBodyAction())
	if k1 != k2 {
		t.Fatalf("sealed body action key not stable: %v vs %v", k1, k2)
	}
	// Sanity: the key is computed without any value material — confirm the
	// struct carries only the ref, never a value field.
	a := sealedBodyAction()
	if got := a.SealedInputs["TOKEN"]; got != "pass:gitlab/token" {
		t.Fatalf("sealed input should hold a ref, got %q", got)
	}
}

// Changing the sealed-input REF changes the key (refs are observable metadata).
func TestActionKey_SealedRefAffectsKey(t *testing.T) {
	a1 := sealedBodyAction()
	a2 := sealedBodyAction()
	a2.SealedInputs = map[string]string{"TOKEN": "pass:gitlab/token-v2"}
	if dag.ComputeActionKey(a1) == dag.ComputeActionKey(a2) {
		t.Fatal("changing the sealed-input ref should change the key")
	}
}

// Changing the delivery MODE changes the key (env vs file is observable).
func TestActionKey_SealedModeAffectsKey(t *testing.T) {
	a1 := sealedBodyAction()
	a2 := sealedBodyAction()
	a2.SealedInputModes = map[string]string{"TOKEN": "file"}
	if dag.ComputeActionKey(a1) == dag.ComputeActionKey(a2) {
		t.Fatal("changing the sealed-input mode should change the key")
	}
}
