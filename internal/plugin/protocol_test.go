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
