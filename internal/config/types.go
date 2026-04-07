package config

// ProjectConfig is the top-level configuration loaded from mu.json.
type ProjectConfig struct {
	Targets      []Target      `json:"targets,omitempty"`
	Toolchains   []Toolchain   `json:"toolchains,omitempty"`
	Cache        *CacheConfig  `json:"cache,omitempty"`
	Plugins      []PluginDef   `json:"plugins,omitempty"`
	Preprocessor *Preprocessor `json:"preprocessor,omitempty"`

	// PluginDirs is populated by the loader — directories (relative to
	// project root) whose mu.json contains a "plugin" key. These are
	// bundled into CAS after their build targets complete.
	PluginDirs []string `json:"-"`
}

// Target describes a build target.
type Target struct {
	Name         string            `json:"target"`
	Toolchain    string            `json:"toolchain"`
	Sources      []string          `json:"sources"`
	Deps         []string          `json:"deps,omitempty"`
	Config       map[string]any    `json:"config,omitempty"`
	SealedInputs map[string]string `json:"sealed_inputs,omitempty"` // env name → secret ref (e.g. "pass:deploy/token")
	// BRICK classification (optional, set by pudl export-actions).
	// mu does not validate these — pudl enforces BRICK constraints via CUE.
	Kind       string `json:"kind,omitempty"`       // "relationship", "interface", "component", "kit"
	Implements string `json:"implements,omitempty"` // interface this component satisfies
}

// Toolchain describes a build toolchain and its base environment.
type Toolchain struct {
	Name   string          `json:"toolchain"`
	From   string          `json:"from"`
	Config ToolchainConfig `json:"config"`
}

// ToolchainConfig holds version and download information for a Toolchain.
type ToolchainConfig struct {
	Version     string `json:"version,omitempty"`
	URL         string `json:"url,omitempty"`
	SHA256      string `json:"sha256,omitempty"`
	StripPrefix string `json:"strip_prefix,omitempty"`
}

// PluginDef defines an external plugin and how to invoke it.
// Resolution order:
//   - Script: a .bb script path (local, vendored in repo). Hashed and stored in CAS.
//   - URL+SHA256: a remote .bb script. Fetched, verified, stored in CAS.
//   - Digest: a CAS digest referencing a previously stored plugin script.
//   - Command: a direct executable (escape hatch, not stored in CAS).
type PluginDef struct {
	Name    string   `json:"name"`
	Command []string `json:"command,omitempty"`
	Script  string   `json:"script,omitempty"`
	URL     string   `json:"url,omitempty"`
	SHA256  string   `json:"sha256,omitempty"`
	Digest  string   `json:"digest,omitempty"`
}

// Preprocessor configures a file preprocessor that transforms non-JSON
// config files into JSON before loading.
type Preprocessor struct {
	Extension string   `json:"extension"`
	Command   []string `json:"command"`
}

// PluginManifest is the "plugin" key in a plugin directory's mu.json.
// It declares the entrypoint, optional runtime toolchain, and files to
// include in the CAS bundle.
type PluginManifest struct {
	Entrypoint string   `json:"entrypoint"`           // relative path to the executable within the plugin dir
	Toolchain  string   `json:"toolchain,omitempty"`   // runtime toolchain (e.g. "bb"); empty = direct execution
	Files      []string `json:"files,omitempty"`        // files to include in CAS bundle; empty = all files
	Guide      string   `json:"guide,omitempty"`        // relative path to a guide text file (bundled automatically)
}

// PluginConfig is the structure of a plugin directory's mu.json.
// It extends the normal project config with a Plugin field.
type PluginConfig struct {
	Plugin     *PluginManifest `json:"plugin,omitempty"`
	Targets    []Target        `json:"targets,omitempty"`
	Toolchains []Toolchain     `json:"toolchains,omitempty"`
}

// CacheConfig configures the build cache system.
type CacheConfig struct {
	Backends     []CacheBackend `json:"backends,omitempty"`
	ReadRepair   bool           `json:"read_repair,omitempty"`
	WriteThrough bool           `json:"write_through,omitempty"`
}

// CacheBackend describes a single cache backend (disk or OCI registry).
type CacheBackend struct {
	Type     string `json:"type"`              // "disk" or "oci"
	Path     string `json:"path,omitempty"`     // for disk
	Registry string `json:"registry,omitempty"` // for oci
	MaxSize  string `json:"max_size,omitempty"`
	Read     *bool  `json:"read,omitempty"`
	Write    *bool  `json:"write,omitempty"`
}
