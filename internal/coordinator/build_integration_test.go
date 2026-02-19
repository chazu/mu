package coordinator

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chau/mu/internal/config"
)

func testdataPath(name string) string {
	return filepath.Join("testdata", name)
}

// mockPluginCommand returns a command slice for a testdata mock plugin script.
// Uses an absolute path so the plugin works regardless of the coordinator's ProjectRoot.
func mockPluginCommand(t *testing.T, script string) []string {
	t.Helper()
	abs, err := filepath.Abs(testdataPath(script))
	if err != nil {
		t.Fatalf("filepath.Abs(%s): %v", script, err)
	}
	return []string{"bash", abs}
}

// ---------- Build() integration tests ----------

func TestBuild_SingleTarget(t *testing.T) {
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
	if result.Completed != 1 {
		t.Errorf("Completed = %d, want 1", result.Completed)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
	if result.Cancelled != 0 {
		t.Errorf("Cancelled = %d, want 0", result.Cancelled)
	}
}

func TestBuild_MultipleTargetsSameToolchain(t *testing.T) {
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
		Workers: 2,
	}

	result, err := c.Build(context.Background(), []string{"//lib/a", "//lib/b"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.Completed != 2 {
		t.Errorf("Completed = %d, want 2", result.Completed)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
}

func TestBuild_TargetWithDependencies(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock", Deps: []string{"//lib"}},
				{Name: "//lib", Toolchain: "mock"},
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
	// Both //lib and //app should be built (transitive deps resolved).
	// The second action may be cached since both targets produce identical
	// actions (same command, no inputs), so check the total.
	total := result.Completed + result.Cached
	if total != 2 {
		t.Errorf("Completed+Cached = %d, want 2 (Completed=%d, Cached=%d)", total, result.Completed, result.Cached)
	}
}

func TestBuild_PluginPlanError(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "erroring"},
			},
			Plugins: []config.PluginDef{
				{Name: "erroring", Command: mockPluginCommand(t, "mock_plan_error.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	_, err := c.Build(context.Background(), []string{"//app"})
	if err == nil {
		t.Fatal("expected error from plugin plan error, got nil")
	}
	if !strings.Contains(err.Error(), "compilation failed") {
		t.Errorf("error = %q, want it to contain 'compilation failed'", err)
	}
}

func TestBuild_FailingAction(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "failing"},
			},
			Plugins: []config.PluginDef{
				{Name: "failing", Command: mockPluginCommand(t, "mock_fail_action.sh")},
			},
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	result, err := c.Build(context.Background(), []string{"//app"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.Failed != 1 {
		t.Errorf("Failed = %d, want 1", result.Failed)
	}
	if result.Completed != 0 {
		t.Errorf("Completed = %d, want 0", result.Completed)
	}
}

func TestBuild_MissingTarget(t *testing.T) {
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

	_, err := c.Build(context.Background(), []string{"//nonexistent"})
	if err == nil {
		t.Fatal("expected error for missing target, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to contain 'not found'", err)
	}
}

func TestBuild_NoPluginRegistered(t *testing.T) {
	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "unknown"},
			},
			// No plugins registered — toolchain "unknown" has no matching plugin.
		},
		Store:   newTestStore(t),
		Workers: 1,
	}

	_, err := c.Build(context.Background(), []string{"//app"})
	if err == nil {
		t.Fatal("expected error for unregistered toolchain, got nil")
	}
}

func TestBuild_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

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

	_, err := c.Build(ctx, []string{"//app"})
	if err == nil {
		t.Fatal("expected error from cancelled context, got nil")
	}
}
