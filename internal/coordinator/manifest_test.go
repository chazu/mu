package coordinator

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/dag"
)

func TestNewManifestValidJSON(t *testing.T) {
	execResult := &dag.ExecuteResult{
		Completed: []dag.ActionStatus{
			{ID: "a", Cached: false, ExitCode: 0, Outputs: map[string]cas.Digest{
				"out.bin": cas.NewSHA256("aabbccdd"),
			}},
			{ID: "b", Cached: true, ExitCode: 0, Outputs: map[string]cas.Digest{
				"lib.so": cas.NewSHA256("11223344"),
			}},
		},
		Failed: []dag.ActionStatus{
			{ID: "c", ExitCode: 1},
		},
		Cancelled: []string{"d"},
	}

	result := &BuildResult{
		Completed:  1,
		Cached:     1,
		Failed:     1,
		Cancelled:  1,
		ExecResult: execResult,
	}

	targets := []config.Target{
		{Name: "//app", Toolchain: "go", Kind: "component", Implements: "interface/app"},
	}

	elapsed := 2500 * time.Millisecond
	manifest := NewManifest(result, execResult, targets, elapsed)

	// Verify basic fields.
	if manifest.Version != 1 {
		t.Errorf("Version = %d, want 1", manifest.Version)
	}
	if manifest.Type != "mu.build.manifest/v1" {
		t.Errorf("Type = %q, want mu.build.manifest/v1", manifest.Type)
	}
	if manifest.Duration != 2.5 {
		t.Errorf("Duration = %f, want 2.5", manifest.Duration)
	}
	if manifest.Timestamp == "" {
		t.Error("Timestamp is empty")
	}

	// Verify actions.
	if len(manifest.Actions) != 3 {
		t.Fatalf("Actions count = %d, want 3", len(manifest.Actions))
	}

	// Check completed action with outputs.
	found := false
	for _, a := range manifest.Actions {
		if a.ID == "a" {
			found = true
			if a.Cached {
				t.Error("action 'a' should not be cached")
			}
			if a.ExitCode != 0 {
				t.Errorf("action 'a' exit code = %d, want 0", a.ExitCode)
			}
			if a.Outputs["out.bin"] != "sha256:aabbccdd" {
				t.Errorf("action 'a' output = %q, want sha256:aabbccdd", a.Outputs["out.bin"])
			}
		}
	}
	if !found {
		t.Error("action 'a' not found in manifest")
	}

	// Check cached action.
	for _, a := range manifest.Actions {
		if a.ID == "b" {
			if !a.Cached {
				t.Error("action 'b' should be cached")
			}
		}
	}

	// Check failed action.
	for _, a := range manifest.Actions {
		if a.ID == "c" {
			if a.ExitCode != 1 {
				t.Errorf("action 'c' exit code = %d, want 1", a.ExitCode)
			}
		}
	}

	// Verify summary.
	if manifest.Summary.Completed != 1 {
		t.Errorf("Summary.Completed = %d, want 1", manifest.Summary.Completed)
	}
	if manifest.Summary.Cached != 1 {
		t.Errorf("Summary.Cached = %d, want 1", manifest.Summary.Cached)
	}
	if manifest.Summary.Failed != 1 {
		t.Errorf("Summary.Failed = %d, want 1", manifest.Summary.Failed)
	}
	if manifest.Summary.Cancelled != 1 {
		t.Errorf("Summary.Cancelled = %d, want 1", manifest.Summary.Cancelled)
	}

	// Verify it produces valid JSON.
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Round-trip: unmarshal back.
	var roundTrip Manifest
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if roundTrip.Version != 1 {
		t.Errorf("round-trip Version = %d, want 1", roundTrip.Version)
	}
	if roundTrip.Type != "mu.build.manifest/v1" {
		t.Errorf("round-trip Type = %q", roundTrip.Type)
	}
	if len(roundTrip.Actions) != 3 {
		t.Errorf("round-trip Actions count = %d, want 3", len(roundTrip.Actions))
	}
}

func TestNewManifestEmptyBuild(t *testing.T) {
	execResult := &dag.ExecuteResult{}
	result := &BuildResult{ExecResult: execResult}
	elapsed := 100 * time.Millisecond

	manifest := NewManifest(result, execResult, nil, elapsed)

	if len(manifest.Actions) != 0 {
		t.Errorf("expected 0 actions, got %d", len(manifest.Actions))
	}
	if manifest.Summary.Completed != 0 || manifest.Summary.Failed != 0 {
		t.Errorf("expected zero summary, got %+v", manifest.Summary)
	}
}

func TestNewManifestBRICKMetadata(t *testing.T) {
	execResult := &dag.ExecuteResult{
		Completed: []dag.ActionStatus{
			{ID: "//app:build", ExitCode: 0},
		},
	}
	result := &BuildResult{Completed: 1, ExecResult: execResult}
	targets := []config.Target{
		{Name: "//app", Toolchain: "k8s", Kind: "component", Implements: "interface/app"},
		{Name: "//infra", Toolchain: "terraform", Kind: "component"},
	}

	manifest := NewManifest(result, execResult, targets, time.Second)

	if len(manifest.Targets) != 2 {
		t.Fatalf("Targets count = %d, want 2", len(manifest.Targets))
	}

	app := manifest.Targets[0]
	if app.Kind != "component" {
		t.Errorf("app.Kind = %q, want component", app.Kind)
	}
	if app.Implements != "interface/app" {
		t.Errorf("app.Implements = %q, want interface/app", app.Implements)
	}

	infra := manifest.Targets[1]
	if infra.Implements != "" {
		t.Errorf("infra.Implements = %q, want empty", infra.Implements)
	}

	// Verify BRICK fields round-trip through JSON.
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var rt Manifest
	if err := json.Unmarshal(data, &rt); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if rt.Targets[0].Kind != "component" {
		t.Errorf("round-trip Kind = %q, want component", rt.Targets[0].Kind)
	}
	if rt.Targets[0].Implements != "interface/app" {
		t.Errorf("round-trip Implements = %q, want interface/app", rt.Targets[0].Implements)
	}
}
