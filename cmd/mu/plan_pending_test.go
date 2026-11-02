package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/dag"
)

// An action whose inputs come from upstream actions has no meaningful key at
// plan time; the JSON plan reports pending_inputs instead of a placeholder key.
func TestPlanJSONOmitsActionKeyForPendingInputs(t *testing.T) {
	g := dag.NewGraph()
	if err := g.AddAction(&dag.Action{ID: "//a:build", Command: []string{"true"}, Outputs: []string{"mid.txt"}}); err != nil {
		t.Fatal(err)
	}
	if err := g.AddAction(&dag.Action{
		ID: "//b:build", Command: []string{"true"}, DependsOn: []string{"//a:build"},
		Inputs: map[string]cas.Digest{"mid.txt": {}, "all": {}},
		InputSources: map[string]dag.InputSource{
			"mid.txt": {ProducerID: "//a:build", OutputPath: "mid.txt"},
			"all":     {ProducerID: "//a:build"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	doc := newPlanJSONDocument(&coordinator.PlanResult{Graph: g}, []string{"//b"})
	if len(doc.Actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(doc.Actions))
	}
	if a := doc.Actions[0]; a.ActionKey == "" || len(a.PendingInputs) != 0 {
		t.Errorf("producer: key %q pending %v, want key and no pending", a.ActionKey, a.PendingInputs)
	}
	consumer := doc.Actions[1]
	if consumer.ActionKey != "" {
		t.Errorf("consumer action_key = %q, want omitted", consumer.ActionKey)
	}
	if got := strings.Join(consumer.PendingInputs, ","); got != "all,mid.txt" {
		t.Errorf("consumer pending_inputs = %q, want all,mid.txt", got)
	}
	payload, err := json.Marshal(consumer)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), `"action_key"`) {
		t.Errorf("consumer JSON still carries action_key: %s", payload)
	}
}
