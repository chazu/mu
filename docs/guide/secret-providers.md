mu guide secret-providers — authoring secret-aware plugins

This is the plugin-author counterpart to 'mu guide secrets'. The user
guide tells target authors how to USE sealed_inputs / sealed_outputs;
this guide tells plugin authors how to IMPLEMENT them correctly.

Two roles a plugin can play, independently:

  1. SECRET PROVIDER — owns a ref scheme (e.g. "pass:") and serves
     resolve_secret and/or store_secret. Plugin providers: 'pass',
     'sops'. mu also ships a built-in 'env' scheme (env:NAME, read-only)
     that needs no plugin; a plugin explicitly named "env" overrides it.

  2. SECRET CONSUMER / EMITTER — an action plugin (k8s, terraform,
     remote-exec, …) that accepts sealed_inputs as runtime values and
     optionally redirects sensitive outputs through sealed_outputs.

Roles are orthogonal. A plugin can be a pure provider (pass), a pure
consumer (k8s), an emitter (terraform with sensitive outputs), or all
of the above.

────────────────────────────────────────────────────────────────────
ROLE 1 — IMPLEMENTING A SECRET PROVIDER
────────────────────────────────────────────────────────────────────

CAPABILITIES

  Declare what you support in discover.capabilities:

    "capabilities": ["discover", "resolve_secret", "store_secret"]

  A read-only provider declares only "resolve_secret"; a write-only
  provider declares only "store_secret". Most providers do both.

REF GRAMMAR

  Pick a scheme that matches your plugin name. mu strips the scheme
  before calling you — your handler sees the path only.

    User writes:   "pass:deploy/k8s/token"
    Plugin gets:   {"method": "resolve_secret", "secret_ref": "deploy/k8s/token"}

  Document the grammar in your GUIDE.md, including any sub-prefixes
  (e.g. "pass:raw:..." for full-content vs. first-line semantics).
  Treat the grammar as a stable contract — users encode refs into
  mu.cue files, and changes break their builds.

resolve_secret

  Request:   {"method": "resolve_secret", "secret_ref": "<path>"}
  Response:  {"value": "<resolved bytes>"}
  Error:     {"value": "", "error": "<reason>"}

  Return the value as a string. Binary secrets should be base64'd by
  the consumer, not the provider — keep the wire shape simple.

  The value MUST NOT be logged to stderr. Plugin stderr is captured
  in tagged ring buffers and surfaced by 'mu plugin info' / verbose
  builds. Treat any stderr write as public.

store_secret

  Request:   {"method": "store_secret",
              "secret_ref":   "<path>",
              "secret_value": "<bytes>",
              "secret_mode":  "create" | "overwrite" | "create_if_absent"}
  Response:  {} on success
  Error:     {"error": "<reason>"}

  Mode semantics (REQUIRED — runners depend on these being honored):

    create             Fail if the ref already exists.
    overwrite          Always set; create if missing.
    create_if_absent   No-op if exists, create if missing.
                       (This is what secret-gen relies on.)

  The mode comes from the action's sealed_output_modes — a field the
  plugin emits on its ActionSpec at plan time, not a target/mu.cue key —
  defaulting to "overwrite". Validate it and reject anything else
  explicitly.

  Like resolve_secret, never log the value. If your backend's CLI
  echoes it, redirect that output to /dev/null in your wrapper.

WRITE POLICY (writable_refs)

  The runner enforces secrets.writable_refs at plan time and again
  in the writer closure — your plugin does not need to filter. But
  the GUIDE.md SHOULD show users a representative writable_refs
  pattern for your scheme so they know what to allow.

────────────────────────────────────────────────────────────────────
ROLE 2 — CONSUMING SEALED INPUTS IN AN ACTION PLUGIN
────────────────────────────────────────────────────────────────────

WHAT YOU RECEIVE

  In a "plan" request you get the target's declared sealed_inputs
  and sealed_input_modes maps:

    target.sealed_inputs       NAME -> "scheme:path"   (still refs)
    target.sealed_input_modes  NAME -> "env" | "file"

  Refs are NOT resolved at plan time. They are resolved by the
  runner just before each action runs and delivered per-mode. Your
  plan output should pass them through unchanged on the ActionSpec:

    {"sealed_inputs":      {"SSH_PASS": "pass:hosts/foo"},
     "sealed_input_modes": {"SSH_PASS": "env"}}

  The runner then handles resolution + delivery; you only see the
  resolved value via $NAME (env mode) or as a path in $NAME (file
  mode) when your action runs.

DELIVERY MODE GUIDANCE

  - Short single-line secrets (passwords, tokens) → env mode.
  - Multi-line / binary / >4 KB secrets (SSH keys, kubeconfigs,
    GCP service-account JSON) → recommend 'file' in your GUIDE.md.
  - Sandbox-pinned (toolchain-built) actions reject file mode.
    Document this if your action runs sandboxed.

NETWORK + IMPURE

  Sealed inputs do not by themselves force impure. The action stays
  cache-eligible on (refs, modes) — refresh on rotation requires
  changing the ref or marking the action impure.

────────────────────────────────────────────────────────────────────
ROLE 3 — EMITTING SECRETS VIA SEALED OUTPUTS
────────────────────────────────────────────────────────────────────

If your action would otherwise produce sensitive material on stdout
or in a declared output file (a generated key, a bootstrap token,
sensitive Terraform outputs, kubectl-extracted secret data), route
it through sealed_outputs instead so the bytes never enter CAS.

PLAN-TIME

  Pass through the target's sealed_outputs (and sealed_output_modes)
  unchanged on each ActionSpec that produces them:

    {"id": "render-token",
     "command": ["./gen-token.sh"],
     "sealed_outputs":      {"TOKEN": "pass:bootstrap/token"},
     "sealed_output_modes": {"TOKEN": "create_if_absent"}}

  The runner sets $MU_SEALED_OUT_DIR before the action runs. Your
  command must write each named output as $MU_SEALED_OUT_DIR/<name>
  (NOT as a regular file output).

RUNTIME CONTRACT

  - $MU_SEALED_OUT_DIR is a per-action 0700 dir, removed after the
    action regardless of success. Files inside are 0600.
  - Missing files are an error if declared. Extra files are ignored.
  - Actions with non-empty sealed_outputs are FORCED IMPURE — the
    runner skips the cache so the store_secret side-effect always
    runs. Document this in your GUIDE.md if relevant.

────────────────────────────────────────────────────────────────────
WHAT TO DOCUMENT IN GUIDE.md
────────────────────────────────────────────────────────────────────

If your plugin participates in any role above, your GUIDE.md needs a
SECRETS section. Recommended subsections (drop any that don't apply):

  - Sealed inputs accepted: each NAME, what it's for, and supported
    delivery modes (env / file).
  - Sealed outputs emitted: each NAME, the expected ref scheme, and
    which sealed_output_modes are meaningful.
  - Ref grammar (providers only): full syntax, sub-prefixes, examples.
  - A worked writable_refs pattern users can paste into mu.cue.
  - Caveats: forced-impure actions, sandbox + file-mode incompat,
    rotation behavior.

See plugins/pass/GUIDE.md for a provider example, and
plugins/host/GUIDE.md / plugins/remote-exec/GUIDE.md for consumer
examples.

────────────────────────────────────────────────────────────────────
SEE ALSO
────────────────────────────────────────────────────────────────────

  mu guide secrets              User-facing reference.
  mu guide secret-gen           secret-gen toolchain (calls store_secret).
  mu guide protocol             Wire format for resolve_secret / store_secret.
  docs/secrets-write-policy.md  writable_refs allow-list.
  docs/sealed-input-delivery-modes.md   env vs file delivery details.
