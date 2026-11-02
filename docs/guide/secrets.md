mu guide secrets — sealed inputs/outputs, secret resolution, write policy

mu has a symmetric secret system: actions can READ secrets via
sealed_inputs and WRITE secrets via sealed_outputs, routed through
provider plugins (e.g. 'pass', 'sops') plus a built-in 'env' scheme
(env:NAME reads $NAME from the environment, read-only, no plugin
required). Values never enter the cache, manifests, or logs. Provider refs are
also omitted from manifests; refs and modes remain non-secret planning metadata
and are part of the cache key.

This guide is the canonical user-facing reference. Related topics:
  mu guide secret-gen            Built-in toolchain that mints secrets
  mu guide secret-providers      Authoring secret-aware plugins

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
    env  (default)  Value exported as $NAME. An unset or empty mode
                    means env.
    file            Value written to a 0600 file named NAME in a
                    per-action directory (mu-sealed-in-* under
                    $TMPDIR, mode 0700); $NAME holds the path. The
                    directory is removed when the action exits,
                    whether or not it succeeded.
  Any other mode fails the action.

  Why file mode: multi-line secrets (SSH keys, x509 certs, JSON
  service-account blobs, kubeconfigs) are awkward as env vars, and
  many tools want a path rather than a value ('ssh -i <path>',
  'kubectl --kubeconfig <path>', 'gcloud ... --key-file <path>').
  Env vars are also visible in 'ps -E' and /proc/<pid>/environ for
  the action and every child it forks; file mode keeps the value out
  of the process table.

  Example: loading an SSH key into ssh-agent.

    env mode (value in the environment, piped by hand):
      config: command: ["bash", "-c", "printf '%s\\n' \"$SSH_KEY\" | ssh-add -"]
      sealed_inputs: SSH_KEY: "pass:raw:hosts/dalian/key"

    file mode ($SSH_KEY is a path to a 0600 file mu deletes after
    the action):
      config: command: ["bash", "-c", "ssh-add \"$SSH_KEY\""]
      sealed_inputs:      SSH_KEY: "pass:raw:hosts/dalian/key"
      sealed_input_modes: SSH_KEY: "file"

  Which to use: env for short tokens and passwords; file for SSH
  keys, kubeconfigs, certificates, JSON blobs, anything multi-line,
  and anything a CLI wants as a --key-file style path. There is no
  stdin mode: to pipe a secret into a command ('ssh-add -',
  'pass insert -m'), read it from a file-mode path or printf the
  env-mode value into the pipe.

  Sandbox-mode (toolchain-pinned) actions reject file mode at exec
  time because the temp file lives outside the sandbox view; env mode
  works in both. shell, secret-gen, remote-exec, remote-file, and
  plugin actions that pin no toolchain run bare and support file
  mode.

  The mode is part of the cache key (see SECURITY MODEL), and under
  strict routing the action that consumes a name must claim the same
  effective mode as the target declares (see ACTION ROUTING).

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
to any ref under its scheme, so a typo in a secret-gen target or a
bad plugin update could clobber any entry in your store. To bound
the blast radius, declare a project-level allow-list at the top of
mu.cue:

  secrets: {
      writable_refs: [
          "pass:registry/*",
          "pass:loosh/*",
      ]
  }

PATTERNS

  Patterns are matched against the FULL ref, scheme included
  ("pass:registry/admin"), using Go's path.Match. Multiple patterns
  are OR'd.
    *               any run of characters except '/'
    ?, [a-z], [!a]  single-character matches and classes
    literal text    itself
  '*' does NOT span '/', and '**' is not supported: list each depth
  you allow.

    writable_refs: [
        "pass:registry/*",            // pass:registry/admin
        "pass:registry/*/htpasswd",   // pass:registry/zone-a/htpasswd
    ]

  One list covers every write-capable scheme; there is no per-plugin
  scoping. With several (pass + sops), include each scheme prefix.

THREE STATES

  secrets block omitted             No allow-list (writes unrestricted).
  writable_refs: [...patterns]      Strict allow-list.
  writable_refs: []                 Explicit deny-all (lockdown).

ENFORCEMENT

  Plan time: every sealed_output ref in the graph is checked. A
  forbidden ref aborts Plan() before the provider manager starts,
  so no provider ever sees the write:
    coordinator: action "//secrets/personal:gen": sealed_output ref
    "pass:personal/all-the-things" is not allowed by
    secrets.writable_refs (2 pattern(s))
  Write time: the SealedOutputWriter closure re-checks defensively
  before calling store_secret, so a write that bypassed planning
  still fails closed.

  The policy composes with routing: routing proves which action owns
  each declared output; writable_refs bounds where it may store it.

  It does not gate reads (resolve_secret) or writes a target makes by
  running the backend's CLI itself; see SECURITY MODEL.

SUGGESTED DEFAULTS

  Scope the list to the project's own namespace:
    secrets: writable_refs: ["pass:<project>/*", "pass:<project>/*/*"]
  Stores shared between people and machines should pin exact refs:
    secrets: writable_refs: ["pass:registry/admin", "pass:registry/htpasswd"]

────────────────────────────────────────────────────────────────────
ACTION ROUTING — target maps are the allow-list
────────────────────────────────────────────────────────────────────

A target's sealed_inputs / sealed_outputs (and their modes) are the
complete set of secrets its actions may touch. Each action claims the
names it uses, with the exact target-level ref and effective mode:

  - every action claim must match a declared name, ref, and mode
  - every declared input must be claimed by at least one action
  - every declared output must be claimed by exactly one action

Undeclared claims, unused declarations, ref or mode changes, and
outputs with zero or several writers fail during planning, before any
secret is resolved.

One convenience: when a target declares sealed maps and its plan has
exactly ONE action that makes no sealed claims, mu fills that action's
claims from the target maps. This covers simple single-action targets
(shell, most plugins). Multi-action plans must claim explicitly.
secret-gen takes its destination from the target's single
sealed_outputs entry, e.g. sealed_outputs: {VALUE: <ref>}; its mode
defaults to create_if_absent rather than overwrite (see
'mu guide secret-gen').

Routing is always strict. The old `sealed_routing` field was removed;
setting it is a load error — delete it.

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

The bundled pass plugin (plugins/pass, a Go plugin, v0.3.0) implements:
  discover, resolve_secret, store_secret

Build its binary, then register the directory:

  go build -trimpath -o plugins/pass/plugin ./plugins/pass
  plugins: [{name: "pass", script: "plugins/pass"}]

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
