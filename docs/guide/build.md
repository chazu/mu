mu guide build — building targets

USAGE

  mu build [flags] <target>...

FLAGS

  --plan            Show planned actions without executing (dry run).
  --dry-run         Alias for --plan.
  --json            Output as JSON.
  --emit-manifest   Emit a build manifest to stdout (for pudl's ACUTE loop).
  --no-cache        Skip cache reads — rebuild everything.
  --jobs N          Max parallel actions (default: NumCPU).
  --config PATH     Path to mu.cue (default: discover by walking up).
  --verbose         Show plugin I/O.

EXAMPLES

  mu build //cmd/myapp                     Build a single target.
  mu build //cmd/myapp //lib/utils         Build multiple targets.
  mu build --plan //cmd/myapp              Preview the action DAG.
  mu build --emit-manifest //cmd/myapp     Build and emit manifest JSON.
  mu build --no-cache //cmd/myapp          Force full rebuild.
  mu build --jobs 4 //cmd/myapp            Limit parallelism.

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
