// Package builtin provides Go-native plugin implementations that bypass the
// external plugin process system. These are for toolchains where spawning an
// external process (e.g., Babashka) would be unnecessarily heavy.
package builtin

import (
	"fmt"

	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/plugin"
)

// ShellPlan synthesizes ActionSpecs from a shell target's config.
// No external plugin process is needed — the coordinator calls this directly
// for targets with toolchain "shell".
//
// Config fields:
//   - command:         []string (required) — the command to run
//   - env:             map[string]string (optional) — environment variables
//   - network:         bool (optional, default false) — allow network access
//   - impure:          bool (optional, default true) — skip CAS cache
//   - outputs:         []string (optional) — declared output files
//   - observe_command: []string (optional) — command for drift detection
func ShellPlan(target config.Target) ([]plugin.ActionSpec, map[string]string, error) {
	cfg := target.Config
	if cfg == nil {
		return nil, nil, fmt.Errorf("shell target %q: config is required", target.Name)
	}

	// Parse command (must be []string).
	command, err := parseStringSlice(cfg, "command")
	if err != nil {
		return nil, nil, fmt.Errorf("shell target %q: %w", target.Name, err)
	}
	if len(command) == 0 {
		return nil, nil, fmt.Errorf("shell target %q: \"command\" is required and must be a non-empty array of strings", target.Name)
	}

	// Parse optional env.
	env := parseStringMap(cfg, "env")

	// Parse optional flags.
	network := parseBool(cfg, "network", false)
	impure := parseBool(cfg, "impure", true) // shell targets default impure

	// Parse optional outputs.
	outputs, _ := parseStringSlice(cfg, "outputs")

	// Build input map from target sources (hashed for action key).
	inputs := make(map[string]string, len(target.Sources))
	for _, s := range target.Sources {
		inputs[s] = s
	}

	action := plugin.ActionSpec{
		ID:      "run",
		Command: command,
		Inputs:  inputs,
		Outputs: outputs,
		Env:     env,
		Network: network,
		Impure:  impure,
	}

	return []plugin.ActionSpec{action}, nil, nil
}

// parseStringSlice extracts a []string from a config map.
func parseStringSlice(cfg map[string]any, key string) ([]string, error) {
	v, ok := cfg[key]
	if !ok {
		return nil, nil
	}
	arr, ok := v.([]any)
	if !ok {
		return nil, fmt.Errorf("%q must be an array of strings", key)
	}
	result := make([]string, 0, len(arr))
	for _, item := range arr {
		s, ok := item.(string)
		if !ok {
			return nil, fmt.Errorf("%q must be an array of strings, got %T", key, item)
		}
		result = append(result, s)
	}
	return result, nil
}

// parseStringMap extracts a map[string]string from a config map.
func parseStringMap(cfg map[string]any, key string) map[string]string {
	v, ok := cfg[key]
	if !ok {
		return nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil
	}
	result := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			result[k] = s
		}
	}
	return result
}

// parseBool extracts a bool from a config map with a default value.
func parseBool(cfg map[string]any, key string, defaultVal bool) bool {
	v, ok := cfg[key]
	if !ok {
		return defaultVal
	}
	b, ok := v.(bool)
	if !ok {
		return defaultVal
	}
	return b
}
