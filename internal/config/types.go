package config

// ProjectConfig is the top-level configuration loaded from mu.cue.
type ProjectConfig struct {
	Targets      []Target       `json:"targets,omitempty"`
	Toolchains   []Toolchain    `json:"toolchains,omitempty"`
	Cache        *CacheConfig   `json:"cache,omitempty"`
	Publish      *PublishConfig `json:"publish,omitempty"`
	Plugins      []PluginDef    `json:"plugins,omitempty"`
	Preprocessor *Preprocessor  `json:"preprocessor,omitempty"`
	Secrets      *SecretsConfig `json:"secrets,omitempty"`
	Advice       []AdviceDef    `json:"advice,omitempty"`

	// PluginDirs is populated by the loader — directories (relative to
	// project root) whose mu.cue contains a "plugin" key. These are
	// bundled into CAS after their build targets complete.
	PluginDirs []string `json:"-"`
}

// SecretsConfig holds project-wide policy for the secret subsystem.
//
// WritableRefs, if non-nil, gates which refs may be written to via
// sealed_outputs / secret-gen / store_secret. Each pattern is matched
// against the full ref (including scheme) using path.Match semantics:
// "*" matches any run of characters except "/", literal text matches
// literally. A nil slice means "no allow-list configured" (writes are
// permitted, current behavior). An empty slice ([]) means "deny all
// writes" — an explicit project-wide lockdown.
//
// Examples:
//   - "pass:registry/*"        — only single-segment refs under pass:registry/
//   - "pass:loosh/*/key"       — anything matching that shape
//   - "pass:secrets/admin"     — exactly that ref
type SecretsConfig struct {
	WritableRefs []string `json:"writable_refs,omitempty"`
}

// Target describes a build target.
type Target struct {
	Name              string            `json:"target"`
	Toolchain         string            `json:"toolchain,omitempty"`
	Sources           []string          `json:"sources"`
	Deps              []string          `json:"deps,omitempty"`
	Config            map[string]any    `json:"config,omitempty"`
	SealedInputs      map[string]string `json:"sealed_inputs,omitempty"`       // name → secret ref (e.g. "pass:deploy/token")
	SealedInputModes  map[string]string `json:"sealed_input_modes,omitempty"`  // name → delivery mode: "env" (default) or "file"
	SealedOutputs     map[string]string `json:"sealed_outputs,omitempty"`      // file name → secret ref; action writes to $MU_SEALED_OUT_DIR/<name>
	SealedOutputModes map[string]string `json:"sealed_output_modes,omitempty"` // file name → store mode: create, overwrite, or create_if_absent
	SealedRouting     string            `json:"sealed_routing,omitempty"`      // empty = convenience routing; "strict" = explicit action claims
	Plan              []any             `json:"plan,omitempty"`                // pith plan program (alternative to plugin planning)
	Transform         []any             `json:"transform,omitempty"`           // pith transform program (runs after deps complete)
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

// AdviceDef declares an advice plugin and its configuration.
// Advice plugins are observers — they react to build lifecycle events
// (e.g., posting results to a webhook) but do not produce build actions.
// Advice errors are non-fatal to the build.
type AdviceDef struct {
	Plugin       string            `json:"plugin"`                  // plugin name (must match a plugin in plugins[])
	Phases       []string          `json:"phases,omitempty"`        // lifecycle phases: "after-build", etc.
	Config       map[string]any    `json:"config,omitempty"`        // plugin-specific configuration
	SealedInputs map[string]string `json:"sealed_inputs,omitempty"` // name → secret ref for advice secrets
}

// Preprocessor configures a file preprocessor that transforms non-JSON
// config files into JSON before loading.
type Preprocessor struct {
	Extension string   `json:"extension"`
	Command   []string `json:"command"`
}

// PluginManifest is the "plugin" key in a plugin directory's mu.cue.
// It declares the entrypoint, optional runtime toolchain, and files to
// include in the CAS bundle.
type PluginManifest struct {
	Entrypoint string       `json:"entrypoint"`          // relative path to the executable within the plugin dir
	Toolchain  string       `json:"toolchain,omitempty"` // runtime toolchain (e.g. "bb"); empty = direct execution
	Files      []string     `json:"files,omitempty"`     // files to include in CAS bundle; empty = all files
	Guide      string       `json:"guide,omitempty"`     // relative path to a guide text file (bundled automatically)
	Schemas    []SchemaDecl `json:"schemas,omitempty"`   // vendored CUE schema modules shipped with the plugin
}

// SchemaDecl declares a CUE schema module vendored alongside a plugin.
// The Path is a directory relative to the plugin's mu.cue containing
// .cue files that constitute the module. By convention the directory
// tree under the plugin's schemas/ root mirrors the module path
// (e.g. module "mu/aws" lives under "schemas/mu/aws").
type SchemaDecl struct {
	Module  string `json:"module"`
	Version string `json:"version"`
	Path    string `json:"path"`
}

// PluginConfig is the structure of a plugin directory's mu.cue.
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
	Push         *CachePush     `json:"push,omitempty"`
}

// CachePush configures the target of `mu cache push`. Registry is the OCI
// registry host (e.g. "registry.loosh.cloud"); Repository is the repository
// path within that registry (e.g. "mu-cache"). Both must be set for push to
// succeed.
type CachePush struct {
	Registry   string `json:"registry,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// PublishConfig configures `mu build --publish`. Registry is the OCI registry
// host (e.g. "registry.platform.loosh.cloud"); Repository is the base repo path
// the published artifact lands under, to which the target slug is appended
// (e.g. "loosh-industries" → "loosh-industries/image/akashic"). The first
// segment must be an org/user the pusher can push to.
type PublishConfig struct {
	Registry   string `json:"registry,omitempty"`
	Repository string `json:"repository,omitempty"`
}

// CacheBackend describes a single cache backend (disk or OCI registry).
type CacheBackend struct {
	Type     string `json:"type"`               // "disk" or "oci"
	Path     string `json:"path,omitempty"`     // for disk
	Registry string `json:"registry,omitempty"` // for oci
	MaxSize  string `json:"max_size,omitempty"`
	Read     *bool  `json:"read,omitempty"`
	Write    *bool  `json:"write,omitempty"`
}
