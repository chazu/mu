package config

// Decoder abstracts the concrete config file format from the rest of the
// codebase. Load and LoadPluginManifest dispatch through this interface so
// alternative front-ends (for example, a future CUE-based decoder) can be
// dropped in without changing call sites.
//
// Decode loads a full project configuration, given the project root
// directory. DecodePlugin loads a plugin manifest, given a plugin directory.
// Both take the same kind of "path" argument that the current public
// entrypoints take.
type Decoder interface {
	Decode(path string) (*ProjectConfig, error)
	DecodePlugin(path string) (*PluginConfig, error)
}

// defaultDecoder returns the Decoder used by the public Load and
// LoadPluginManifest entry points. For now this is always the JSON decoder;
// a future change may select a different decoder based on filesystem probes
// or configuration.
func defaultDecoder() Decoder { return jsonDecoder{} }

// jsonDecoder is the legacy JSON-based Decoder implementation. Its methods
// wrap the existing unexported loaders verbatim so that switching callers
// to the Decoder interface introduces no behavioral change.
type jsonDecoder struct{}

// Decode loads a project configuration rooted at projectRoot by delegating
// to the legacy JSON loader.
func (jsonDecoder) Decode(projectRoot string) (*ProjectConfig, error) {
	return loadJSONProject(projectRoot)
}

// DecodePlugin loads a plugin manifest from pluginDir by delegating to the
// legacy JSON plugin-manifest loader.
func (jsonDecoder) DecodePlugin(pluginDir string) (*PluginConfig, error) {
	return loadJSONPluginManifest(pluginDir)
}
