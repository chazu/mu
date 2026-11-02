package dag_test

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/dag"
	"github.com/chazu/mu/internal/sandbox"
)

// requireHostShellSandbox skips when the sandbox cannot run /bin/sh: the Linux
// namespace rootfs holds only the sandbox tree until system paths are mounted
// read-only (mu-oa1.2).
func requireHostShellSandbox(t *testing.T) sandbox.IsolationLevel {
	t.Helper()
	sb, err := sandbox.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	level := sb.Level()
	sb.Cleanup()
	if level == sandbox.IsolationNamespace {
		t.Skip("namespace sandbox has no /bin/sh until system paths are mounted (mu-oa1.2)")
	}
	return level
}

func writeProjectFile(t *testing.T, root, rel, content string) cas.Digest {
	t.Helper()
	path := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := cas.ComputeDigest(strings.NewReader(content))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func runGraph(t *testing.T, e *dag.Executor, actions ...*dag.Action) *dag.ExecuteResult {
	t.Helper()
	g := dag.NewGraph()
	for _, a := range actions {
		if err := g.AddAction(a); err != nil {
			t.Fatal(err)
		}
	}
	result, err := e.Execute(context.Background(), g)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func requireSucceeded(t *testing.T, result *dag.ExecuteResult) {
	t.Helper()
	if len(result.Failed) != 0 || len(result.Cancelled) != 0 {
		for _, f := range result.Failed {
			t.Errorf("action %s failed: %v", f.ID, f.Err)
		}
		t.Fatalf("cancelled: %v", result.Cancelled)
	}
}

func TestSandboxGate(t *testing.T) {
	pure := &dag.Action{}
	impure := &dag.Action{Impure: true}
	tool := &dag.Action{Toolchain: map[string]cas.Digest{}}
	t.Setenv(dag.SandboxEnvVar, "")
	if pure.Sandboxed() || impure.Sandboxed() || !tool.Sandboxed() {
		t.Fatal("gate off: only toolchain actions are sandboxed")
	}
	off := dag.ComputeActionKey(pure)
	t.Setenv(dag.SandboxEnvVar, "1")
	if !pure.Sandboxed() || impure.Sandboxed() {
		t.Fatal("gate on: every pure action, and no impure action, is sandboxed")
	}
	if dag.ComputeActionKey(pure) == off {
		t.Fatal("sandboxed and bare execution share an action key")
	}
}

// A pure action sees exactly its declared inputs, at their project paths,
// and its outputs come back to the project.
func TestSandboxedActionInputsAndOutputs(t *testing.T) {
	level := requireHostShellSandbox(t)
	t.Setenv(dag.SandboxEnvVar, "1")
	project := t.TempDir()
	inDigest := writeProjectFile(t, project, "pkg/in.txt", "declared\n")
	writeProjectFile(t, project, "pkg/secret.txt", "undeclared\n")
	writeProjectFile(t, project, "root.txt", "also undeclared\n")

	script := `cat in.txt > out.txt
test ! -e secret.txt || { echo "undeclared sibling visible" >&2; exit 3; }
test ! -e ../root.txt || { echo "undeclared root file visible" >&2; exit 4; }
`
	if level == sandbox.IsolationSeatbelt {
		// The kernel also denies reading the host project by absolute path.
		script += `if cat "$PROJECT/pkg/secret.txt" >/dev/null 2>&1; then echo "host project readable" >&2; exit 5; fi
`
	}
	a := &dag.Action{
		ID:          "read",
		Command:     []string{"sh", "-c", script},
		Inputs:      map[string]cas.Digest{"in": inDigest},
		InputPaths:  map[string]string{"in": "pkg/in.txt"},
		Outputs:     []string{"out.txt"},
		Env:         map[string]string{"PROJECT": project},
		ProjectRoot: project,
		WorkDir:     filepath.Join(project, "pkg"),
	}
	requireSucceeded(t, runGraph(t, &dag.Executor{Store: newStore(t), Workers: 1}, a))
	got, err := os.ReadFile(filepath.Join(project, "pkg", "out.txt"))
	if err != nil || string(got) != "declared\n" {
		t.Fatalf("output = %q, %v", got, err)
	}
}

// With the gate off the same action runs on the host and sees the project.
func TestUnsandboxedActionSeesProject(t *testing.T) {
	t.Setenv(dag.SandboxEnvVar, "")
	project := t.TempDir()
	writeProjectFile(t, project, "secret.txt", "undeclared\n")
	a := &dag.Action{
		ID: "read", Command: []string{"sh", "-c", "cat secret.txt > out.txt"},
		Outputs: []string{"out.txt"}, Env: map[string]string{}, ProjectRoot: project, WorkDir: project,
	}
	requireSucceeded(t, runGraph(t, &dag.Executor{Store: newStore(t), Workers: 1}, a))
}

func TestSandboxedActionChangedInputFails(t *testing.T) {
	requireHostShellSandbox(t)
	t.Setenv(dag.SandboxEnvVar, "1")
	project := t.TempDir()
	planned := writeProjectFile(t, project, "in.txt", "planned")
	writeProjectFile(t, project, "in.txt", "edited after planning")
	a := &dag.Action{
		ID: "read", Command: []string{"sh", "-c", "cat in.txt > out.txt"},
		Inputs: map[string]cas.Digest{"in": planned}, InputPaths: map[string]string{"in": "in.txt"},
		Outputs: []string{"out.txt"}, Env: map[string]string{}, ProjectRoot: project, WorkDir: project,
	}
	result := runGraph(t, &dag.Executor{Store: newStore(t), Workers: 1}, a)
	if len(result.Failed) != 1 || !strings.Contains(result.Failed[0].Err.Error(), "does not match planned digest") {
		t.Fatalf("want digest mismatch failure, got %+v", result)
	}
}

func TestSandboxedOutputEscapeIsError(t *testing.T) {
	t.Setenv(dag.SandboxEnvVar, "1")
	project := t.TempDir()
	for _, out := range []string{"../escape.txt", filepath.Join(project, "abs.txt")} {
		a := &dag.Action{
			ID: "escape", Command: []string{"true"}, Outputs: []string{out},
			Env: map[string]string{}, ProjectRoot: project, WorkDir: project,
		}
		result := runGraph(t, &dag.Executor{Store: newStore(t), Workers: 1}, a)
		if len(result.Failed) != 1 || !strings.Contains(result.Failed[0].Err.Error(), "escapes its work directory") {
			t.Fatalf("output %q: want escape error, got %+v", out, result)
		}
	}
}

// Upstream outputs are materialised in the consumer's sandbox, both when the
// consumer names one output by path and when it references the whole producer.
func TestSandboxedActionUpstreamOutputs(t *testing.T) {
	requireHostShellSandbox(t)
	t.Setenv(dag.SandboxEnvVar, "1")
	project := t.TempDir()
	if err := os.Mkdir(filepath.Join(project, "gen"), 0o755); err != nil {
		t.Fatal(err)
	}
	producer := &dag.Action{
		ID:      "gen",
		Command: []string{"sh", "-c", `printf generated > "$MU_OUT/gen.txt" && printf '#!/bin/sh\necho tool\n' > tool.sh && chmod +x tool.sh`},
		Outputs: []string{"gen.txt", "tool.sh"}, Env: map[string]string{},
		ProjectRoot: project, WorkDir: filepath.Join(project, "gen"),
	}
	byPath := &dag.Action{
		ID:           "by-path",
		Command:      []string{"sh", "-c", "cat gen/gen.txt > copy.txt"},
		Inputs:       map[string]cas.Digest{"gen": {}},
		InputSources: map[string]dag.InputSource{"gen": {ProducerID: "gen", OutputPath: "gen/gen.txt"}},
		InputPaths:   map[string]string{"gen": "gen/gen.txt"},
		Outputs:      []string{"copy.txt"}, Env: map[string]string{}, DependsOn: []string{"gen"},
		ProjectRoot: project, WorkDir: project,
	}
	whole := &dag.Action{
		ID:           "whole",
		Command:      []string{"sh", "-c", "./gen/tool.sh > ran.txt && cat gen/gen.txt >> ran.txt"},
		Inputs:       map[string]cas.Digest{"gen": {}},
		InputSources: map[string]dag.InputSource{"gen": {ProducerID: "gen"}},
		Outputs:      []string{"ran.txt"}, Env: map[string]string{}, DependsOn: []string{"gen"},
		ProjectRoot: project, WorkDir: project,
	}
	requireSucceeded(t, runGraph(t, &dag.Executor{Store: newStore(t), Workers: 2}, producer, byPath, whole))
	if got, _ := os.ReadFile(filepath.Join(project, "copy.txt")); string(got) != "generated" {
		t.Fatalf("copy.txt = %q", got)
	}
	if got, _ := os.ReadFile(filepath.Join(project, "ran.txt")); string(got) != "tool\ngenerated" {
		t.Fatalf("ran.txt = %q", got)
	}
}

// A pure action naming a scratch toolchain finds its binary under the sandbox
// root on PATH. Toolchains sandbox the action even with MU_SANDBOX unset.
func TestSandboxedToolchainOnPath(t *testing.T) {
	requireHostShellSandbox(t)
	t.Setenv(dag.SandboxEnvVar, "")
	store := newStore(t)
	tool, err := store.Put(context.Background(), bytes.NewReader([]byte("#!/bin/sh\necho \"mytool $0\" > \"$1\"\n")))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	a := &dag.Action{
		ID: "tool", Command: []string{"mytool", "out.txt"},
		Toolchain: map[string]cas.Digest{"bin/mytool": tool},
		Outputs:   []string{"out.txt"}, Env: map[string]string{}, ProjectRoot: project, WorkDir: project,
	}
	requireSucceeded(t, runGraph(t, &dag.Executor{Store: store, Workers: 1}, a))
	got, err := os.ReadFile(filepath.Join(project, "out.txt"))
	if err != nil {
		t.Fatal(err)
	}
	ran := strings.TrimSpace(strings.TrimPrefix(string(got), "mytool "))
	if !strings.HasSuffix(ran, filepath.Join("bin", "mytool")) || strings.HasPrefix(ran, project) {
		t.Fatalf("tool did not run from the sandbox bin/: %q", got)
	}
}

// File-mode sealed inputs are readable inside the sandbox and gone afterwards.
func TestSandboxedSealedInputFileMode(t *testing.T) {
	requireHostShellSandbox(t)
	t.Setenv(dag.SandboxEnvVar, "1")
	project := t.TempDir()
	a := &dag.Action{
		ID:               "sealed",
		Command:          []string{"sh", "-c", `cat "$TOKEN" > value.txt && printf %s "$TOKEN" > path.txt`},
		SealedInputs:     map[string]string{"TOKEN": "fake:token"},
		SealedInputModes: map[string]string{"TOKEN": "file"},
		Outputs:          []string{"value.txt", "path.txt"}, Env: map[string]string{},
		ProjectRoot: project, WorkDir: project,
	}
	e := &dag.Executor{
		Store: newStore(t), Workers: 1,
		ResolvedSecrets: map[string]map[string]string{"sealed": {"TOKEN": "s3cret"}},
	}
	requireSucceeded(t, runGraph(t, e, a))
	if got, _ := os.ReadFile(filepath.Join(project, "value.txt")); string(got) != "s3cret" {
		t.Fatalf("sealed value = %q", got)
	}
	path, _ := os.ReadFile(filepath.Join(project, "path.txt"))
	if len(path) == 0 {
		t.Fatal("no sealed path recorded")
	}
	if _, err := os.Stat(string(path)); !os.IsNotExist(err) {
		t.Fatalf("sealed file %s still exists after execution: %v", path, err)
	}
}
