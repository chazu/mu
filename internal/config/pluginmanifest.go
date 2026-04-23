package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// LoadPluginManifest reads a mu.json from a plugin directory and returns
// the parsed PluginConfig. Returns an error if the file doesn't exist,
// doesn't contain a "plugin" key, or the entrypoint is empty.
//
// LoadPluginManifest is a thin wrapper that dispatches to the decoder
// returned by defaultDecoder. The actual JSON parsing lives in
// loadJSONPluginManifest, which is the jsonDecoder implementation of
// Decoder.DecodePlugin.
func LoadPluginManifest(pluginDir string) (*PluginConfig, error) {
	return defaultDecoder().DecodePlugin(pluginDir)
}

// loadJSONPluginManifest is the legacy JSON implementation of
// plugin-manifest loading. It is wired up as jsonDecoder.DecodePlugin via
// the Decoder interface.
func loadJSONPluginManifest(pluginDir string) (*PluginConfig, error) {
	path := filepath.Join(pluginDir, "mu.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading plugin manifest: %w", err)
	}

	var cfg PluginConfig
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing plugin manifest %s: %w", path, err)
	}

	if cfg.Plugin == nil {
		return nil, fmt.Errorf("plugin manifest %s: missing \"plugin\" key", path)
	}

	if cfg.Plugin.Entrypoint == "" {
		return nil, fmt.Errorf("plugin manifest %s: \"entrypoint\" is required", path)
	}

	return &cfg, nil
}

// IsPluginDir reports whether the given mu.json file (raw bytes) contains
// a "plugin" key at the top level. This is used by the config loader to
// skip plugin directories during the subdirectory walk.
func IsPluginDir(data []byte) bool {
	var probe struct {
		Plugin *json.RawMessage `json:"plugin"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	return probe.Plugin != nil
}
