package plugin_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chau/mu/internal/plugin"
)

func testdataPath(name string) string {
	return filepath.Join("testdata", name)
}

// ---------------------------------------------------------------------------
// Protocol tests
// ---------------------------------------------------------------------------

func TestProtocolTypesRoundtrip(t *testing.T) {
	t.Run("Request", func(t *testing.T) {
		req := plugin.Request{
			Method: "plan",
			Target: &plugin.TargetInfo{
				Name:      "//lib/foo",
				Toolchain: "go",
				Sources:   []string{"foo.go", "bar.go"},
				Config:    map[string]any{"cgo": true},
			},
			Deps: []plugin.DepInfo{
				{Target: "//lib/utils", Artifacts: map[string]string{"object": "abc123"}},
			},
			ToolchainArtifacts: map[string]string{"sdk": "go1.22"},
		}
		b, err := json.Marshal(req)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got plugin.Request
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Method != req.Method {
			t.Errorf("Method = %q, want %q", got.Method, req.Method)
		}
		if got.Target == nil || got.Target.Name != "//lib/foo" {
			t.Errorf("Target.Name = %v, want //lib/foo", got.Target)
		}
		if len(got.Deps) != 1 || got.Deps[0].Target != "//lib/utils" {
			t.Errorf("Deps = %v, want one dep with target //lib/utils", got.Deps)
		}
		if got.ToolchainArtifacts["sdk"] != "go1.22" {
			t.Errorf("ToolchainArtifacts[sdk] = %q, want go1.22", got.ToolchainArtifacts["sdk"])
		}
	})

	t.Run("DiscoverResponse", func(t *testing.T) {
		resp := plugin.DiscoverResponse{
			Name:            "test-plugin",
			Version:         "1.0.0",
			ProtocolVersion: 1,
			Consumes:        []string{"source:go"},
			Produces:        []string{"object"},
			ConfigSchema:    map[string]any{"type": "object"},
		}
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got plugin.DiscoverResponse
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if got.Name != resp.Name {
			t.Errorf("Name = %q, want %q", got.Name, resp.Name)
		}
		if got.Version != resp.Version {
			t.Errorf("Version = %q, want %q", got.Version, resp.Version)
		}
		if got.ProtocolVersion != 1 {
			t.Errorf("ProtocolVersion = %d, want 1", got.ProtocolVersion)
		}
		if len(got.Consumes) != 1 || got.Consumes[0] != "source:go" {
			t.Errorf("Consumes = %v, want [source:go]", got.Consumes)
		}
		if len(got.Produces) != 1 || got.Produces[0] != "object" {
			t.Errorf("Produces = %v, want [object]", got.Produces)
		}
	})

	t.Run("PlanResponse", func(t *testing.T) {
		resp := plugin.PlanResponse{
			Actions: []plugin.ActionSpec{
				{
					ID:      "compile",
					Command: []string{"go", "build"},
					Inputs:  map[string]string{"src": "main.go"},
					Outputs: []string{"main"},
				},
			},
			Outputs: map[string]string{"binary": "main"},
		}
		b, err := json.Marshal(resp)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var got plugin.PlanResponse
		if err := json.Unmarshal(b, &got); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if len(got.Actions) != 1 || got.Actions[0].ID != "compile" {
			t.Errorf("Actions = %v, want one action with id compile", got.Actions)
		}
		if got.Outputs["binary"] != "main" {
			t.Errorf("Outputs[binary] = %q, want main", got.Outputs["binary"])
		}
	})
}

func TestNewDiscoverRequest(t *testing.T) {
	req := plugin.NewDiscoverRequest()
	if req.Method != "discover" {
		t.Errorf("Method = %q, want discover", req.Method)
	}
	if req.Target != nil {
		t.Errorf("Target = %v, want nil", req.Target)
	}
}

func TestNewPlanRequest(t *testing.T) {
	target := plugin.TargetInfo{
		Name:      "//app",
		Toolchain: "go",
		Sources:   []string{"main.go"},
	}
	deps := []plugin.DepInfo{
		{Target: "//lib", Artifacts: map[string]string{"obj": "sha256:abc"}},
	}
	arts := map[string]string{"sdk": "go1.22"}

	req := plugin.NewPlanRequest(target, deps, arts)
	if req.Method != "plan" {
		t.Errorf("Method = %q, want plan", req.Method)
	}
	if req.Target == nil || req.Target.Name != "//app" {
		t.Errorf("Target = %v, want //app", req.Target)
	}
	if len(req.Deps) != 1 {
		t.Errorf("Deps = %v, want 1 dep", req.Deps)
	}
	if req.ToolchainArtifacts["sdk"] != "go1.22" {
		t.Errorf("ToolchainArtifacts[sdk] = %q, want go1.22", req.ToolchainArtifacts["sdk"])
	}
}

// ---------------------------------------------------------------------------
// Process tests
// ---------------------------------------------------------------------------

func startTestPlugin(t *testing.T, script string) *plugin.Process {
	t.Helper()
	p, err := plugin.StartProcess("test", []string{"bash", testdataPath(script)}, ".")
	if err != nil {
		t.Fatalf("StartProcess(%s): %v", script, err)
	}
	t.Cleanup(func() { p.Close() })
	return p
}

func TestProcessDiscover(t *testing.T) {
	p := startTestPlugin(t, "mock_plugin.sh")
	resp, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}
	if resp.Name != "mock" {
		t.Errorf("Name = %q, want mock", resp.Name)
	}
	if resp.Version != "0.1.0" {
		t.Errorf("Version = %q, want 0.1.0", resp.Version)
	}
	if resp.ProtocolVersion != 1 {
		t.Errorf("ProtocolVersion = %d, want 1", resp.ProtocolVersion)
	}
	if len(resp.Consumes) != 1 || resp.Consumes[0] != "source:mock" {
		t.Errorf("Consumes = %v, want [source:mock]", resp.Consumes)
	}
	if len(resp.Produces) != 1 || resp.Produces[0] != "mock_output" {
		t.Errorf("Produces = %v, want [mock_output]", resp.Produces)
	}
}

func TestProcessPlan(t *testing.T) {
	p := startTestPlugin(t, "mock_plugin.sh")

	// Must discover first (protocol requirement).
	_, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	target := plugin.TargetInfo{
		Name:      "//test",
		Toolchain: "mock",
		Sources:   []string{"input.txt"},
	}
	resp, err := p.Plan(context.Background(), target, nil, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(resp.Actions) != 1 {
		t.Fatalf("Actions count = %d, want 1", len(resp.Actions))
	}
	if resp.Actions[0].ID != "mock-action" {
		t.Errorf("Actions[0].ID = %q, want mock-action", resp.Actions[0].ID)
	}
	if resp.Outputs["mock_output"] != "out.txt" {
		t.Errorf("Outputs[mock_output] = %q, want out.txt", resp.Outputs["mock_output"])
	}
}

func TestProcessTimeout(t *testing.T) {
	p, err := plugin.StartProcess("timeout-test", []string{"bash", testdataPath("mock_timeout.sh")}, ".")
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	// Don't use startTestPlugin helper because we need manual cleanup control.
	// The process will be killed by the timeout.

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = p.Discover(ctx)
	if err == nil {
		t.Fatal("expected error from timed-out Discover, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("error = %q, want it to contain 'context deadline exceeded'", err)
	}
}

func TestProcessCrash(t *testing.T) {
	p, err := plugin.StartProcess("crash-test", []string{"bash", testdataPath("mock_crash.sh")}, ".")
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}

	// Give the process a moment to exit and flush stderr.
	time.Sleep(200 * time.Millisecond)

	_, err = p.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error from crashed plugin, got nil")
	}
	// The error may manifest as a broken pipe (write fails) or as a
	// "process closed stdout" message that includes stderr. Either way
	// we must get an error indicating the plugin is dead.
	errMsg := err.Error()
	if !strings.Contains(errMsg, "something went wrong") &&
		!strings.Contains(errMsg, "broken pipe") &&
		!strings.Contains(errMsg, "process closed stdout") &&
		!strings.Contains(errMsg, "process exited") {
		t.Errorf("error = %q, want it to indicate a crashed process", errMsg)
	}
}

func TestProcessBadVersion(t *testing.T) {
	p := startTestPlugin(t, "mock_bad_version.sh")

	_, err := p.Discover(context.Background())
	if err == nil {
		t.Fatal("expected error from bad protocol version, got nil")
	}
	if !strings.Contains(err.Error(), "protocol version mismatch") {
		t.Errorf("error = %q, want it to contain 'protocol version mismatch'", err)
	}
}

func TestProcessPlanError(t *testing.T) {
	p := startTestPlugin(t, "mock_plan_error.sh")

	_, err := p.Discover(context.Background())
	if err != nil {
		t.Fatalf("Discover: %v", err)
	}

	target := plugin.TargetInfo{
		Name:      "//test",
		Toolchain: "erroring",
		Sources:   []string{"input.txt"},
	}
	_, err = p.Plan(context.Background(), target, nil, nil)
	if err == nil {
		t.Fatal("expected error from plan, got nil")
	}
	if !strings.Contains(err.Error(), "compilation failed") {
		t.Errorf("error = %q, want it to contain 'compilation failed'", err)
	}
}

func TestProcessClose(t *testing.T) {
	p, err := plugin.StartProcess("close-test", []string{"bash", testdataPath("mock_plugin.sh")}, ".")
	if err != nil {
		t.Fatalf("StartProcess: %v", err)
	}
	if err := p.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Manager tests
// ---------------------------------------------------------------------------

func TestManagerStartAndPlan(t *testing.T) {
	m := plugin.NewManager(".")
	err := m.Register(plugin.PluginDef{
		Name:    "mock",
		Command: []string{"bash", testdataPath("mock_plugin.sh")},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer m.Close()

	// Verify discover info was cached.
	info := m.DiscoverInfo("mock")
	if info == nil {
		t.Fatal("DiscoverInfo(mock) = nil, want non-nil")
	}
	if info.Name != "mock" {
		t.Errorf("DiscoverInfo.Name = %q, want mock", info.Name)
	}

	target := plugin.TargetInfo{
		Name:      "//app",
		Toolchain: "mock",
		Sources:   []string{"src.txt"},
	}
	resp, err := m.Plan(ctx, "mock", target, nil, nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(resp.Actions) != 1 {
		t.Fatalf("Actions count = %d, want 1", len(resp.Actions))
	}
	if resp.Actions[0].ID != "mock-action" {
		t.Errorf("Actions[0].ID = %q, want mock-action", resp.Actions[0].ID)
	}
}

func TestManagerUnknownToolchain(t *testing.T) {
	m := plugin.NewManager(".")
	ctx := context.Background()

	target := plugin.TargetInfo{
		Name:      "//app",
		Toolchain: "nonexistent",
		Sources:   []string{"src.txt"},
	}
	_, err := m.Plan(ctx, "nonexistent", target, nil, nil)
	if err == nil {
		t.Fatal("expected error for unknown toolchain, got nil")
	}
	if !strings.Contains(err.Error(), "no plugin registered") {
		t.Errorf("error = %q, want it to contain 'no plugin registered'", err)
	}
}

func TestManagerDuplicateRegister(t *testing.T) {
	m := plugin.NewManager(".")
	def := plugin.PluginDef{
		Name:    "dup",
		Command: []string{"bash", testdataPath("mock_plugin.sh")},
	}
	if err := m.Register(def); err != nil {
		t.Fatalf("first Register: %v", err)
	}
	err := m.Register(def)
	if err == nil {
		t.Fatal("expected error for duplicate register, got nil")
	}
	if !strings.Contains(err.Error(), "already registered") {
		t.Errorf("error = %q, want it to contain 'already registered'", err)
	}
}

func TestManagerClose(t *testing.T) {
	m := plugin.NewManager(".")
	err := m.Register(plugin.PluginDef{
		Name:    "mock",
		Command: []string{"bash", testdataPath("mock_plugin.sh")},
	})
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	ctx := context.Background()
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := m.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}
