mu guide build — building targets

USAGE

  mu build [flags] <target>...

FLAGS

  --plan            Show planned actions without executing (dry run).
  --json            With --plan, emit the plan as JSON (requires --plan; use
                    --emit-manifest for a machine-readable build result).
  --emit-manifest   Emit a build manifest as JSON to stdout (for CI).
  --no-cache        Skip action-result reads; retain dependency CAS storage.
  --offline         Use local dependencies; disable Mu-owned remote access.
  --no-discover-cache
                    Force live plugin discovery instead of its digest cache.
  --jobs N          Max parallel actions (default: NumCPU).
  --config PATH     Path to mu.cue (default: discover by walking up).
  --verbose         Show plugin I/O.
  --publish         Publish each requested target's outputs as an artifact.
                    Wildcards (//pkg/...) are expanded; matched targets
                    with no outputs are skipped. A target named explicitly
                    with no outputs, or a publish that would push nothing,
                    fails before anything is pushed.
  --attach TYPE=PATH
                    Attach a referrer file to the published artifact
                    (repeatable; requires --publish).

EXAMPLES

  mu build //cmd/myapp                     Build a single target.
  mu build //cmd/myapp //lib/utils         Build multiple targets.
  mu build --plan //cmd/myapp              Preview the action DAG.
  mu build --plan --json //cmd/myapp       Emit plan schema v2 as JSON.
  mu build --emit-manifest //cmd/myapp     Build and emit manifest JSON.
  mu build --no-cache //cmd/myapp          Force full rebuild.
  mu build --jobs 4 //cmd/myapp            Limit parallelism.

JSON PLAN CONTRACT

  `mu build --plan --json` emits plan schema version 2. Its top-level fields
  are version, targets, plugins, actions, and summary. Plugin rows record
  resolved content digest, version, protocol version, and capabilities. Each
  action includes its ID,
  computed action_key, and every execution-affecting field: command or body,
  ewe_digest, input digests, outputs, dependencies, environment, sealed refs
  and modes, network/work-dir/project-root/impure/retry settings, toolchain digests, and
  sources. Empty optional fields may be omitted.

  An action whose inputs are produced by other actions ("{action:id}" refs or
  a dependency's output paths) has no action_key in the plan: its key depends
  on content that does not exist yet. Such actions list those input names in
  pending_inputs instead, and their digests in inputs are zero placeholders.

  Planning resolves file and toolchain digests but never secret values. Sealed
  provider refs and modes are non-secret plan metadata and are projected for
  inspection. Build manifests remain a separate schema and omit provider refs
  and values.

BUILD PIPELINE

  1. Bootstrap toolchains from scratch (if defined).
  2. Resolve plugins to CAS (hash scripts, fetch URLs).
  3. Start plugin processes and run discover.
  4. Resolve target dependency graph (topological order).
  5. Plan each target via its plugin (plugin emits action specs).
     Built-in toolchains 'shell' and 'secret-gen' bypass plugins.
  6. Validate strict sealed-routing claims (when requested) and merge action
     subgraphs into a unified DAG. No provider values are resolved while
     planning.
  7. Enforce secrets.writable_refs against any sealed_outputs in
     the graph; abort if any ref is not allowed.
  8. Execute DAG: topological sort, worker pool, per-action:
     - check cache (skip if impure or sealed_outputs declared)
     - resolve and inject that action's sealed_inputs (env or file mode)
     - mint $MU_SEALED_OUT_DIR if sealed_outputs declared
     - run command
     - capture sealed_outputs and route via store_secret
     - hash outputs into CAS, write action result entry

ACTION CACHING

  See 'mu guide cache' for the full cache-key contract. Summary:
  command + sorted input digests + env + network + impure +
  sealed-input refs/modes + sealed-output refs are hashed.
  Sealed-input/output VALUES are never hashed.
  Build manifests omit both sealed values and provider refs.

OTHER COMMANDS

  mu target list                List all targets in the project.
  mu target list --json         List targets as JSON.

ACTION ENVIRONMENT AND WORK DIRECTORY

  Pure actions default to an empty environment. Declare PATH and other
  required variables in action.env; use sealed_inputs for secret values.
  When a bare command has no PATH, Mu searches only /usr/bin:/bin for its
  initial executable. An explicit PATH controls lookup; ambient parent PATH
  cannot select another binary. The sandbox supplies its own toolchain PATH.

  Impure bare commands with no env declaration retain parent inheritance for
  direct host operations. Explicit env maps, including {}, replace inheritance.
  VM and sandbox execution never imports arbitrary parent variables. Any
  action may name host variables in env_passthrough; they are copied at
  execution time and only their names enter the cache key. Sealed inputs and
  MU_OUT / MU_SEALED_OUT_DIR are layered on top without dropping the rest.

  Coordinator actions carry project_root in plan-v2; cache identity uses only
  the work directory relative to it, so it does not depend on checkout path.
  Working-directory resolution permits internal relative symlinks and rejects
  external links. Execution reopens and pins the directory before cache or
  provider access; the child uses fchdir on its inherited descriptor. Relative
  restores, output collection, and sandbox source reads use that pinned root.
  A later symlink swap cannot redirect those operations. This native descriptor
  execution is supported on Linux and macOS. Bare commands remain intentional
  host commands, not a full filesystem sandbox; explicit absolute outputs retain
  their host-path contract.
