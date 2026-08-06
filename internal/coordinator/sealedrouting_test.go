package coordinator

import (
	"strings"
	"testing"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

func TestValidateStrictSealedRouting(t *testing.T) {
	target := config.Target{
		SealedInputs:      map[string]string{"TOKEN": "env:TOKEN"},
		SealedInputModes:  map[string]string{"TOKEN": "file"},
		SealedOutputs:     map[string]string{"RESULT": "pass:generated/result"},
		SealedOutputModes: map[string]string{"RESULT": "create_if_absent"},
	}
	validActions := []plugin.ActionSpec{
		{
			ID:               "read-a",
			SealedInputs:     map[string]string{"TOKEN": "env:TOKEN"},
			SealedInputModes: map[string]string{"TOKEN": "file"},
		},
		{
			ID:                "read-and-write",
			SealedInputs:      map[string]string{"TOKEN": "env:TOKEN"},
			SealedInputModes:  map[string]string{"TOKEN": "file"},
			SealedOutputs:     map[string]string{"RESULT": "pass:generated/result"},
			SealedOutputModes: map[string]string{"RESULT": "create_if_absent"},
		},
	}

	tests := []struct {
		name    string
		mutate  func(*config.Target, *[]plugin.ActionSpec)
		wantErr string
	}{
		{name: "valid exact claims"},
		{
			name: "unused input",
			mutate: func(_ *config.Target, actions *[]plugin.ActionSpec) {
				for i := range *actions {
					(*actions)[i].SealedInputs = nil
					(*actions)[i].SealedInputModes = nil
				}
			},
			wantErr: `declared input "TOKEN" is not claimed`,
		},
		{
			name: "undeclared claim",
			mutate: func(_ *config.Target, actions *[]plugin.ActionSpec) {
				(*actions)[0].SealedInputs["OTHER"] = "env:OTHER"
			},
			wantErr: `claims undeclared input "OTHER"`,
		},
		{
			name: "reference mismatch",
			mutate: func(_ *config.Target, actions *[]plugin.ActionSpec) {
				(*actions)[0].SealedInputs["TOKEN"] = "env:OTHER"
			},
			wantErr: `input "TOKEN" reference differs`,
		},
		{
			name: "mode mismatch",
			mutate: func(_ *config.Target, actions *[]plugin.ActionSpec) {
				(*actions)[0].SealedInputModes["TOKEN"] = "env"
			},
			wantErr: `input "TOKEN" mode "env" differs`,
		},
		{
			name: "mode without claim",
			mutate: func(_ *config.Target, actions *[]plugin.ActionSpec) {
				(*actions)[0].SealedInputModes["OTHER"] = "env"
			},
			wantErr: `sets an input mode without claiming input "OTHER"`,
		},
		{
			name: "unused output",
			mutate: func(_ *config.Target, actions *[]plugin.ActionSpec) {
				(*actions)[1].SealedOutputs = nil
				(*actions)[1].SealedOutputModes = nil
			},
			wantErr: `declared output "RESULT" is not claimed`,
		},
		{
			name: "ambiguous output",
			mutate: func(_ *config.Target, actions *[]plugin.ActionSpec) {
				(*actions)[0].SealedOutputs = map[string]string{"RESULT": "pass:generated/result"}
				(*actions)[0].SealedOutputModes = map[string]string{"RESULT": "create_if_absent"}
			},
			wantErr: `output "RESULT" is claimed by 2 actions`,
		},
		{
			name: "target mode without declaration",
			mutate: func(target *config.Target, _ *[]plugin.ActionSpec) {
				target.SealedOutputModes["OTHER"] = "create"
			},
			wantErr: `target sets mode for undeclared output "OTHER"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotTarget := cloneRoutingTarget(target)
			gotActions := cloneRoutingActions(validActions)
			if tt.mutate != nil {
				tt.mutate(&gotTarget, &gotActions)
			}
			err := validateStrictSealedRouting(gotTarget, gotActions)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validateStrictSealedRouting: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
			}
		})
	}
}

func TestMapToActionSpecIncludesSealedOutputModes(t *testing.T) {
	spec := mapToActionSpec(map[string]any{
		"sealed_output_modes": map[string]any{"TOKEN": "create_if_absent"},
	})
	if got := spec.SealedOutputModes["TOKEN"]; got != "create_if_absent" {
		t.Fatalf("sealed output mode = %q, want create_if_absent", got)
	}
}

func cloneRoutingTarget(target config.Target) config.Target {
	target.SealedInputs = cloneStringMap(target.SealedInputs)
	target.SealedInputModes = cloneStringMap(target.SealedInputModes)
	target.SealedOutputs = cloneStringMap(target.SealedOutputs)
	target.SealedOutputModes = cloneStringMap(target.SealedOutputModes)
	return target
}

func cloneRoutingActions(actions []plugin.ActionSpec) []plugin.ActionSpec {
	result := append([]plugin.ActionSpec(nil), actions...)
	for index := range result {
		result[index].SealedInputs = cloneStringMap(result[index].SealedInputs)
		result[index].SealedInputModes = cloneStringMap(result[index].SealedInputModes)
		result[index].SealedOutputs = cloneStringMap(result[index].SealedOutputs)
		result[index].SealedOutputModes = cloneStringMap(result[index].SealedOutputModes)
	}
	return result
}
