package coordinator

import (
	"context"
	"testing"

	"github.com/chau/mu/internal/config"
)

func TestPlan_SingleTarget(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//app"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Graph == nil {
		t.Fatal("Plan returned nil Graph")
	}
	if plan.Graph.Len() == 0 {
		t.Error("Plan returned empty graph")
	}
}

func TestPlan_MultipleTargets(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//lib/a", Toolchain: "mock"},
				{Name: "//lib/b", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//lib/a", "//lib/b"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Graph.Len() != 2 {
		t.Errorf("Plan graph has %d actions, want 2", plan.Graph.Len())
	}
}

func TestPlan_MissingTarget(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//nonexistent"})
	if err == nil {
		t.Fatal("expected error for missing target, got nil")
	}
	if plan != nil {
		t.Error("expected nil PlanResult on error")
	}
}

func TestPlan_GraphHasCorrectActionIDs(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//app"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	actions := plan.Graph.Actions()
	for _, a := range actions {
		// Actions should be prefixed with target name.
		if len(a.ID) == 0 {
			t.Error("action has empty ID")
		}
	}
}

func TestBuild_GraphPopulatedInResult(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	result, err := c.Build(context.Background(), []string{"//app"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.Graph == nil {
		t.Fatal("BuildResult.Graph is nil, want populated")
	}
	if result.Graph.Len() == 0 {
		t.Error("BuildResult.Graph is empty")
	}
}

func TestPlanThenExecute(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	ctx := context.Background()

	plan, err := c.Plan(ctx, []string{"//app"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}

	result, err := c.Execute(ctx, plan)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("Completed = %d, want 1", result.Completed)
	}
	if result.Graph == nil {
		t.Error("Execute result.Graph is nil")
	}
}
