package dag

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/chazu/mu/internal/cas"
)

// An impure action with no declared env inherits the host env; adding a
// sealed input must not drop that inheritance (it used to rebuild the env from
// a.Env + secrets only, losing HOME and PATH).
func TestImpureSealedInputKeepsInheritedEnv(t *testing.T) {
	t.Setenv("HOME", "/synthetic/home")
	a := &Action{
		ID:           "impure-sealed",
		Impure:       true,
		Command:      []string{"sh", "-c", `printf '%s|%s' "$HOME" "$TOK" > "$MU_OUT/out"`},
		Outputs:      []string{"out"},
		WorkDir:      t.TempDir(),
		SealedInputs: map[string]string{"TOK": "pass:deploy/token"},
	}
	graph := NewGraph()
	if err := graph.AddAction(a); err != nil {
		t.Fatal(err)
	}
	exec := &Executor{Workers: 1, ResolvedSecrets: map[string]map[string]string{a.ID: {"TOK": "s3cret"}}}
	result, err := exec.Execute(t.Context(), graph)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Failed) > 0 {
		t.Fatalf("execute: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(a.WorkDir, "out"))
	if err != nil || string(data) != "/synthetic/home|s3cret" {
		t.Fatalf("got %q, %v; want inherited HOME and the secret", data, err)
	}
	if a.Env != nil {
		t.Fatal("runtime environment mutated declaration")
	}
}

func TestEnvPassthroughReachesActionWithoutEnteringKey(t *testing.T) {
	t.Setenv("MU_PASSTHROUGH_TEST", "first")
	a := &Action{
		ID:             "pure-passthrough",
		Command:        []string{"sh", "-c", `printf '%s' "$MU_PASSTHROUGH_TEST" > "$MU_OUT/out"`},
		Outputs:        []string{"out"},
		WorkDir:        t.TempDir(),
		EnvPassthrough: []string{"MU_PASSTHROUGH_TEST"},
	}
	key := ComputeActionKey(a)

	result := executeEnvironmentAction(t, a)
	if len(result.Failed) > 0 {
		t.Fatalf("execute: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(a.WorkDir, "out"))
	if err != nil || string(data) != "first" {
		t.Fatalf("passthrough value: %q, %v", data, err)
	}

	t.Setenv("MU_PASSTHROUGH_TEST", "second")
	if ComputeActionKey(a) != key {
		t.Fatal("host value of a passthrough var changed the action key")
	}

	withExtra := *a
	withExtra.EnvPassthrough = []string{"MU_PASSTHROUGH_TEST", "HOME"}
	if ComputeActionKey(&withExtra) == key {
		t.Fatal("adding a passthrough name did not change the action key")
	}
	without := *a
	without.EnvPassthrough = nil
	if ComputeActionKey(&without) == key {
		t.Fatal("removing a passthrough name did not change the action key")
	}
	reordered := withExtra
	reordered.EnvPassthrough = []string{"HOME", "MU_PASSTHROUGH_TEST"}
	if ComputeActionKey(&reordered) != ComputeActionKey(&withExtra) {
		t.Fatal("passthrough name order changed the action key")
	}
}

func TestActionEnvLayering(t *testing.T) {
	host := []string{"HOME=/h", "PATH=/host/bin", "KEEP=host", "TOK=host-tok"}

	pure := &Action{Env: map[string]string{"A": "declared"}}
	got := actionEnv(pure, host, nil, nil)
	if len(got) != 1 || got["A"] != "declared" {
		t.Fatalf("pure action env = %v, want declared only", got)
	}
	if got := actionEnv(&Action{}, host, nil, nil); got == nil || len(got) != 0 {
		t.Fatalf("pure nil-env action = %#v, want empty non-nil", got)
	}

	impure := &Action{Impure: true, EnvPassthrough: []string{"KEEP", "ABSENT"}}
	got = actionEnv(impure, host,
		map[string]string{"MU_OUT": "/out", "TOK": "mu"},
		map[string]string{"TOK": "secret"})
	if got["HOME"] != "/h" || got["PATH"] != "/host/bin" || got["KEEP"] != "host" || got["MU_OUT"] != "/out" {
		t.Fatalf("impure inherited env = %v", got)
	}
	if got["TOK"] != "secret" {
		t.Fatalf("sealed input did not win: TOK=%q", got["TOK"])
	}
	if _, ok := got["ABSENT"]; ok {
		t.Fatal("unset passthrough var was injected")
	}

	declaredImpure := &Action{Impure: true, Env: map[string]string{"A": "1"}, EnvPassthrough: []string{"PATH"}}
	got = actionEnv(declaredImpure, host, nil, nil)
	if len(got) != 2 || got["A"] != "1" || got["PATH"] != "/host/bin" {
		t.Fatalf("declared impure env = %v, want declared + passthrough", got)
	}

	// Sandboxed execution never imports the ambient environment.
	sandboxed := &Action{Impure: true, Toolchain: map[string]cas.Digest{}}
	if got := actionEnv(sandboxed, host, nil, nil); len(got) != 0 {
		t.Fatalf("sandboxed impure action inherited host env: %v", got)
	}
}
