package muplugin_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/chazu/mu/sdk/muplugin"
)

func TestPlanResponseRejectsRemovedActionFields(t *testing.T) {
	for field, want := range map[string]string{
		`"body": ["'a", "emit"]`:       "action bodies were removed",
		`"ewe_source": "populate.cue"`: "ewe actions were removed",
	} {
		line := `{"actions": [{"id": "run", "command": ["true"], ` + field + `}]}`
		var resp muplugin.PlanResponse
		err := json.Unmarshal([]byte(line), &resp)
		if err == nil || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), `"run"`) {
			t.Errorf("%s: Unmarshal error = %v, want %q naming action \"run\"", field, err, want)
		}
	}
}

func TestPlanResponseDecodesAction(t *testing.T) {
	line := `{"actions": [{"id": "run", "command": ["true"], "outputs": ["a"], "body": null}]}`
	var resp muplugin.PlanResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if len(resp.Actions) != 1 || resp.Actions[0].ID != "run" || len(resp.Actions[0].Command) != 1 || resp.Actions[0].Outputs[0] != "a" {
		t.Fatalf("decoded = %#v", resp.Actions)
	}
}
