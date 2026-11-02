package coordinator

import (
	"fmt"
	"sort"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

const (
	defaultSealedInputMode  = "env"
	defaultSealedOutputMode = "overwrite"
)

// autoClaimSingleAction is the one routing convenience: when a target declares
// sealed maps and its plan has exactly one action that makes no sealed claims
// of its own, that action claims everything the target declares. Any other
// plan shape must claim explicitly.
func autoClaimSingleAction(target config.Target, actions []plugin.ActionSpec) {
	if len(actions) != 1 || (len(target.SealedInputs) == 0 && len(target.SealedOutputs) == 0) {
		return
	}
	action := &actions[0]
	if len(action.SealedInputs) > 0 || len(action.SealedInputModes) > 0 ||
		len(action.SealedOutputs) > 0 || len(action.SealedOutputModes) > 0 {
		return
	}
	action.SealedInputs = cloneStringMap(target.SealedInputs)
	action.SealedInputModes = cloneStringMap(target.SealedInputModes)
	action.SealedOutputs = cloneStringMap(target.SealedOutputs)
	action.SealedOutputModes = cloneStringMap(target.SealedOutputModes)
}

// validateSealedRouting checks that target-level sealed declarations are the
// complete allow-list of capabilities which actions claim explicitly. It never
// copies a declaration to an action. Every claim must match a declared name,
// reference, and effective mode; every declared input must be claimed at
// least once and every declared output exactly once.
func validateSealedRouting(target config.Target, actions []plugin.ActionSpec) error {
	if err := validateTargetModeKeys("input", target.SealedInputs, target.SealedInputModes); err != nil {
		return err
	}
	if err := validateTargetModeKeys("output", target.SealedOutputs, target.SealedOutputModes); err != nil {
		return err
	}

	inputClaims := make(map[string]int, len(target.SealedInputs))
	outputClaims := make(map[string]int, len(target.SealedOutputs))
	for _, action := range actions {
		if err := validateActionClaims(
			action.ID, "input", target.SealedInputs, target.SealedInputModes,
			action.SealedInputs, action.SealedInputModes, defaultSealedInputMode,
			inputClaims,
		); err != nil {
			return err
		}
		if err := validateActionClaims(
			action.ID, "output", target.SealedOutputs, target.SealedOutputModes,
			action.SealedOutputs, action.SealedOutputModes, defaultSealedOutputMode,
			outputClaims,
		); err != nil {
			return err
		}
	}

	for _, name := range sortedMapKeys(target.SealedInputs) {
		if inputClaims[name] == 0 {
			return fmt.Errorf("sealed routing: declared input %q is not claimed by any action", name)
		}
	}
	for _, name := range sortedMapKeys(target.SealedOutputs) {
		switch outputClaims[name] {
		case 0:
			return fmt.Errorf("sealed routing: declared output %q is not claimed by any action", name)
		case 1:
		default:
			return fmt.Errorf("sealed routing: declared output %q is claimed by %d actions; want exactly one", name, outputClaims[name])
		}
	}
	return nil
}

func validateTargetModeKeys(kind string, refs, modes map[string]string) error {
	for _, name := range sortedMapKeys(modes) {
		if _, ok := refs[name]; !ok {
			return fmt.Errorf("sealed routing: target sets mode for undeclared %s %q", kind, name)
		}
	}
	return nil
}

func validateActionClaims(
	actionID, kind string,
	declaredRefs, declaredModes, claimedRefs, claimedModes map[string]string,
	defaultMode string,
	claimCounts map[string]int,
) error {
	for _, name := range sortedMapKeys(claimedModes) {
		if _, ok := claimedRefs[name]; !ok {
			return fmt.Errorf("sealed routing: action %q sets an %s mode without claiming %s %q", actionID, kind, kind, name)
		}
	}
	for _, name := range sortedMapKeys(claimedRefs) {
		declaredRef, ok := declaredRefs[name]
		if !ok {
			return fmt.Errorf("sealed routing: action %q claims undeclared %s %q", actionID, kind, name)
		}
		if claimedRefs[name] != declaredRef {
			return fmt.Errorf("sealed routing: action %q %s %q reference differs from its target declaration", actionID, kind, name)
		}
		declaredMode := effectiveMode(declaredModes[name], defaultMode)
		claimedMode := effectiveMode(claimedModes[name], defaultMode)
		if claimedMode != declaredMode {
			return fmt.Errorf("sealed routing: action %q %s %q mode %q differs from target mode %q", actionID, kind, name, claimedMode, declaredMode)
		}
		claimCounts[name]++
	}
	return nil
}

func effectiveMode(mode, fallback string) string {
	if mode == "" {
		return fallback
	}
	return mode
}

func sortedMapKeys[V any](values map[string]V) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
