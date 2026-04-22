package coordinator

import (
	"context"
	"testing"

	"github.com/chau/mu/internal/config"
)

// TestPlan_CrossTargetArtifact exercises the full cross-target artifact
// pipeline:
//  1. A producer target declares an output path AND declared_outputs.
//  2. A consumer target depends on the producer; its plugin sees the
//     artifact path via DepInfo.Artifacts and emits an action that uses
//     the path as an input.
//  3. The coordinator populates DepInfo.Artifacts, tracks producer paths,
//     and wires a cross-target DependsOn edge so the consumer's action
//     runs only after the producer's.
//
// The producer's output file never exists on disk during planning, so a
// correct implementation must defer the input digest (zero placeholder)
// rather than erroring on a missing file.
func TestPlan_CrossTargetArtifact(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//producer", Toolchain: "producer"},
				{Name: "//consumer", Toolchain: "consumer", Deps: []string{"//producer"}},
			},
			Plugins: []config.PluginDef{
				{Name: "producer", Command: mockPluginCommand(t, "mock_plugin_producer.sh")},
				{Name: "consumer", Command: mockPluginCommand(t, "mock_plugin_consumer.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//consumer"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Graph == nil {
		t.Fatal("Plan returned nil Graph")
	}

	producerID := "//producer:emit"
	consumerID := "//consumer:ingest"

	producer := plan.Graph.Action(producerID)
	if producer == nil {
		t.Fatalf("producer action %q missing from graph", producerID)
	}
	consumer := plan.Graph.Action(consumerID)
	if consumer == nil {
		t.Fatalf("consumer action %q missing from graph", consumerID)
	}

	// The consumer must have a DependsOn edge back to the producer —
	// injected by Resolve when it detected the cross-target path.
	got := map[string]bool{}
	for _, d := range consumer.DependsOn {
		got[d] = true
	}
	if !got[producerID] {
		t.Errorf("consumer.DependsOn missing cross-target edge to %q; got %v",
			producerID, consumer.DependsOn)
	}

	// The consumer's "state" input must be a deferred zero-digest
	// placeholder (file doesn't exist at plan time).
	if !consumer.Inputs["state"].IsZero() {
		t.Errorf("consumer input %q should be deferred (zero digest), got %v",
			"state", consumer.Inputs["state"])
	}
}

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

func TestPlan_ShellTarget_NoPluginsNeeded(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{
					Name:      "//deploy",
					Toolchain: "shell",
					Config: map[string]any{
						"command": []any{"echo", "deployed"},
						"network": true,
					},
				},
			},
			// No plugins registered — shell targets bypass plugin system.
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	plan, err := c.Plan(context.Background(), []string{"//deploy"})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if plan.Graph.Len() != 1 {
		t.Errorf("Graph has %d actions, want 1", plan.Graph.Len())
	}

	actions := plan.Graph.Actions()
	a := actions[0]
	if a.Command[0] != "echo" {
		t.Errorf("Command[0] = %q, want \"echo\"", a.Command[0])
	}
	if !a.Impure {
		t.Error("shell action should be impure by default")
	}
	if !a.Network {
		t.Error("shell action should have network=true as configured")
	}
}

func TestBuild_ShellTarget_Executes(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{
					Name:      "//hello",
					Toolchain: "shell",
					Config: map[string]any{
						"command": []any{"echo", "hello from shell"},
					},
				},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	result, err := c.Build(context.Background(), []string{"//hello"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.Completed != 1 {
		t.Errorf("Completed = %d, want 1", result.Completed)
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
