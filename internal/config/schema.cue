// Package mu defines the CUE schema for mu.cue project files.
//
// Two top-level definitions:
//   - #ProjectConfig: the root mu.cue of a repository.
//   - #PluginConfig:  the mu.cue of a plugin directory (has a "plugin" key).
//
// Every definition is closed: a field the schema does not declare is a load
// error, so a typo (writable_ref, sealed_routng) fails instead of being
// silently ignored. The one open struct is a plugin target's config, which
// the plugin validates against the config_schema it reports from discover.
// The builtin shell and secret-gen toolchains have no discover step, so
// their config is closed here (#ShellConfig, #SecretGenConfig).
//
// This schema enforces structural constraints only. Cross-record constraints
// such as "target names must be unique" and "plugin names must be unique"
// remain the responsibility of the Go validator in internal/config.
package mu

// #ProjectConfig is the schema for the repository root mu.cue.
#ProjectConfig: {
	targets?: [...#Target]
	toolchains?: [...#Toolchain]
	cache?:   #CacheConfig
	publish?: #PublishConfig
	plugins?: [...#PluginDef]
	secrets?: #SecretsConfig
	// A plugin directory's mu.cue is also walked as a sub-config of the
	// project that contains it, so its manifest key is allowed here.
	plugin?: #PluginManifest
}

// #PublishConfig describes the target of `mu build --publish`: repository is
// the base reference "host/namespace" each target's slug is appended to.
#PublishConfig: {
	repository?: string & !=""
}

// #SecretsConfig declares project-wide policy for the secret subsystem.
// writable_refs is an allow-list of glob patterns; only refs that
// match at least one pattern may be written to via sealed_outputs.
// Omitting the field means "no allow-list" (writes permitted —
// current behavior). Setting it to [] means "deny all writes".
#SecretsConfig: {
	writable_refs?: [...string]
}

// #PluginConfig is the schema for a plugin directory's mu.cue.
// Presence of the "plugin" key distinguishes plugin configs from the root.
#PluginConfig: {
	plugin: #PluginManifest
	targets?: [...#Target]
	toolchains?: [...#Toolchain]
}

// #TargetName matches either a fully-qualified label ("//path/to/name")
// or a bare identifier used inside plugin configs ("build").
#TargetName: =~"^(//[A-Za-z0-9_][A-Za-z0-9_.:/-]*|[A-Za-z0-9_][A-Za-z0-9_.-]*)$"

// #Target describes a single build target.
#Target: {
	target:     #TargetName
	toolchain?: string & !=""
	sources?: [...string]
	deps?: [...string]
	sealed_inputs?: {[string]: string}
	sealed_input_modes?: {[string]: "env" | "file"}
	sealed_outputs?: {[string]: string}
	sealed_output_modes?: {[string]: "create" | "overwrite" | "create_if_absent"}

	// config is free-form for plugin toolchains (the plugin validates it
	// against its discover config_schema). The builtin toolchains have no
	// plugin, so the schema closes their config. The branches must be
	// exclusive: unifying #ShellConfig with an open {...} would reopen it.
	_builtin: *"" | string
	if toolchain != _|_ {
		_builtin: toolchain
	}
	if _builtin == "shell" {
		config!: #ShellConfig
	}
	if _builtin == "secret-gen" {
		config!: #SecretGenConfig
	}
	if _builtin != "shell" && _builtin != "secret-gen" {
		config?: {...}
	}
}

// #ShellConfig is the config of a builtin "shell" target (see
// internal/builtin/shell.go).
#ShellConfig: {
	command!: [string, ...string]
	env?: {[string]: string}
	network?: bool
	impure?:  bool
	outputs?: [...string]
	host_paths?: [...string]
	env_passthrough?: [...string]
}

// #SecretGenConfig is the config of a builtin "secret-gen" target (see
// internal/builtin/secretgen.go). The destination is the target's single
// sealed_outputs entry, not a config field.
#SecretGenConfig: {
	derivation!: [string, ...string]
	env?: {[string]: string}
	keep_trailing_newline?: bool
}

// #SHA256 is a lowercase 64-character hex digest.
#SHA256: =~"^[a-f0-9]{64}$"

// #ToolchainConfig holds version + download info for a toolchain.
#ToolchainConfig: {
	version?:      string
	url?:          string
	sha256?:       #SHA256
	strip_prefix?: string
}

// #Toolchain describes a toolchain and its base environment.
#Toolchain: {
	toolchain: string & !=""
	from:      string & !=""
	config:    #ToolchainConfig
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
		script:   string & !=""
		command?: _|_
		url?:     _|_
		sha256?:  _|_
		digest?:  _|_
	} | {
		// Form 3: remote .bb script with SHA256 verification.
		url:      string & !=""
		sha256:   #SHA256
		command?: _|_
		script?:  _|_
		digest?:  _|_
	} | {
		// Form 4: CAS digest referencing a previously stored plugin.
		digest:   string & !=""
		command?: _|_
		script?:  _|_
		url?:     _|_
		sha256?:  _|_
	}
}

// #CacheConfig configures the build cache: local disk plus at most one
// read-only remote ("host/namespace/repo"). push is the repository
// ("host/namespace/repo") `mu cache push` writes to.
#CacheConfig: {
	path?:   string & !=""
	remote?: string & !=""
	push?:   string & !=""
}

// #PluginManifest is the "plugin" key in a plugin directory's mu.cue.
#PluginManifest: {
	entrypoint: string & !=""
	toolchain?: string
	files?: [...string]
	guide?: string
}
