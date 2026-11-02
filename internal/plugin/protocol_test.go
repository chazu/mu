package plugin_test

import (
	"encoding/json"
	"testing"

	"github.com/chazu/mu/internal/plugin"
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

func TestTargetInfoSealedOutputModesJSONRoundTrip(t *testing.T) {
	target := plugin.TargetInfo{
		Name:              "//secrets/create",
		Toolchain:         "secret-writer",
		SealedOutputs:     map[string]string{"TOKEN": "pass:generated/token"},
		SealedOutputModes: map[string]string{"TOKEN": "create_if_absent"},
	}
	payload, err := json.Marshal(target)
	if err != nil {
		t.Fatal(err)
	}
	var got plugin.TargetInfo
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatal(err)
	}
	if got.SealedOutputs["TOKEN"] != "pass:generated/token" || got.SealedOutputModes["TOKEN"] != "create_if_absent" {
		t.Fatalf("round trip = %#v", got)
	}
}

func TestHasCapability_WithCapabilities(t *testing.T) {
	resp := &plugin.DiscoverResponse{
		Capabilities: []string{"discover", "plan", "store_secret"},
	}
	if !resp.HasCapability("store_secret") {
		t.Error("expected store_secret capability")
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
	if resp.HasCapability("resolve_secret") {
		t.Error("resolve_secret should not be a default capability")
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
