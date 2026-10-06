package config

import "fmt"

// LoadPluginManifest reads a mu.cue from a plugin directory and returns
// the parsed PluginConfig. Returns an error if the file doesn't exist,
// doesn't contain a "plugin" key, or the entrypoint is empty.
func LoadPluginManifest(pluginDir string, offline ...bool) (*PluginConfig, error) {
	if !hasMuCue(pluginDir) {
		return nil, fmt.Errorf("reading plugin manifest: no mu.cue in %s", pluginDir)
	}
	return cueDecoder{offline: len(offline) > 0 && offline[0]}.DecodePlugin(pluginDir)
}
