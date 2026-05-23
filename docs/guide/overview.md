mu guide overview — what mu is, in 60 seconds

WHAT MU IS

  mu is a build/convergence orchestrator. You declare targets in
  mu.cue; mu resolves them into a DAG of actions, executes the DAG
  with parallel scheduling and content-addressed caching, and
  hands off observability to plugins.

  mu is not opinionated about what you build — Go binaries, OCI
  images, terraform-managed infrastructure, k8s manifests, secrets
  in a password store, and shell pipelines all live in the same
  graph and share the same cache.

THE MENTAL MODEL

  - mu.cue           declares targets (what you want)
  - plugins          translate targets into actions (how to get there)
  - the coordinator  resolves the cross-target action graph
  - the executor     runs the DAG, caches by content
  - the CAS          stores blobs and action results, keyed by sha256
  - sealed_inputs    resolve secrets for actions (read side)
  - sealed_outputs   capture secrets from actions (write side)
  - observe          plugins report current state for drift detection

  Each piece is replaceable. Plugins are external processes speaking
  NDJSON over stdin/stdout (see 'mu guide protocol'). Two toolchains
  are built in: 'shell' and 'secret-gen' (see their guides).

THE DAY-TO-DAY VERBS

  mu build <target>...      Plan + execute the target's action DAG.
  mu build --plan <target>  Show the planned actions, don't run them.
  mu observe <target>...    Ask each plugin to report current state.
  mu cache ls               List cached action results.
  mu plugin list            Show registered plugins.
  mu plugin info <name>     Show capabilities/metadata for one plugin.
  mu target list            Show targets defined in this project.

WHAT TO READ NEXT

  Authoring a project:    mu guide mu.cue → mu guide build
  Writing a plugin:       mu guide plugins → mu guide protocol
  Inline pith plugins:   mu guide pith-plugins
  Working with secrets:   mu guide secrets → mu guide secret-gen
  Drift / convergence:    mu guide observe → mu guide pudl
  Hermetic toolchains:    mu guide toolchains
  Caching/CAS internals:  mu guide cache

KEY DESIGN PRINCIPLES

  1. Everything that runs is an action with a deterministic cache key.
  2. Plugins are sensors and planners; they never mutate the graph
     after it's built.
  3. Secrets values never enter the cache, manifests, or logs.
     Refs and modes do — they're non-secret metadata.
  4. mu and pudl are decoupled. mu builds; pudl reasons about state.
  5. mu.cue is the single source of authoring truth.
