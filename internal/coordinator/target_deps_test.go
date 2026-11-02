package coordinator

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
		Store:           newTestStore(t),
		NoDiscoverCache: true,
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
				Store:           newTestStore(t),
				NoDiscoverCache: true,
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
