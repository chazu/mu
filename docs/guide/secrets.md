mu guide secrets — sealed inputs/outputs, secret resolution, write policy

mu has a symmetric secret system: actions can READ secrets via
sealed_inputs and WRITE secrets via sealed_outputs, routed through
provider plugins (e.g. 'pass', 'sops') plus a built-in 'env' scheme
(env:NAME reads $NAME from the environment, read-only, no plugin
required). Values never enter the cache, manifests, or logs; refs and
modes are non-secret metadata and are part of the cache key.

This guide is the canonical user-facing reference. Per-feature deep
dives and the plugin-author counterpart:
  mu guide secret-providers      Authoring secret-aware plugins
  docs/secret-gen-toolchain.md
  docs/sealed-input-delivery-modes.md
  docs/secrets-write-policy.md

────────────────────────────────────────────────────────────────────
READING SECRETS — sealed_inputs
────────────────────────────────────────────────────────────────────

DECLARATION

  target: "//deploy/app"
  toolchain: "k8s"
  sealed_inputs: {
      KUBECONFIG_TOKEN: "pass:deploy/k8s-token"
      AWS_SECRET_KEY:   "pass:aws/secret-key"
  }

  Format: NAME: "scheme:path". The scheme prefix selects the plugin
  ("pass" → the pass plugin). The path is sent to that plugin's
  resolve_secret method.

RESOLUTION FLOW

  1. Planning records sealed_inputs but does not resolve them.
  2. Before execution, mu sends each plugin:
       {"method": "resolve_secret", "secret_ref": "deploy/k8s-token"}
  3. The plugin returns {"value": "..."}.
  4. mu delivers the value to the action per its delivery mode.

DELIVERY MODES — sealed_input_modes

  By default, the resolved value is exported as the env var $NAME.
  Set sealed_input_modes to change that per-name:

  sealed_inputs:      SSH_KEY: "pass:raw:hosts/dalian/key"
  sealed_input_modes: SSH_KEY: "file"

  Modes:
    env  (default)  Value exported as $NAME.
    file            Value written to a 0600 temp file under a
                    per-action sealed-input directory; $NAME holds
                    the path. The dir is removed when the action
                    exits regardless of success.

  Sandbox-mode (toolchain-pinned) actions reject file mode because
  the temp file lives outside the sandbox view. shell, secret-gen,
  remote-exec, remote-file all run bare and support file mode.

THE PASS:RAW PREFIX

  By default, "pass:foo/bar" returns the FIRST LINE of 'pass show
  foo/bar' (the conventional "password" line). For multi-line
  secrets — SSH keys, certs, JSON blobs — use the raw: prefix
  inside the ref to get the full content, with trailing newlines
  trimmed:

    sealed_inputs: SSH_KEY: "pass:raw:hosts/dalian/key"

────────────────────────────────────────────────────────────────────
WRITING SECRETS — sealed_outputs
────────────────────────────────────────────────────────────────────

A target can capture an action's emitted value into a secret store.
The action writes the value to a file under $MU_SEALED_OUT_DIR; mu
reads it on success and routes through the provider's store_secret.

DECLARATION

  target: "//secrets/admin-pass"
  toolchain: "shell"
  sealed_outputs: ADMIN_PASS: "pass:registry/admin"
  config: {
      command: ["sh", "-c", `openssl rand -base64 24 > "$MU_SEALED_OUT_DIR/ADMIN_PASS"`]
  }

PROPERTIES

  - The value is read from $MU_SEALED_OUT_DIR/<NAME> after the
    action exits (success only).
  - Routing failures abort the action.
  - Actions with sealed_outputs are forced impure — caching would
    skip the store side-effect.
  - Values never appear in stdout, the action cache, or manifests.

For a higher-level wrapper that handles the common
"derive-once-and-store" pattern, see 'mu guide secret-gen'. For
two-correlated-output secrets that don't fit secret-gen's single-
stdout shape (SSH keypairs, TLS keypairs), use the keypair-gen
plugin — see plugins/keypair-gen/GUIDE.md.

────────────────────────────────────────────────────────────────────
WRITE-POLICY ALLOW-LIST — secrets.writable_refs
────────────────────────────────────────────────────────────────────

By default any plugin with the store_secret capability can write
to any ref under its scheme. To bound the blast radius, declare a
project-level allow-list at the top of mu.cue:

  secrets: {
      writable_refs: [
          "pass:registry/*",
          "pass:loosh/*",
      ]
  }

  Patterns use Go's path.Match: '*' matches a single path segment
  (does NOT span '/'); literal text matches itself. Multiple
  patterns are OR'd.

THREE STATES

  secrets block omitted             No allow-list (writes unrestricted).
  writable_refs: [...patterns]      Strict allow-list.
  writable_refs: []                 Explicit deny-all (lockdown).

ENFORCEMENT

  Plan time: every sealed_output ref in the graph is checked. A
  forbidden ref aborts Plan() before the provider manager starts.
  Write time: the SealedOutputWriter closure re-checks defensively
  before calling store_secret.

────────────────────────────────────────────────────────────────────
SECURITY MODEL
────────────────────────────────────────────────────────────────────

VALUES never appear in:
  - the CAS or action cache
  - build manifests
  - stdout / stderr captured by the cache layer
  - verbose plugin I/O logs
  - process tables (file mode keeps multi-line secrets out of env)

REFS and MODES are part of the cache key. Changing
"pass:foo/v1" → "pass:foo/v2" or env → file invalidates the
cache, because both can change observable behavior.

The allow-list bounds writes; reads are bounded by what mu.cue
explicitly references. mu cannot stop a target from shelling out
to 'pass insert' directly — only writes routed through
sealed_outputs go through store_secret.

────────────────────────────────────────────────────────────────────
THE PASS PLUGIN
────────────────────────────────────────────────────────────────────

The bundled pass plugin (plugins/pass/plugin.bb, v0.3.0) implements:
  discover, resolve_secret, store_secret

Register it:

  plugins: [{name: "pass", script: "plugins/pass/plugin.bb"}]

Resolution: "pass:foo/bar" → 'pass show foo/bar' (first line).
            "pass:raw:foo/bar" → full content (newline-trimmed).
Storage:    'pass insert -m -f' under the configured mode.
            Modes: create | overwrite | create_if_absent.

Run 'mu guide plugin pass' for the full plugin guide.

────────────────────────────────────────────────────────────────────
WRITING A SECRET PROVIDER PLUGIN
────────────────────────────────────────────────────────────────────

A provider plugin is a normal NDJSON plugin (see 'mu guide protocol')
that adds two methods to its capability list:

  capabilities: ["discover", "resolve_secret", "store_secret"]

resolve_secret request:
  {"method": "resolve_secret", "secret_ref": "deploy/token"}
  reply: {"value": "..."}

store_secret request:
  {"method": "store_secret",
   "secret_ref": "deploy/token",
   "secret_value": "...",
   "secret_mode": "create_if_absent"}
  reply: {} or {"error": "..."}

The plugin is responsible for:
  - Implementing the three modes (create, overwrite, create_if_absent).
  - Returning empty success on no-op (create_if_absent for an
    existing ref).
  - Never logging the resolved or stored value.
