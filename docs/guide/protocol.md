mu guide protocol — the NDJSON plugin protocol

Plugins communicate with mu over stdin/stdout using NDJSON (newline-
delimited JSON). Each line is a complete JSON object.

  The Go representation of every message below lives in
  sdk/muplugin/types.go — that package is the canonical Go binding
  for this protocol. Plugin authors writing in Go should use
  sdk/muplugin (see 'mu guide sdk'); the wire format documented
  here is the contract for any other language.

LIFECYCLE

  1. mu starts the plugin process.
  2. mu sends {"method": "discover"} — plugin replies with metadata.
  3. mu sends plan/resolve_secret/store_secret requests as needed.
  4. mu closes stdin when done — plugin exits.

  Request deadlines cover waiting for another request, writing stdin, and
  reading stdout. Cancelling an active exchange terminates the plugin rather
  than risking pairing its late response with another request. Cancelling a
  queued request leaves the active request alone. On Unix, ordinary plugin
  descendants share a dedicated process group and are killed on cancellation.
  Close waits one second for an idle plugin to exit on EOF, then kills it and
  joins owned I/O. Descendants that deliberately leave the group are outside
  that cancellation guarantee; custom verbose writers must not block forever.

METHODS

  discover (required)
    Request:  {"method": "discover"}
    Response: {
      "name": "myplugin",
      "description": "one-line summary (optional)",
      "version": "0.1.0",
      "protocol_version": 1,
      "consumes": ["source:any"],
      "produces": ["binary"],
      "capabilities": ["discover", "plan"],
      "config_schema": {"output": {"type": "string"}}
    }

    Plugins must declare every supported method in `capabilities`.
    `plan` is required only for build plugins. The only methods are
    discover, plan, resolve_secret and store_secret: the old `observe`
    and `advise` methods were removed (mu is a build tool), and a plugin
    that still advertises `observe` fails discover with a removal error.

    Plan-capable plugins should declare `config_schema` (field name ->
    {"type", "description", "default", "required"}): mu validates each
    target's config against it before planning, so a typo or wrong type
    fails early. When a plan-capable plugin declares none and a target
    passes it config, mu prints one warning per plugin per run. Plugins
    that do not plan (secret providers) take no target config
    and need no schema.

  plan (optional; required for build plugins)
    Request:  {
      "method": "plan",
      "target": {"name": "//app", "toolchain": "go", "sources": [...], "config": {...}},
      "deps": [{"target": "//lib", "artifacts": {"binary": "lib/libfoo.a"}}],
      "toolchain_artifacts": {"bin/go": "sha256:..."}
    }
    Response: {
      "actions": [
        {
          "id": "compile",
          "command": ["go", "build", "-o", "app", "./cmd/app"],
          "inputs": {"main.go": "main.go"},
          "outputs": ["app"],
          "depends_on": [],
          "env": {"GOOS": "linux"},
          "network": false
        }
      ],
      "declared_outputs": {"binary": "app"}
    }

  resolve_secret (optional)
    Request:  {"method": "resolve_secret", "secret_ref": "deploy/token"}
    Response: {"value": "the-secret-value"}

  store_secret (optional, paired with resolve_secret)
    Request:  {"method": "store_secret",
               "secret_ref": "deploy/token",
               "secret_value": "...",
               "secret_mode": "create" | "overwrite" | "create_if_absent"}
    Response: {} on success, {"error": "..."} on failure.
    The plugin must implement all three modes; create_if_absent
    is a no-op if the ref already exists. Capability:
    "store_secret" must be present in discover.

TARGET INFO FIELDS

  Target info is sent in plan requests. Fields beyond
  the obvious name/toolchain/sources/config:

  sealed_inputs       map[NAME]ref — secret refs to resolve before exec.
  sealed_input_modes  map[NAME]mode — "env" (default) or "file".
                      file mode writes the value to a 0600 temp
                      file under a per-action sealed-input dir
                      and exports the path as $NAME.
  sealed_outputs      map[NAME]ref — destinations to capture
                      after exec. Action writes the value to
                      $MU_SEALED_OUT_DIR/<NAME>; mu routes
                      through the scheme's plugin's store_secret.
  sealed_output_modes map[NAME]mode — store mode per output.

  Target sealed maps are the allow-list. The plugin puts exact claims on the
  actions that need them: every target input is claimed at least once, and
  every target output exactly once. Mu validates claim names, refs, and
  effective modes during planning. The one exception: a plan with exactly one
  action that makes no sealed claims has its claims filled from the target
  maps. Provider values are still resolved only immediately before each
  action executes.

ACTION SPEC FIELDS

  id                  Unique within the subgraph.
  command             []string — the command to execute.
  inputs              map[name]path — input files (paths resolved to CAS digests).
  outputs             []string — declared output file paths.
  depends_on          []string — IDs of actions this depends on (within subgraph).
  env                 map[string]string — environment variables (optional).
  env_passthrough     []string — names of host environment variables copied
                      into the action at execution time, when set on the host
                      (optional). Only the names enter the cache key, never
                      the values: use this (not env) to forward HOME, PATH,
                      GOCACHE, SSH_AUTH_SOCK, ... without baking host values
                      into the plan. Applies to pure and impure actions; a
                      sandboxed action always gets the sandbox's own HOME and
                      TMPDIR, and a forwarded host path is usable there only
                      if it is visible (an input or a host_paths grant).
  sealed_inputs       map[NAME]ref — secret refs, resolved at runtime (optional).
  sealed_input_modes  map[NAME]mode — "env" (default) or "file" (optional).
  sealed_outputs      map[NAME]ref — destinations for $MU_SEALED_OUT_DIR/<NAME>
                      capture (optional). Forces impure.
  sealed_output_modes map[NAME]mode — store mode per output:
                      "create" | "overwrite" | "create_if_absent".
                      Default "overwrite".
  network             bool — allow network access (default false). Pure
                      actions run in the sandbox, where false is
                      kernel-enforced under Seatbelt and Linux namespaces
                      (not under copy isolation). Impure actions run on the
                      host, where it restricts nothing. See `mu guide sandbox`.
  work_dir            string — working directory relative to project root (optional).
  impure              bool — run on the host, outside the sandbox, and skip
                      the CAS cache (optional; forced true when
                      sealed_outputs is non-empty). Pure actions (the default)
                      run in the sandbox and are cached.
  toolchains          []string — names of `from: "scratch"` toolchains the
                      action needs (optional, pure actions only). Their files
                      are unpacked into the sandbox root (bin/ is on PATH; a
                      raw binary "<name>" lands at bin/<name>) and hashed into
                      the action key. An unknown name, or a toolchain on an
                      impure action, is a plan error. See `mu guide sandbox`.
  host_paths          []string — absolute, clean host paths (files or
                      directories) granted read+exec inside the sandbox, for
                      tools that cannot be scratch toolchains (optional).
                      Seatbelt allows each path (and its symlink-resolved
                      real path); Linux namespaces bind-mount it read-only at
                      the same path; copy isolation needs no grant. "/" is
                      refused. Identity in the action key: the path plus,
                      for a file, its content digest (taken at plan time);
                      for a directory, only the path — granting a directory
                      means trusting whatever it holds, so prefer granting
                      specific files or a scratch toolchain when the tool's
                      version must invalidate the cache.
  timeout_s           int — per-attempt wall-clock timeout in seconds (optional).
  retries             int — additional attempts on failure (network actions only).
  retry_backoff_ms    int — sleep between retries.

  Any other field is ignored, except the removed `body` and `ewe_source`,
  which fail the plan with "action bodies were removed" / "ewe actions were
  removed; emit a command instead".

ACTION ENVIRONMENT

  mu builds each action's environment in one place, in this order (later
  layers win):
    1. base — pure actions: env only (empty when absent), never the host.
       Impure bare commands with no env: the host environment; otherwise env.
    2. env_passthrough — each named host variable that is set.
    3. MU_OUT / MU_SEALED_OUT_DIR — per-execution runtime paths.
    4. sealed_inputs — env-mode values, file-mode paths. Secrets always win.
  A sandboxed action then gets HOME and TMPDIR set to the sandbox's own
  directories, and the sandbox bin/ prepended to PATH (/usr/bin:/bin when no
  PATH is declared).

INTER-ACTION REFERENCES

  Inputs can reference outputs of other actions in the same subgraph:
    "inputs": {"lib.a": "{action:compile-lib}"}
  The reference implies a depends_on edge to that action. Once it has run,
  and before the consumer's cache lookup, mu replaces the input's digest
  with a digest over the producer's declared outputs, so the consumer's
  action key changes whenever the producer's output content does.

CROSS-TARGET ARTIFACTS

  A plan request includes "deps", one entry per listed dependency:
    "deps": [{"target": "//lib/crypto",
              "artifacts": {"go_library": "lib/crypto/libcrypto.a"}}]
  The artifacts map comes from each dep's declared_outputs (artifact-type
  name → project-relative path). To consume one, declare the path as a
  normal input:
    "inputs": {"cryptolib": "lib/crypto/libcrypto.a"}
  mu plans every target before wiring inputs, so any input path that
  another target's action declares as an output is linked, whatever the
  planning order and whether or not that target is listed in "deps". mu
  defers the input digest and injects an implicit DependsOn edge to the
  producing action so the consumer runs after the producer. The deferred
  digest is filled with the produced file's digest before the consumer's
  cache lookup. Paths are compared after cleaning: outputs relative to the
  producer's work_dir, inputs relative to the project root ("./x" and "x"
  match). Two targets that read each other's outputs (directly or via
  "deps") are a cycle and fail planning.

  Every output path has one producer. Two targets declaring the same
  path (after cleaning) fail planning, naming both targets, both actions
  and the path. Within one target, two actions may declare the same path
  only when the plan orders them, through depends_on or an {action:id}
  input, directly or transitively (for example a generate step followed
  by an in-place rewrite); the last writer in that chain is the producer
  other targets wire to. Writers of one path with no dependency between
  them fail planning.

  Listing a target in "deps" also orders execution on its own: every
  action of the dependent with no predecessor in its own subgraph waits
  for every action of each dep with no successor in the dep's subgraph.
