package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
)

// A dependent target must not start until its dep finishes, even when no
// input path links them: //after reads a file //slow writes after a delay.
func TestBuild_TargetDepsOrderExecution(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name: "//slow", Toolchain: "shell",
				Config: map[string]any{
					"command": []any{"sh", "-c", "sleep 1; echo hi > s.txt"},
					"outputs": []any{"s.txt"},
				},
			}, {
				Name: "//after", Toolchain: "shell", Deps: []string{"//slow"},
				Config: map[string]any{"command": []any{"sh", "-c", "cat s.txt"}},
			}},
		},
		Store:   newTestStore(t),
		Workers: 4,
	}
	result, err := c.Build(context.Background(), []string{"//slow", "//after"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.Failed != 0 || result.Completed != 2 {
		t.Fatalf("Completed=%d Failed=%d, want 2/0", result.Completed, result.Failed)
	}
}

// Building only the dependent pulls its deps in and orders them first,
// including transitively through an intermediate kit-style target.
func TestPlan_UnselectedDepIsPlannedAndOrdered(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//a", Toolchain: "shell", Config: map[string]any{"command": []any{"true"}}},
				{Name: "//b", Toolchain: "shell", Config: map[string]any{"command": []any{"true"}}},
				{Name: "//kit", Toolchain: "shell", Deps: []string{"//b", "//a"}, Config: map[string]any{"command": []any{"true"}}},
				{Name: "//top", Toolchain: "shell", Deps: []string{"//kit"}, Config: map[string]any{"command": []any{"true"}}},
			},
		},
		Store: newTestStore(t),
	}
	plan, err := c.Plan(context.Background(), []string{"//top"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Graph.Len() != 4 {
		t.Fatalf("Graph has %d actions, want 4 (deps pulled in)", plan.Graph.Len())
	}
	if got := plan.Graph.DependsOn("//kit:run"); !reflect.DeepEqual(got, []string{"//a:run", "//b:run"}) {
		t.Errorf("//kit:run DependsOn = %v, want sorted [//a:run //b:run]", got)
	}
	if got := plan.Graph.DependsOn("//top:run"); !reflect.DeepEqual(got, []string{"//kit:run"}) {
		t.Errorf("//top:run DependsOn = %v, want [//kit:run]", got)
	}
}

// Only root actions of the dependent get edges, and only to leaf actions of
// the dep; internal edges are left untouched and nothing is duplicated.
func TestPlan_TargetDepEdgesRootsToLeaves(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name: "//dep", Toolchain: "cfg",
				Config: map[string]any{"actions": []any{
					map[string]any{"id": "one", "command": []any{"true"}},
					map[string]any{"id": "two", "command": []any{"true"}, "depends_on": []any{"//dep:one"}},
				}},
			}, {
				Name: "//use", Toolchain: "cfg", Deps: []string{"//dep"},
				Config: map[string]any{"actions": []any{
					map[string]any{"id": "x", "command": []any{"true"}, "depends_on": []any{"//dep:two"}},
					map[string]any{"id": "y", "command": []any{"true"}, "depends_on": []any{"//use:x"}},
				}},
			}},
			Plugins: []config.PluginDef{{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")}},
		},
		Store: newTestStore(t),
	}
	plan, err := c.Plan(context.Background(), []string{"//use"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.Graph.DependsOn("//use:x"); !reflect.DeepEqual(got, []string{"//dep:two"}) {
		t.Errorf("//use:x DependsOn = %v, want [//dep:two] (deduped)", got)
	}
	if got := plan.Graph.DependsOn("//use:y"); !reflect.DeepEqual(got, []string{"//use:x"}) {
		t.Errorf("//use:y DependsOn = %v, want [//use:x] (non-root untouched)", got)
	}
}

// Output and input spellings are normalised before matching: "./s.txt" and
// "s.txt" link, and an output relative to the producer's work_dir matches
// the project-relative input path. No target deps, so only path wiring can
// create the edge.
func TestPlan_ProducerForPathNormalised(t *testing.T) {
	for _, tc := range []struct {
		name, workDir, output, input string
	}{
		{"dot-slash input", "", "s.txt", "./s.txt"},
		{"dot-slash output", "", "./s.txt", "s.txt"},
		{"work_dir output", "sub", "s.txt", "sub/s.txt"},
		{"work_dir output dot-slash input", "sub", "./s.txt", "./sub/s.txt"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			prod := map[string]any{"id": "emit", "command": []any{"true"}, "outputs": []any{tc.output}}
			if tc.workDir != "" {
				prod["work_dir"] = tc.workDir
			}
			c := &Coordinator{
				ProjectRoot: root,
				Config: &config.ProjectConfig{
					Targets: []config.Target{{
						Name: "//a-prod", Toolchain: "cfg",
						Config: map[string]any{"actions": []any{prod}},
					}, {
						Name: "//b-cons", Toolchain: "cfg",
						Config: map[string]any{"actions": []any{map[string]any{
							"id": "read", "command": []any{"true"}, "inputs": map[string]any{"s": tc.input},
						}}},
					}},
					Plugins: []config.PluginDef{{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")}},
				},
				Store: newTestStore(t),
			}
			plan, err := c.Plan(context.Background(), []string{"//a-prod", "//b-cons"})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if got := plan.Graph.DependsOn("//b-cons:read"); !slices.Contains(got, "//a-prod:emit") {
				t.Errorf("//b-cons:read DependsOn = %v, want //a-prod:emit", got)
			}
		})
	}
}

// Path wiring does not depend on planning order: //app sorts before //gen
// and has no deps, yet its input is //gen's output, so it must be wired to
// //gen and run after it.
func TestBuild_PathConsumerPlannedBeforeProducer(t *testing.T) {
	root := t.TempDir()
	c := &Coordinator{
		ProjectRoot: root,
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name: "//app", Toolchain: "shell", Sources: []string{"gen.txt"},
				Config: map[string]any{
					"command": []any{"sh", "-c", "cat gen.txt > app.txt"},
					"outputs": []any{"app.txt"},
				},
			}, {
				Name: "//gen", Toolchain: "shell",
				Config: map[string]any{
					"command": []any{"sh", "-c", "sleep 1; echo hi > gen.txt"},
					"outputs": []any{"gen.txt"},
				},
			}},
		},
		Store:   newTestStore(t),
		Workers: 4,
	}
	plan, err := c.Plan(context.Background(), []string{"//app", "//gen"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if got := plan.Graph.DependsOn("//app:run"); !slices.Contains(got, "//gen:run") {
		t.Fatalf("//app:run DependsOn = %v, want //gen:run", got)
	}
	result, err := c.Build(context.Background(), []string{"//app", "//gen"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.Failed != 0 || result.Completed != 2 {
		t.Fatalf("Completed=%d Failed=%d, want 2/0", result.Completed, result.Failed)
	}
	got, err := os.ReadFile(filepath.Join(root, "app.txt"))
	if err != nil || string(got) != "hi\n" {
		t.Fatalf("app.txt = %q, %v; want %q", got, err, "hi\n")
	}
}

// A producer whose work_dir is not the project root declares its output
// relative to that work_dir; the consumer names it project-relative. The
// deferred input must carry the producer's spelling so the executor finds
// the output among the producer's recorded outputs or on disk relative to
// the producer's work_dir.
func TestBuild_CrossTargetInputFromProducerWorkDir(t *testing.T) {
	for _, impure := range []bool{false, true} {
		t.Run(map[bool]string{false: "pure", true: "impure"}[impure], func(t *testing.T) {
			root := t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
				t.Fatal(err)
			}
			c := &Coordinator{
				ProjectRoot: root,
				Config: &config.ProjectConfig{
					Targets: []config.Target{{
						Name: "//app", Toolchain: "cfg",
						Config: map[string]any{"actions": []any{map[string]any{
							"id": "read", "command": []any{"sh", "-c", "cat sub/s.txt > app.txt"},
							"inputs": map[string]any{"s": "sub/s.txt"}, "outputs": []any{"app.txt"},
						}}},
					}, {
						Name: "//gen", Toolchain: "cfg",
						Config: map[string]any{"actions": []any{map[string]any{
							"id": "emit", "command": []any{"sh", "-c", "echo hi > s.txt"},
							"work_dir": "sub", "outputs": []any{"./s.txt"}, "impure": impure,
						}}},
					}},
					Plugins: []config.PluginDef{{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")}},
				},
				Store:   newTestStore(t),
				Workers: 4,
			}
			plan, err := c.Plan(context.Background(), []string{"//app", "//gen"})
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			src := plan.Graph.Action("//app:read").InputSources["s"]
			if src.ProducerID != "//gen:emit" || src.OutputPath != "./s.txt" {
				t.Fatalf("InputSources[s] = %+v, want producer //gen:emit with its own path ./s.txt", src)
			}
			result, err := c.Build(context.Background(), []string{"//app", "//gen"})
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			if result.Failed != 0 || result.Completed != 2 {
				t.Fatalf("Completed=%d Failed=%d, want 2/0", result.Completed, result.Failed)
			}
		})
	}
}

// Targets that read each other's outputs (directly, or through a dep plus
// a path input) form a cycle and fail planning, naming both targets.
func TestPlan_CrossTargetPathCycle(t *testing.T) {
	for _, tc := range []struct {
		name   string
		bDeps  []string
		bInput []string
	}{
		{"path both ways", nil, []string{"a.txt"}},
		{"dep plus path", []string{"//a"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Coordinator{
				ProjectRoot: t.TempDir(),
				Config: &config.ProjectConfig{
					Targets: []config.Target{{
						Name: "//a", Toolchain: "shell", Sources: []string{"b.txt"},
						Config: map[string]any{"command": []any{"true"}, "outputs": []any{"a.txt"}},
					}, {
						Name: "//b", Toolchain: "shell", Sources: tc.bInput, Deps: tc.bDeps,
						Config: map[string]any{"command": []any{"true"}, "outputs": []any{"b.txt"}},
					}},
				},
				Store: newTestStore(t),
			}
			_, err := c.Plan(context.Background(), []string{"//a", "//b"})
			if err == nil {
				t.Fatal("Plan succeeded, want cycle error")
			}
			for _, want := range []string{"cycle", "//a", "//b"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not mention %q", err, want)
				}
			}
		})
	}
}

// Two targets declaring the same output path (after normalisation) fail
// planning with an error naming both targets, both actions, and the path.
func TestPlan_DuplicateOutputAcrossTargets(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{{
				Name: "//a", Toolchain: "cfg",
				Config: map[string]any{"actions": []any{map[string]any{
					"id": "w", "command": []any{"true"}, "outputs": []any{"out/x.txt"},
				}}},
			}, {
				Name: "//b", Toolchain: "cfg",
				Config: map[string]any{"actions": []any{map[string]any{
					"id": "w", "command": []any{"true"}, "work_dir": "out", "outputs": []any{"./x.txt"},
				}}},
			}},
			Plugins: []config.PluginDef{{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")}},
		},
		Store: newTestStore(t),
	}
	_, err := c.Plan(context.Background(), []string{"//a", "//b"})
	if err == nil {
		t.Fatal("Plan succeeded, want duplicate output error")
	}
	for _, want := range []string{`"//a"`, `"//b"`, "//a:w", "//b:w", filepath.Join("out", "x.txt")} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
}

// Within one target, actions may share an output path only when the plugin
// orders them; the last writer in the chain is the path's producer.
func TestPlan_DuplicateOutputWithinTarget(t *testing.T) {
	writer := func(id string, extra map[string]any) map[string]any {
		a := map[string]any{"id": id, "command": []any{"true"}, "outputs": []any{"x.txt"}}
		for k, v := range extra {
			a[k] = v
		}
		return a
	}
	after := func(ids ...any) map[string]any { return map[string]any{"depends_on": ids} }
	for _, tc := range []struct {
		name     string
		actions  []any
		producer string // "" means planning must fail
	}{
		{"unordered", []any{writer("gen", nil), writer("fmt", nil)}, ""},
		{"depends_on", []any{writer("gen", nil), writer("fmt", after("gen"))}, "//p:fmt"},
		{"action ref, listed first", []any{
			writer("fmt", map[string]any{"inputs": map[string]any{"src": "{action:gen}"}}),
			writer("gen", nil),
		}, "//p:fmt"},
		{"transitive via non-writer", []any{
			writer("a", nil),
			map[string]any{"id": "b", "command": []any{"true"}, "depends_on": []any{"a"}},
			writer("c", after("b")),
		}, "//p:c"},
		{"siblings of one root", []any{writer("a", nil), writer("b", after("a")), writer("c", after("a"))}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &Coordinator{
				ProjectRoot: t.TempDir(),
				Config: &config.ProjectConfig{
					Targets: []config.Target{{
						Name: "//p", Toolchain: "cfg",
						Config: map[string]any{"actions": tc.actions},
					}, {
						Name: "//q", Toolchain: "cfg",
						Config: map[string]any{"actions": []any{map[string]any{
							"id": "read", "command": []any{"true"}, "inputs": map[string]any{"x": "x.txt"},
						}}},
					}},
					Plugins: []config.PluginDef{{Name: "cfg", Command: mockPluginCommand(t, "mock_plugin_config_actions.sh")}},
				},
				Store: newTestStore(t),
			}
			plan, err := c.Plan(context.Background(), []string{"//p", "//q"})
			if tc.producer == "" {
				if err == nil {
					t.Fatal("Plan succeeded, want duplicate output error")
				}
				for _, want := range []string{`"//p"`, "x.txt", "without a dependency"} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not mention %q", err, want)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("Plan: %v", err)
			}
			if got := plan.Graph.Action("//q:read").InputSources["x"].ProducerID; got != tc.producer {
				t.Fatalf("//q:read input producer = %q, want %q", got, tc.producer)
			}
		})
	}
}
