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
	req := plugin.NewObserveRequest(target, map[string]string{"bin/kubectl": "sha256:abc"}, nil)

	if req.Method != "observe" {
		t.Errorf("Method = %q, want observe", req.Method)
	}
	if req.Target.Name != "//k8s/api" {
		t.Errorf("Target.Name = %q, want //k8s/api", req.Target.Name)
	}
}

func TestDiscoverResponseOutputSchemaRoundTrip(t *testing.T) {
	resp := plugin.DiscoverResponse{
		Name:            "aws",
		Version:         "0.1.0",
		ProtocolVersion: 1,
		Consumes:        []string{},
		Produces:        []string{"aws:resource"},
		OutputSchema: &plugin.SchemaRef{
			Module:     "mu/aws",
			Version:    "v1",
			Definition: "#EC2Instance",
			Source:     "vendored",
		},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var got plugin.DiscoverResponse
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got.OutputSchema == nil {
		t.Fatal("expected non-nil OutputSchema after round-trip")
	}
	if got.OutputSchema.Module != "mu/aws" {
		t.Errorf("Module = %q, want mu/aws", got.OutputSchema.Module)
	}
	if got.OutputSchema.Version != "v1" {
		t.Errorf("Version = %q, want v1", got.OutputSchema.Version)
	}
	if got.OutputSchema.Definition != "#EC2Instance" {
		t.Errorf("Definition = %q, want #EC2Instance", got.OutputSchema.Definition)
	}
	if got.OutputSchema.Source != "vendored" {
		t.Errorf("Source = %q, want vendored", got.OutputSchema.Source)
	}
}

func TestDiscoverResponseOutputSchemaOmitEmpty(t *testing.T) {
	resp := plugin.DiscoverResponse{
		Name:            "cowsay",
		Version:         "0.1.0",
		ProtocolVersion: 1,
		Consumes:        []string{},
		Produces:        []string{"text"},
	}

	data, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, exists := raw["output_schema"]; exists {
		t.Error("output_schema should be omitted when nil (omitempty)")
	}
}

func TestSchemaRefDefinitionAndSourceOmitEmpty(t *testing.T) {
	ref := plugin.SchemaRef{
		Module:  "mu/aws",
		Version: "v1",
	}
	data, err := json.Marshal(ref)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	if _, exists := raw["definition"]; exists {
		t.Error("definition should be omitted when empty")
	}
	if _, exists := raw["source"]; exists {
		t.Error("source should be omitted when empty")
	}
	if raw["module"] != "mu/aws" {
		t.Errorf("module = %v, want mu/aws", raw["module"])
	}
}

func TestHasCapability_UnaffectedByOutputSchema(t *testing.T) {
	resp := &plugin.DiscoverResponse{
		Capabilities: []string{"discover", "plan"},
		OutputSchema: &plugin.SchemaRef{Module: "mu/aws", Version: "v1"},
	}
	if !resp.HasCapability("plan") {
		t.Error("expected plan capability")
	}
	if resp.HasCapability("observe") {
		t.Error("observe should not be present")
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
