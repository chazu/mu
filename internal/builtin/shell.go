// Package builtin provides Go-native plugin implementations that bypass the
// external plugin process system. These are for toolchains where spawning an
// external process (e.g., Babashka) would be unnecessarily heavy.
package builtin

import (
	"fmt"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
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

	// The schema already checked these at load time; a wrong type here
	// means config built outside mu.cue, and is still an error.
	env, err := parseStringMap(cfg, "env")
	if err != nil {
		return nil, nil, fmt.Errorf("shell target %q: %w", target.Name, err)
	}
	network, err := parseBool(cfg, "network", false)
	if err != nil {
		return nil, nil, fmt.Errorf("shell target %q: %w", target.Name, err)
	}
	impure, err := parseBool(cfg, "impure", true) // shell targets default impure
	if err != nil {
		return nil, nil, fmt.Errorf("shell target %q: %w", target.Name, err)
	}
	outputs, err := parseStringSlice(cfg, "outputs")
	if err != nil {
		return nil, nil, fmt.Errorf("shell target %q: %w", target.Name, err)
	}

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

// parseStringMap extracts a map[string]string from a config map. A missing
// key is nil; a non-map value or a non-string entry is an error.
func parseStringMap(cfg map[string]any, key string) (map[string]string, error) {
	v, ok := cfg[key]
	if !ok {
		return nil, nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%q must be a map of strings, got %T", key, v)
	}
	result := make(map[string]string, len(m))
	for k, val := range m {
		s, ok := val.(string)
		if !ok {
			return nil, fmt.Errorf("%q entry %q must be a string, got %T", key, k, val)
		}
		result[k] = s
	}
	return result, nil
}

// parseBool extracts a bool from a config map, returning defaultVal when the
// key is missing. A non-bool value is an error.
func parseBool(cfg map[string]any, key string, defaultVal bool) (bool, error) {
	v, ok := cfg[key]
	if !ok {
		return defaultVal, nil
	}
	b, ok := v.(bool)
	if !ok {
		return false, fmt.Errorf("%q must be a bool, got %T", key, v)
	}
	return b, nil
}
