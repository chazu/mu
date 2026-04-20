package dag_test

import (
	"testing"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/dag"
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
