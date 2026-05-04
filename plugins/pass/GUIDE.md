mu guide plugin pass — secret provider (password-store)

Resolves and stores secrets from password-store (pass). The plugin
does not build or converge anything — it serves as the bidirectional
secret backend for mu's sealed_inputs and sealed_outputs.

SETUP

  Requires 'pass' (https://www.passwordstore.org/) installed and
  configured with a GPG key.

USAGE IN mu.cue

  Register the plugin:

    plugins: [{name: "pass", script: "plugins/pass/plugin.bb"}]

READING SECRETS (sealed_inputs)

  Reference secrets via the "pass:" scheme:

    target: "//deploy/app"
    toolchain: "k8s"
    sealed_inputs: {
        KUBECONFIG_TOKEN: "pass:deploy/k8s-token"
        AWS_SECRET_KEY:   "pass:aws/secret-key"
    }

  By default, only the first line of `pass show` is returned (suitable
  for typical passwords). For multi-line secrets — SSH private keys,
  certificates, JSON service-account blobs — use the "raw:" prefix
  inside the ref to get the full content (trailing newlines trimmed):

    sealed_inputs: SSH_KEY: "pass:raw:hosts/dalian/key"

WRITING SECRETS (sealed_outputs)

  A target may declare sealed_outputs to capture an action's emitted
  value into pass. The action writes the value to a file under the
  per-action $MU_SEALED_OUT_DIR; mu reads it on success and stores it
  via this plugin's store_secret. The value never lands in stdout,
  the action cache, or build manifests.

    target: "//secrets/admin-pass"
    toolchain: "shell"
    sealed_outputs: ADMIN_PASS: "pass:registry/admin"
    config: {
        command: ["sh", "-c", "openssl rand -base64 24 > \"$MU_SEALED_OUT_DIR/ADMIN_PASS\""]
        impure: true
    }

  Actions with sealed_outputs are always treated as impure — caching
  would skip the store side-effect.

CAPABILITIES

  discover, resolve_secret, store_secret

PROTOCOL NOTES

  store_secret accepts a "secret_mode" of "create" (fail if exists),
  "overwrite" (always set), or "create_if_absent" (no-op if exists).
  The runner currently calls store_secret with mode "overwrite".
  The "raw:" prefix is meaningful only for resolve_secret; on write
  it is ignored because there is no first-line/full-content
  distinction at insert time.
