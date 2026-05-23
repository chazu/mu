mu guide mu.cue — configuration file reference

mu.cue is the project configuration file, written in CUE
(cuelang.org). mu discovers it by walking up from the current
directory. Subdirectories may contain their own mu.cue files — they
are merged automatically. CUE accepts JSON-shaped syntax as well, so
existing JSON-style configs keep working.

MINIMAL EXAMPLE

  package mu

  plugins: [{name: "go", script: "plugins/go/plugin.bb"}]

  targets: [{
      target:    "//cmd/myapp"
      toolchain: "go"
      sources:   ["cmd/myapp/*.go", "internal/**/*.go"]
      config: {
          output: "myapp"
          pkg:    "./cmd/myapp"
      }
  }]

TOP-LEVEL KEYS

  targets       Array of build targets (see below).
  plugins       Array of plugin definitions (see 'mu guide plugins').
  toolchains    Array of toolchain bootstrap definitions (see 'mu guide toolchains').
  cache         Cache configuration (optional).
  secrets       Project-wide secret policy, e.g. writable_refs (optional).
                See 'mu guide secrets'.
  preprocessor  Config preprocessor for non-CUE/JSON formats (optional).

TARGET FIELDS

  target               Target name. Convention: "//path/to/name".
  toolchain            Which toolchain handles this target. Built-in:
                       "shell", "secret-gen". Otherwise the name of a
                       registered plugin (e.g. "go", "file", "k8s").
  sources              Array of source file paths or globs ("src/**/*.go").
  deps                 Array of target names this depends on (optional).
  config               Toolchain-specific config object (optional).
  sealed_inputs        {NAME: "scheme:path"} — secret refs to resolve
                       before exec (optional). See 'mu guide secrets'.
  sealed_input_modes   {NAME: "env" | "file"} — delivery per name
                       (optional, default env).
  sealed_outputs       {NAME: "scheme:path"} — destinations to capture
                       from $MU_SEALED_OUT_DIR/<NAME> (optional).
  kind                 BRICK classification: "relationship",
                       "interface", "component", "kit" (optional).
  implements           Interface this component satisfies (optional).

TARGET NAMING

  Targets are named "//path/name". Subdirectory targets are auto-prefixed:
  a target "mylib" in foo/mu.cue becomes "//foo/mylib".

CONFIG MERGING

  mu walks up to find the project root mu.cue, then recursively discovers
  mu.cue files in subdirectories. Targets from subdirectories are merged
  with paths rebased relative to the project root. Globs in sources are
  expanded. Hidden directories (.git, .claude, etc.) and testdata/ are skipped.

SECRETS POLICY

  secrets: writable_refs: ["pass:my-project/*"]

  Allow-list of glob patterns; sealed_outputs whose ref does not
  match is rejected at plan time. Omit the block to allow all
  writes; set to [] for explicit deny-all. See 'mu guide secrets'.

PREPROCESSOR

  For other config formats (YAML, TOML), define a preprocessor:

  preprocessor: {
      extension: ".yaml"
      command: ["yq", "-o", "json"]
  }

  mu pipes files with matching extension through the command before parsing.

CACHE CONFIG

  cache: {
      backends: [
          {type: "disk", path: "~/.mu/cache", max_size: "10GB"},
          {type: "oci", registry: "ghcr.io/org/cache"},
      ]
      write_through: true
      read_repair:   true
  }

SEE ALSO

  docs/cue-conventions.md     Authoring conventions, layout, embed.
