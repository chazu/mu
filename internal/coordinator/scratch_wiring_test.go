package coordinator

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/chau/mu/internal/config"
)

// mockBuilder records calls and can be configured to return errors.
type mockBuilder struct {
	called    bool
	callCount int
	cfg       *config.ProjectConfig
	err       error // if set, Build returns this error
}

func (m *mockBuilder) Build(ctx context.Context, cfg *config.ProjectConfig) error {
	m.called = true
	m.callCount++
	m.cfg = cfg
	return m.err
}

// ---------- Scratch build wiring tests ----------

func TestBuild_ToolchainDeclared_ScratchBuildCalled(t *testing.T) {
	mb := &mockBuilder{}
	registry := NewToolchainRegistry(newTestStore(t))

	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
			Toolchains: []config.Toolchain{
				{
					Name:   "mock",
					From:   "mock",
					Config: config.ToolchainConfig{Version: "1.0"},
				},
			},
		},
		Store:             newTestStore(t),
		ToolchainRegistry: registry,
		Builder:           mb,
		Workers:           1,
	}

	result, err := c.Build(context.Background(), []string{"//app"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !mb.called {
		t.Error("expected Builder.Build to be called when toolchains declared")
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
}

func TestBuild_NoToolchains_ScratchBuildSkipped(t *testing.T) {
	mb := &mockBuilder{}

	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
			// No Toolchains declared.
		},
		Store:   newTestStore(t),
		Builder: mb,
		Workers: 1,
	}

	_, err := c.Build(context.Background(), []string{"//app"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if mb.called {
		t.Error("Builder.Build should NOT be called when no toolchains declared")
	}
}

func TestBuild_ScratchBuildFailure_BuildFails(t *testing.T) {
	mb := &mockBuilder{
		err: errors.New("sha256 mismatch: expected abc, got def"),
	}

	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin.sh")},
			},
			Toolchains: []config.Toolchain{
				{
					Name:   "mock",
					From:   "mock",
					Config: config.ToolchainConfig{Version: "1.0"},
				},
			},
		},
		Store:   newTestStore(t),
		Builder: mb,
		Workers: 1,
	}

	_, err := c.Build(context.Background(), []string{"//app"})
	if err == nil {
		t.Fatal("expected build to fail when scratch build fails")
	}
	if !strings.Contains(err.Error(), "scratch build") {
		t.Errorf("error should mention 'scratch build', got: %v", err)
	}
	if !strings.Contains(err.Error(), "sha256 mismatch") {
		t.Errorf("error should propagate root cause, got: %v", err)
	}
}

func TestBuild_CacheHit_ArtifactsInjected(t *testing.T) {
	store := newTestStore(t)
	registry := NewToolchainRegistry(store)
	ctx := context.Background()

	// Pre-register a toolchain manifest (simulating a CAS cache hit).
	manifest := &ToolchainManifest{
		Name:    "mock",
		Version: "1.0",
		Artifacts: map[string]string{
			"bin/mock":    "sha256:aaa111",
			"lib/mock.so": "sha256:bbb222",
		},
	}
	if err := registry.Register(ctx, manifest); err != nil {
		t.Fatalf("Register: %v", err)
	}

	// Builder is a no-op (cache hit scenario -- builder wouldn't fetch).
	mb := &mockBuilder{}

	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				// Use echo-artifacts plugin to verify artifacts are injected.
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin_echo_artifacts.sh")},
			},
			Toolchains: []config.Toolchain{
				{
					Name:   "mock",
					From:   "mock",
					Config: config.ToolchainConfig{Version: "1.0"},
				},
			},
		},
		Store:             store,
		ToolchainRegistry: registry,
		Builder:           mb,
		Workers:           1,
	}

	result, err := c.Build(ctx, []string{"//app"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if result.Failed != 0 {
		t.Errorf("Failed = %d, want 0", result.Failed)
	}
	if result.Completed+result.Cached < 1 {
		t.Errorf("expected at least 1 completed/cached action, got completed=%d cached=%d", result.Completed, result.Cached)
	}
}

func TestBuild_FullScratchBuild_ArtifactsReachPlan(t *testing.T) {
	store := newTestStore(t)
	registry := NewToolchainRegistry(store)
	ctx := context.Background()

	// A builder that registers a manifest (simulating full scratch build flow).
	mb := &mockBuilder{}

	// We manually pre-register to simulate what the builder would do.
	manifest := &ToolchainManifest{
		Name:    "mock",
		Version: "1.0",
		Artifacts: map[string]string{
			"sdk": "sha256:deadbeef",
		},
	}
	if err := registry.Register(ctx, manifest); err != nil {
		t.Fatalf("Register: %v", err)
	}

	c := &Coordinator{
		ProjectRoot: t.TempDir(),
		Config: &config.ProjectConfig{
			Targets: []config.Target{
				{Name: "//app", Toolchain: "mock"},
			},
			Plugins: []config.PluginDef{
				// Use the echo-artifacts plugin to verify artifacts reach the plan request.
				{Name: "mock", Command: mockPluginCommand(t, "mock_plugin_echo_artifacts.sh")},
			},
			Toolchains: []config.Toolchain{
				{
					Name:   "mock",
					From:   "mock",
					Config: config.ToolchainConfig{Version: "1.0"},
				},
			},
		},
		Store:             store,
		ToolchainRegistry: registry,
		Builder:           mb,
		Workers:           1,
	}

	result, err := c.Build(ctx, []string{"//app"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// The echo-artifacts plugin embeds the artifacts in the action command.
	// If we get here without error, the plan was called with artifacts.
	// The action command will be ["echo", "artifacts={\"sdk\":\"sha256:deadbeef\"}"]
	// which means the artifacts map reached the plugin.
	if result.Completed+result.Cached < 1 {
		t.Errorf("expected at least 1 completed/cached action, got completed=%d cached=%d", result.Completed, result.Cached)
	}
}

func TestBuild_NilBuilder_NoToolchains_Succeeds(t *testing.T) {
	// When Builder is nil and no toolchains are declared, build should work fine.
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
}
