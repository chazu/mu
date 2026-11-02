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

TARGET FIELDS

  target               Target name. Convention: "//path/to/name".
  toolchain            Which toolchain handles this target. Built-in:
                       "shell", "secret-gen". Otherwise the name of a
                       registered plugin (e.g. "go", "file", "k8s").
  sources              Array of source file paths or globs ("src/**/*.go").
  deps                 Array of target names this depends on (optional).
                       Deps are always built, and finish, before this
                       target's actions start.
  config               Toolchain-specific config object (optional).
  sealed_inputs        {NAME: "scheme:path"} — secret refs to resolve
                       before exec (optional). See 'mu guide secrets'.
  sealed_input_modes   {NAME: "env" | "file"} — delivery per name
                       (optional, default env).
  sealed_outputs       {NAME: "scheme:path"} — destinations to capture
                       from $MU_SEALED_OUT_DIR/<NAME> (optional).
  sealed_output_modes  {NAME: "create" | "overwrite" |
                       "create_if_absent"} — store mode per output
                       (optional, default overwrite).

TARGET NAMING

  Targets are named "//path/name". Subdirectory targets are auto-prefixed:
  a target "mylib" in foo/mu.cue becomes "//foo/mylib". A subdirectory
  mu.cue may also use absolute names, but only under its own directory
  ("//foo", "//foo/...", "//foo:..."); anything else is a load error.

CONFIG MERGING

  mu walks up to find the project root mu.cue, then recursively discovers
  mu.cue files in subdirectories. Targets from subdirectories are merged
  with paths rebased relative to the project root. Globs in sources are
  expanded. Hidden directories (.git, .claude, etc.) and testdata/ are skipped.
  A subdirectory with its own cue.mod/ is a separate project and is not
  merged (nor is anything below it).

  Rules for subdirectory mu.cue files (each violation is a load error
  naming the file):

  - cache, publish and secrets are project-wide and may only be set in
    the root mu.cue.
  - A plugin or toolchain name may be declared in only one mu.cue across
    the project; a redeclaration names both files.
  - Plugin paths are rebased onto the project root: script always, and
    command[0] only when it starts with "./" or "../". Other command
    elements are passed through unchanged, so "sh -c '...a/b...'" is safe.

SECRETS POLICY

  secrets: writable_refs: ["pass:my-project/*"]

  Allow-list of glob patterns; sealed_outputs whose ref does not
  match is rejected at plan time. Omit the block to allow all
  writes; set to [] for explicit deny-all. See 'mu guide secrets'.

SEALED ROUTING

  Target-level sealed maps are the complete allow-list. Plan actions must
  claim each sealed name they use with the exact target ref and effective
  mode. Every declared input must be claimed by at least one action; every
  output by exactly one action. Undeclared claims, unused declarations,
  mode/ref changes, and ambiguous outputs fail during planning. If the plan
  has exactly one action and it makes no sealed claims, mu fills its claims
  from the target maps. Planning never resolves provider values; each
  claiming action resolves its inputs immediately before execution.

  The former sealed_routing field was removed (routing is always strict);
  setting it is a load error.

CACHE CONFIG

  cache: {
      path:   "~/.mu/cache"              // optional; local store (default shown)
      remote: "ghcr.io/org/mu-cache"     // optional; read-only remote cache
      push:   "ghcr.io/org/mu-cache"     // optional; mu cache push target
  }
  publish: repository: "ghcr.io/org"     // mu build --publish base

  Local disk plus at most one read-only remote. The old cache.backends,
  read_repair, and write_through fields are rejected. Every registry
  location is one "host/path" string with no scheme; the older split
  {registry, repository} forms of cache.push and publish are rejected.
  See `mu guide cache`.

SEE ALSO

  docs/cue-conventions.md     Authoring conventions, layout, embed.
