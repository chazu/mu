package plugin_test

import (
	"encoding/json"
	"testing"

	"github.com/chau/mu/internal/plugin"
)

func TestActionSpecImpureJSONRoundTrip(t *testing.T) {
	spec := plugin.ActionSpec{
		ID:      "fetch-deps",
		Command: []string{"curl", "-O", "https://example.com/dep.tar.gz"},
		Impure:  true,
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got plugin.ActionSpec
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if !got.Impure {
		t.Error("expected Impure=true after round-trip, got false")
	}
	if got.ID != spec.ID {
		t.Errorf("ID = %q, want %q", got.ID, spec.ID)
	}
}

func TestHasCapability_WithCapabilities(t *testing.T) {
	resp := &plugin.DiscoverResponse{
		Capabilities: []string{"discover", "plan", "observe"},
	}
	if !resp.HasCapability("observe") {
		t.Error("expected observe capability")
	}
	if resp.HasCapability("nonexistent") {
		t.Error("unexpected nonexistent capability")
	}
}

func TestHasCapability_EmptyDefaultsToDiscoverPlan(t *testing.T) {
	resp := &plugin.DiscoverResponse{} // old plugin, no capabilities field
	if !resp.HasCapability("discover") {
		t.Error("expected discover as default capability")
	}
	if !resp.HasCapability("plan") {
		t.Error("expected plan as default capability")
	}
	if resp.HasCapability("observe") {
		t.Error("observe should not be a default capability")
	}
}

func TestObserveResponseJSONRoundTrip(t *testing.T) {
	resp := plugin.ObserveResponse{
		Current: map[string]any{
			"active":  true,
			"enabled": true,
			"unit":    "victoriametrics",
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got plugin.ObserveResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.Current == nil {
		t.Fatal("expected non-nil Current")
	}
	if got.Current["active"] != true {
		t.Errorf("Current[active] = %v, want true", got.Current["active"])
	}
}

func TestNewObserveRequest(t *testing.T) {
	target := plugin.TargetInfo{Name: "//k8s/api", Toolchain: "k8s"}
	req := plugin.NewObserveRequest(target, map[string]string{"bin/kubectl": "sha256:abc"})

	if req.Method != "observe" {
		t.Errorf("Method = %q, want observe", req.Method)
	}
	if req.Target.Name != "//k8s/api" {
		t.Errorf("Target.Name = %q, want //k8s/api", req.Target.Name)
	}
}

func TestActionSpecImpureOmitEmpty(t *testing.T) {
	spec := plugin.ActionSpec{
		ID:      "build",
		Command: []string{"go", "build"},
		Impure:  false,
	}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	// With omitempty, "impure" should not appear in the JSON when false.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, exists := raw["impure"]; exists {
		t.Error("impure field should be omitted when false (omitempty)")
	}
}
