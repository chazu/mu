// Package mu defines the CUE schema for mu.json files.
//
// Two top-level definitions:
//   - #ProjectConfig: the root mu.json of a repository.
//   - #PluginConfig:  the mu.json of a plugin directory (has a "plugin" key).
//
// This schema enforces structural constraints only. Cross-record constraints
// such as "target names must be unique" and "plugin names must be unique"
// remain the responsibility of the Go validator in internal/config.
package mu

// #ProjectConfig is the schema for the repository root mu.json.
#ProjectConfig: {
	targets?:      [...#Target]
	toolchains?:   [...#Toolchain]
	cache?:        #CacheConfig
	publish?:      #PublishConfig
	plugins?:      [...#PluginDef]
	preprocessor?: #Preprocessor
	secrets?:      #SecretsConfig
	...
}

// #PublishConfig describes the target of `mu build --publish`.
#PublishConfig: {
	registry?:   string
	repository?: string
	...
}

// #SecretsConfig declares project-wide policy for the secret subsystem.
// writable_refs is an allow-list of glob patterns; only refs that
// match at least one pattern may be written to via sealed_outputs.
// Omitting the field means "no allow-list" (writes permitted —
// current behavior). Setting it to [] means "deny all writes".
#SecretsConfig: {
	writable_refs?: [...string]
	...
}

// #PluginConfig is the schema for a plugin directory's mu.json.
// Presence of the "plugin" key distinguishes plugin configs from the root.
#PluginConfig: {
	plugin:      #PluginManifest
	targets?:    [...#Target]
	toolchains?: [...#Toolchain]
	...
}

// #TargetName matches either a fully-qualified label ("//path/to/name")
// or a bare identifier used inside plugin configs ("build").
#TargetName: =~"^(//[A-Za-z0-9_][A-Za-z0-9_.:/-]*|[A-Za-z0-9_][A-Za-z0-9_.-]*)$"

// #Target describes a single build target.
#Target: {
	target:                   #TargetName
	toolchain?:               string & !=""
	sources?:                 [...string]
	deps?:                    [...string]
	config?:                  {...}
	plan?:                    [...]
	transform?:               [...]
	sealed_inputs?:           {[string]: string}
	sealed_input_modes?:      {[string]: "env" | "file"}
	sealed_outputs?:          {[string]: string}
	sealed_output_modes?:     {[string]: "create" | "overwrite" | "create_if_absent"}
	kind?:                    "relationship" | "interface" | "component" | "kit" | ""
	implements?:              string
	...
}

// #SHA256 is a lowercase 64-character hex digest.
#SHA256: =~"^[a-f0-9]{64}$"

// #ToolchainConfig holds version + download info for a toolchain.
#ToolchainConfig: {
	version?:      string
	url?:          string
	sha256?:       #SHA256
	strip_prefix?: string
	...
}

// #Toolchain describes a toolchain and its base environment.
#Toolchain: {
	toolchain: string & !=""
	from:      string & !=""
	config:    #ToolchainConfig
	...
}

// #PluginDef describes an external plugin. It must declare exactly one
// source form, enforced by the disjunction below.
#PluginDef: {
	name: string & !=""
	{
		// Form 1: inline command (escape hatch, not stored in CAS).
		command: [string, ...string]
		script?: _|_
		url?:    _|_
		sha256?: _|_
		digest?: _|_
	} | {
		// Form 2: local .bb script (hashed and stored in CAS).
		script:  string & !=""
		command?: _|_
		url?:     _|_
		sha256?:  _|_
		digest?:  _|_
	} | {
		// Form 3: remote .bb script with SHA256 verification.
		url:    string & !=""
		sha256: #SHA256
		command?: _|_
		script?:  _|_
		digest?:  _|_
	} | {
		// Form 4: CAS digest referencing a previously stored plugin.
		digest:  string & !=""
		command?: _|_
		script?:  _|_
		url?:     _|_
		sha256?:  _|_
	}
}

// #CacheBackend describes a single cache backend.
#CacheBackend: {
	type:      "disk" | "oci"
	path?:     string
	registry?: string
	max_size?: string
	read?:     bool
	write?:    bool
	...
}

// #CachePush describes the target of `mu cache push`.
#CachePush: {
	registry?:   string
	repository?: string
	...
}

// #CacheConfig configures the build cache system.
#CacheConfig: {
	backends?:      [...#CacheBackend]
	read_repair?:   bool
	write_through?: bool
	push?:          #CachePush
	...
}

// #Preprocessor transforms non-JSON config files into JSON before loading.
#Preprocessor: {
	extension: string & !=""
	command:   [string, ...string]
	...
}

// #PluginManifest is the "plugin" key in a plugin directory's mu.json.
#PluginManifest: {
	entrypoint: string & !=""
	toolchain?: string
	files?:     [...string]
	guide?:     string
	schemas?:   [...#SchemaDecl]
	...
}

// #SchemaDecl declares a CUE schema module vendored alongside the plugin.
// The path is a directory (relative to the plugin's mu.cue) containing
// .cue files that constitute the module. By convention the directory
// tree mirrors the module path
// (e.g. module "mu/aws" lives under "schemas/mu/aws").
#SchemaDecl: {
	module:  string & !=""
	version: string & !=""
	path:    string & !=""
	...
}
