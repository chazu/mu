package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/plugin"
)

func TestResolveConfinesSymlinkReads(t *testing.T) {
	for _, outside := range []bool{false, true} {
		name := "input inside"
		if outside {
			name = "input outside"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			target := filepath.Join(root, "actual")
			if outside {
				target = filepath.Join(t.TempDir(), "actual")
			}
			if err := os.WriteFile(target, []byte("input"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, filepath.Join(root, "link")); err != nil {
				t.Fatal(err)
			}
			// Root follows relative symlinks that stay inside the tree.
			if !outside {
				if err := os.Remove(filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("actual", filepath.Join(root, "link")); err != nil {
					t.Fatal(err)
				}
			}
			spec := plugin.ActionSpec{ID: "test", Inputs: map[string]string{"src": "link"}}
			_, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, root, newTestStore(t), nil, nil)
			if outside && err == nil {
				t.Fatal("read escaped project through symlink")
			}
			if !outside && err != nil {
				t.Fatalf("in-project link rejected: %v", err)
			}
			if err != nil && !strings.Contains(err.Error(), "test") {
				t.Fatalf("missing action context: %v", err)
			}
		})
	}
}

func TestResolveFileInputs(t *testing.T) {
	dir := t.TempDir()

	content := []byte("hello world\n")
	if err := os.WriteFile(filepath.Join(dir, "input.txt"), content, 0o644); err != nil {
		t.Fatal(err)
	}

	specs := []plugin.ActionSpec{
		{
			ID:      "build",
			Command: []string{"cat", "input.txt"},
			Inputs:  map[string]string{"src": "input.txt"},
			Outputs: []string{"out.txt"},
		},
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}

	a := actions[0]
	if a.ID != "build" {
		t.Errorf("ID = %q, want %q", a.ID, "build")
	}
	if a.WorkDir != dir {
		t.Errorf("WorkDir = %q, want %q", a.WorkDir, dir)
	}

	dgst, ok := a.Inputs["src"]
	if !ok {
		t.Fatal("missing input key 'src'")
	}
	if dgst.IsZero() {
		t.Fatal("expected non-zero digest for file input")
	}
	if dgst.Algorithm != "sha256" {
		t.Errorf("Algorithm = %q, want %q", dgst.Algorithm, "sha256")
	}

	// Verify the digest matches a fresh computation.
	f, _ := os.Open(filepath.Join(dir, "input.txt"))
	defer f.Close()
	want, _ := cas.ComputeDigest(f)
	if dgst != want {
		t.Errorf("digest = %v, want %v", dgst, want)
	}
}

func TestResolveAbsoluteFileInput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(path, []byte("absolute input\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	actions, err := Resolve(context.Background(), []plugin.ActionSpec{{
		ID:      "build",
		Command: []string{"cat", path},
		Inputs:  map[string]string{"src": path},
	}}, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve absolute input: %v", err)
	}
	if len(actions) != 1 || actions[0].Inputs["src"].IsZero() {
		t.Fatalf("absolute input was not resolved: %#v", actions)
	}
}

// Retries only take effect on network actions, so asking for them anywhere
// else is a plan error rather than a silent no-op.
func TestResolveRejectsRetriesWithoutNetwork(t *testing.T) {
	dir := t.TempDir()
	spec := plugin.ActionSpec{ID: "build", Command: []string{"true"}, Retries: 2}
	_, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, dir, nil, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "retries apply only to network actions") {
		t.Fatalf("Resolve error = %v, want retries rejection", err)
	}
	spec.Network = true
	spec.Impure = true
	if _, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, dir, nil, nil, nil); err != nil {
		t.Fatalf("Resolve network action with retries: %v", err)
	}
}

func TestResolveCrossTargetProducer(t *testing.T) {
	// Simulate a dependent target whose plugin referenced a dep's declared
	// output path (e.g. "infra/vpc/state.json"). The file does NOT exist on
	// disk yet — the producer hasn't run — so Resolve must not try to hash
	// it. Instead it should defer the input and wire an implicit DependsOn
	// edge to the producing action.
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "//pudl/ingest:run",
			Command: []string{"pudl", "ingest"},
			Inputs: map[string]string{
				"state": "infra/vpc/state.json",
			},
			Outputs:   []string{"ingested.db"},
			DependsOn: []string{"//pudl/ingest:setup"},
		},
	}

	producers := map[string]string{
		"infra/vpc/state.json": "//infra/vpc:show",
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, producers, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}

	a := actions[0]
	if !a.Inputs["state"].IsZero() {
		t.Errorf("cross-target input should be a zero-digest placeholder, got %v", a.Inputs["state"])
	}
	// Original DependsOn preserved, plus producer injected.
	gotDeps := map[string]bool{}
	for _, d := range a.DependsOn {
		gotDeps[d] = true
	}
	if !gotDeps["//pudl/ingest:setup"] {
		t.Errorf("DependsOn missing original edge, got %v", a.DependsOn)
	}
	if !gotDeps["//infra/vpc:show"] {
		t.Errorf("DependsOn missing cross-target edge to producer, got %v", a.DependsOn)
	}
}

func TestResolveActionRef(t *testing.T) {
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "link",
			Command: []string{"ld", "-o", "out"},
			Inputs:  map[string]string{"obj": "{action:compile}"},
			Outputs: []string{"out"},
		},
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}

	dgst := actions[0].Inputs["obj"]
	if !dgst.IsZero() {
		t.Errorf("expected zero digest for action ref, got %v", dgst)
	}
	// The ref alone orders the DAG: the plugin did not declare depends_on.
	if got := actions[0].DependsOn; len(got) != 1 || got[0] != "compile" {
		t.Errorf("DependsOn = %v, want [compile]", got)
	}
	if src := actions[0].InputSources["obj"]; src.ProducerID != "compile" || src.OutputPath != "" {
		t.Errorf("InputSources[obj] = %+v, want producer compile with all outputs", src)
	}
}

func TestPrefixActionsQualifiesActionRefs(t *testing.T) {
	specs := []plugin.ActionSpec{{
		ID: "link",
		Inputs: map[string]string{
			"local":  "{action:compile}",
			"remote": "{action://lib:compile}",
			"file":   "main.go",
		},
	}}
	prefixActions("//app", specs)
	want := map[string]string{
		"local":  "{action://app:compile}",
		"remote": "{action://lib:compile}",
		"file":   "main.go",
	}
	for name, value := range want {
		if got := specs[0].Inputs[name]; got != value {
			t.Errorf("Inputs[%s] = %q, want %q", name, got, value)
		}
	}
}

func TestResolveMixedInputs(t *testing.T) {
	dir := t.TempDir()

	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	specs := []plugin.ActionSpec{
		{
			ID:      "compile",
			Command: []string{"go", "build"},
			Inputs: map[string]string{
				"source": "main.go",
				"stdlib": "{action:toolchain}",
			},
			Outputs:   []string{"main"},
			DependsOn: []string{"toolchain"},
			Env:       map[string]string{"GOOS": "linux"},
			Network:   true,
		},
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	a := actions[0]

	// File input should have a real digest.
	if a.Inputs["source"].IsZero() {
		t.Error("expected non-zero digest for file input 'source'")
	}
	// Action ref should have a zero digest.
	if !a.Inputs["stdlib"].IsZero() {
		t.Errorf("expected zero digest for action ref 'stdlib', got %v", a.Inputs["stdlib"])
	}

	// Verify other fields are copied.
	if len(a.DependsOn) != 1 || a.DependsOn[0] != "toolchain" {
		t.Errorf("DependsOn = %v, want [toolchain]", a.DependsOn)
	}
	if a.Env["GOOS"] != "linux" {
		t.Errorf("Env[GOOS] = %q, want %q", a.Env["GOOS"], "linux")
	}
	if !a.Network {
		t.Error("Network = false, want true")
	}
}

func TestResolveMissingFile(t *testing.T) {
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "build",
			Command: []string{"cat"},
			Inputs:  map[string]string{"src": "nonexistent.txt"},
		},
	}

	_, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}

	// Error should mention the action ID and input name.
	errMsg := err.Error()
	if !contains(errMsg, "build") || !contains(errMsg, "src") {
		t.Errorf("error missing context, got: %s", errMsg)
	}
}

func TestResolvePathTraversal(t *testing.T) {
	dir := t.TempDir()

	cases := []string{
		"../../../etc/passwd",
		"../outside",
		"subdir/../../outside",
	}

	for _, input := range cases {
		specs := []plugin.ActionSpec{
			{
				ID:      "build",
				Command: []string{"cat"},
				Inputs:  map[string]string{"src": input},
			},
		}

		_, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
		if err == nil {
			t.Errorf("expected error for traversal input %q, got nil", input)
			continue
		}
		if !contains(err.Error(), "escapes project root") {
			t.Errorf("expected 'escapes project root' error for %q, got: %s", input, err.Error())
		}
	}
}

func TestResolveWorkDirTraversal(t *testing.T) {
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "build",
			Command: []string{"make"},
			WorkDir: "../../etc",
		},
	}

	_, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err == nil {
		t.Fatal("expected error for work_dir traversal, got nil")
	}
	if !contains(err.Error(), "escapes project root") {
		t.Errorf("expected 'escapes project root' error, got: %s", err.Error())
	}
}

func TestResolveWorkDirValid(t *testing.T) {
	dir := t.TempDir()

	// Create the subdirectory so the test is realistic.
	subdir := filepath.Join(dir, "subdir")
	if err := os.MkdirAll(subdir, 0o755); err != nil {
		t.Fatal(err)
	}

	specs := []plugin.ActionSpec{
		{
			ID:      "build",
			Command: []string{"make"},
			WorkDir: "subdir",
		},
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	if actions[0].WorkDir != filepath.Join(dir, "subdir") {
		t.Errorf("WorkDir = %q, want %q", actions[0].WorkDir, filepath.Join(dir, "subdir"))
	}
}

func TestResolveWorkDirEmpty(t *testing.T) {
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "build",
			Command: []string{"make"},
			WorkDir: "",
		},
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}
	if actions[0].WorkDir != dir {
		t.Errorf("WorkDir = %q, want %q", actions[0].WorkDir, dir)
	}
}

func TestResolveSealedInputsPassthrough(t *testing.T) {
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "deploy",
			Command: []string{"kubectl", "apply"},
			SealedInputs: map[string]string{
				"REGISTRY_PASSWORD": "pass:deploy/registry",
				"API_TOKEN":         "vault:secrets/api",
			},
		},
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 1 {
		t.Fatalf("got %d actions, want 1", len(actions))
	}

	a := actions[0]
	if len(a.SealedInputs) != 2 {
		t.Fatalf("SealedInputs count = %d, want 2", len(a.SealedInputs))
	}
	if a.SealedInputs["REGISTRY_PASSWORD"] != "pass:deploy/registry" {
		t.Errorf("SealedInputs[REGISTRY_PASSWORD] = %q, want pass:deploy/registry", a.SealedInputs["REGISTRY_PASSWORD"])
	}
	if a.SealedInputs["API_TOKEN"] != "vault:secrets/api" {
		t.Errorf("SealedInputs[API_TOKEN] = %q, want vault:secrets/api", a.SealedInputs["API_TOKEN"])
	}
}

func TestResolveSealedInputsNil(t *testing.T) {
	dir := t.TempDir()

	specs := []plugin.ActionSpec{
		{
			ID:      "build",
			Command: []string{"make"},
		},
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if actions[0].SealedInputs != nil {
		t.Errorf("SealedInputs = %v, want nil", actions[0].SealedInputs)
	}
}

func TestResolveSealedOutputsForceImpure(t *testing.T) {
	dir := t.TempDir()
	specs := []plugin.ActionSpec{
		{
			ID:      "mint",
			Command: []string{"sh", "-c", "true"},
			// Note: spec.Impure left false; resolver must force it true
			// because cache hits would skip the store_secret side-effect.
			SealedOutputs: map[string]string{
				"ADMIN_PASS": "pass:registry/admin",
			},
		},
	}
	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !actions[0].Impure {
		t.Error("action with sealed_outputs should be forced impure")
	}
	if actions[0].SealedOutputs["ADMIN_PASS"] != "pass:registry/admin" {
		t.Errorf("SealedOutputs[ADMIN_PASS] = %q, want pass:registry/admin", actions[0].SealedOutputs["ADMIN_PASS"])
	}
}

func TestResolveSealedInputsIsolatedCopy(t *testing.T) {
	dir := t.TempDir()

	original := map[string]string{"TOKEN": "pass:deploy/token"}
	specs := []plugin.ActionSpec{
		{
			ID:           "deploy",
			Command:      []string{"kubectl"},
			SealedInputs: original,
		},
	}

	actions, err := Resolve(context.Background(), specs, dir, nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// Mutating the original should not affect the resolved action.
	original["EXTRA"] = "pass:extra"
	if len(actions[0].SealedInputs) != 1 {
		t.Errorf("SealedInputs was not copied; mutation leaked through")
	}
}

func TestResolveEmptySpecs(t *testing.T) {
	actions, err := Resolve(context.Background(), nil, t.TempDir(), nil, nil, nil)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("got %d actions, want 0", len(actions))
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

func TestResolveImplicitDependenciesAreStable(t *testing.T) {
	spec := plugin.ActionSpec{
		ID: "consumer", DependsOn: []string{"z", "explicit", "explicit"},
		Inputs: map[string]string{"a": "out-a", "b": "out-b", "c": "out-c", "d": "out-d"},
	}
	producers := map[string]string{"out-a": "b", "out-b": "a", "out-c": "z", "out-d": "b"}
	for i := 0; i < 100; i++ {
		actions, err := Resolve(context.Background(), []plugin.ActionSpec{spec}, t.TempDir(), nil, producers, nil)
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Join(actions[0].DependsOn, ","); got != "z,explicit,a,b" {
			t.Fatalf("dependency order = %q, want explicit order followed by sorted unique implicit edges", got)
		}
	}
	if strings.Join(spec.DependsOn, ",") != "z,explicit,explicit" {
		t.Fatal("mutated plugin declaration")
	}
}
