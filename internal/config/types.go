package config

// ProjectConfig is the top-level configuration loaded from mu.json.
type ProjectConfig struct {
	Targets      []Target      `json:"targets,omitempty"`
	Toolchains   []Toolchain   `json:"toolchains,omitempty"`
	Services     []Service     `json:"services,omitempty"`
	Triggers     []Trigger     `json:"triggers,omitempty"`
	Cache        *CacheConfig  `json:"cache,omitempty"`
	Plugins      []PluginDef   `json:"plugins,omitempty"`
	Preprocessor *Preprocessor `json:"preprocessor,omitempty"`
}

// Target describes a build target.
type Target struct {
	Name      string         `json:"target"`
	Toolchain string         `json:"toolchain"`
	Sources   []string       `json:"sources"`
	Deps      []string       `json:"deps,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
}

// Service describes an infrastructure service (e.g. a Docker container).
type Service struct {
	Name      string            `json:"service"`
	Runtime   string            `json:"runtime"`
	DependsOn map[string]string `json:"depends_on,omitempty"`
	Config    ServiceConfig     `json:"config"`
}

// ServiceConfig holds runtime-specific configuration for a Service.
type ServiceConfig struct {
	Image       string            `json:"image,omitempty"`
	Command     []string          `json:"command,omitempty"`
	Ports       []string          `json:"ports,omitempty"`
	Volumes     []string          `json:"volumes,omitempty"`
	Environment map[string]string `json:"environment,omitempty"`
	Healthcheck *Healthcheck      `json:"healthcheck,omitempty"`
}

// Healthcheck describes how to determine if a service is healthy.
type Healthcheck struct {
	Type     string `json:"type"`              // "http", "tcp", "command"
	Endpoint string `json:"endpoint,omitempty"` // for http/tcp
	Command  string `json:"command,omitempty"`  // for command type
	Interval string `json:"interval,omitempty"`
	Retries  int    `json:"retries,omitempty"`
}

// Trigger watches files and runs commands or restarts services on change.
type Trigger struct {
	Name     string   `json:"trigger"`
	Watch    []string `json:"watch"`
	Run      string   `json:"run,omitempty"`
	Then     ThenSpec `json:"then,omitempty"`
	Debounce string   `json:"debounce,omitempty"`
}

// ThenSpec describes follow-up actions for a Trigger.
type ThenSpec struct {
	Restart []string `json:"restart,omitempty"`
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
// Either Command or Script must be set, but not both.
//   - Command: a direct executable command (e.g. ["./my-plugin"])
//   - Script:  a .bb script path, executed via the bb toolchain
type PluginDef struct {
	Name    string   `json:"name"`
	Command []string `json:"command,omitempty"`
	Script  string   `json:"script,omitempty"`
}

// Preprocessor configures a file preprocessor that transforms non-JSON
// config files into JSON before loading.
type Preprocessor struct {
	Extension string   `json:"extension"`
	Command   []string `json:"command"`
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
