package dag

import (
	"context"
	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/cas/oci"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPureActionsDoNotInheritAmbientValues(t *testing.T) {
	t.Setenv("MU_AMBIENT_PRIVATE", "synthetic-private-value")
	a := &Action{ID: "pure", Command: []string{"sh", "-c", `printf '%s' "${MU_AMBIENT_PRIVATE-unset}" > "$MU_OUT/out"`}, Outputs: []string{"out"}, WorkDir: t.TempDir()}
	first := ComputeActionKey(a)
	t.Setenv("MU_AMBIENT_PRIVATE", "another-private-value")
	if ComputeActionKey(a) != first {
		t.Fatal("ambient private value influenced identity")
	}
	result := executeEnvironmentAction(t, a)
	if len(result.Failed) > 0 {
		t.Fatalf("execute: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(a.WorkDir, "out"))
	if err != nil || string(data) != "unset" {
		t.Fatalf("inherited undeclared value: %q, %v", data, err)
	}
	if a.Env != nil {
		t.Fatal("runtime environment mutated declaration")
	}
}

func TestDeclaredPathSelectsExecutableAndChangesIdentity(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "mu-env-choice")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nprintf declared > \"$MU_OUT/out\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	a := &Action{ID: "path", Command: []string{"mu-env-choice"}, Env: map[string]string{"PATH": dir}, Outputs: []string{"out"}, WorkDir: t.TempDir()}
	first := ComputeActionKey(a)
	t.Setenv("PATH", "/usr/bin:/bin")
	result := executeEnvironmentAction(t, a)
	if len(result.Failed) > 0 {
		t.Fatalf("declared PATH was not used: %+v", result)
	}
	a.Env["PATH"] = "/usr/bin:/bin"
	if ComputeActionKey(a) == first {
		t.Fatal("declared PATH change did not invalidate identity")
	}
}

func TestImpureInheritanceSurvivesRuntimeOutputInjection(t *testing.T) {
	t.Setenv("MU_AMBIENT_TEST", "inherited")
	a := &Action{ID: "impure", Impure: true, Command: []string{"sh", "-c", `printf '%s' "$MU_AMBIENT_TEST" > "$MU_OUT/out"`}, Outputs: []string{"out"}, WorkDir: t.TempDir()}
	result := executeEnvironmentAction(t, a)
	if len(result.Failed) > 0 {
		t.Fatalf("execute: %+v", result)
	}
	data, err := os.ReadFile(filepath.Join(a.WorkDir, "out"))
	if err != nil || string(data) != "inherited" {
		t.Fatalf("impure inheritance: %q, %v", data, err)
	}
}

func TestOpenWorkDirConfinesSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("inside", filepath.Join(root, "internal")); err != nil {
		t.Fatal(err)
	}
	dir, err := OpenWorkDir(root, filepath.Join(root, "internal"))
	if err != nil {
		t.Fatal(err)
	}
	dir.Close()
	if err := os.Symlink(t.TempDir(), filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	if dir, err := OpenWorkDir(root, filepath.Join(root, "external")); err == nil {
		dir.Close()
		t.Fatal("allowed external symlink")
	}
}

func TestExecutorRejectsSwappedWorkDirBeforeEffects(t *testing.T) {
	root, outside := t.TempDir(), t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "inside"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "work")
	if err := os.Symlink("inside", link); err != nil {
		t.Fatal(err)
	}
	// Simulate a plan accepted while the link was still inside.
	dir, err := OpenWorkDir(root, link)
	if err != nil {
		t.Fatal(err)
	}
	dir.Close()
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	result := executeEnvironmentAction(t, &Action{ID: "escape", ProjectRoot: root, WorkDir: link, Impure: true, Command: []string{"sh", "-c", "touch escaped"}})
	if len(result.Failed) != 1 || !strings.Contains(result.Failed[0].Err.Error(), "work_dir") {
		t.Fatalf("did not reject changed link: %+v", result)
	}
	if _, err := os.Stat(filepath.Join(outside, "escaped")); !os.IsNotExist(err) {
		t.Fatalf("external effect: %v", err)
	}
}

func executeEnvironmentAction(t *testing.T, a *Action) *ExecuteResult {
	t.Helper()
	graph := NewGraph()
	if err := graph.AddAction(a); err != nil {
		t.Fatal(err)
	}
	result, err := (&Executor{Workers: 1}).Execute(context.Background(), graph)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

type workDirSwapStore struct {
	cas.Store
	swap    func()
	receipt *cas.ActionResult
}

func (s workDirSwapStore) GetActionResult(context.Context, cas.ActionKey) (*cas.ActionResult, error) {
	s.swap()
	return s.receipt, nil
}

func TestRelativeOutputsStayPinnedDuringCacheAndFreshExecution(t *testing.T) {
	for _, cached := range []bool{false, true} {
		t.Run(map[bool]string{false: "fresh", true: "cached"}[cached], func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			inside := filepath.Join(root, "inside")
			if err := os.Mkdir(inside, 0o755); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(root, "work")
			if err := os.Symlink("inside", link); err != nil {
				t.Fatal(err)
			}
			store, err := oci.NewLocal(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			digest, err := store.Put(context.Background(), strings.NewReader("pinned"))
			if err != nil {
				t.Fatal(err)
			}
			var receipt *cas.ActionResult
			if cached {
				receipt = &cas.ActionResult{Version: cas.ActionResultVersion, Outputs: map[string]cas.Digest{"out": digest}, OutputModes: map[string]uint32{"out": 0o640}}
			}
			swapStore := workDirSwapStore{Store: store, receipt: receipt, swap: func() {
				if err := os.Remove(link); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, link); err != nil {
					t.Fatal(err)
				}
			}}
			a := &Action{ID: "pinned-output", ProjectRoot: root, WorkDir: link, Outputs: []string{"out"}, Command: []string{"sh", "-c", "printf pinned > out"}}
			graph := NewGraph()
			if err := graph.AddAction(a); err != nil {
				t.Fatal(err)
			}
			result, err := (&Executor{Store: swapStore, Workers: 1}).Execute(context.Background(), graph)
			if err != nil || len(result.Failed) > 0 || len(result.Completed) != 1 {
				t.Fatalf("execute after swap: %+v, %v", result, err)
			}
			if result.Completed[0].Cached != cached {
				t.Fatal("unexpected execution path")
			}
			if data, err := os.ReadFile(filepath.Join(inside, "out")); err != nil || string(data) != "pinned" {
				t.Fatalf("output did not stay pinned: %q, %v", data, err)
			}
			if _, err := os.Stat(filepath.Join(outside, "out")); !os.IsNotExist(err) {
				t.Fatalf("output escaped: %v", err)
			}
		})
	}
}
