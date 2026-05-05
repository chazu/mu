mu guide plugin sops — secret provider (Mozilla SOPS)

Resolves and stores secrets from SOPS-encrypted files. The plugin does
not build or converge anything — it serves as a bidirectional secret
backend for mu's sealed_inputs and sealed_outputs.

SETUP

  Requires `sops` 3.7+ on PATH (we use the `set` subcommand introduced
  in 3.7). On macOS: `brew install sops`. On Linux: download from
  github.com/getsops/sops releases or your distro's package.

  Whatever credentials sops needs (gpg agent, AWS KMS, age keys, GCP
  KMS, …) must already be available to the running user. mu does not
  inject any sops-specific credentials.

  v0.1 does not auto-bootstrap empty encrypted files — the file has to
  exist before the first store_secret. Either commit a stub
  (`sops <file>` once, save with `{}` or an empty top-level), or seed
  it from your provisioning workflow.

USAGE IN mu.cue

  plugins: [{name: "sops", script: "plugins/sops"}]

REF GRAMMAR

  sops:<file>#<dotted.key>

  Examples:
    sops:secrets/prod.yaml#database.password
    sops:env/dev.json#aws.access_key_id
    sops:k8s/staging.enc.yaml#registry.credentials.docker_pass

  - <file> is resolved relative to the action's working directory
    (typically the project root).
  - <dotted.key> is split on "." and converted to sops's --extract
    syntax: `database.password` → `["database"]["password"]`.
  - Array indexing (e.g. `users[0].name`) is not supported in v0.1.
    Use a flatter layout or a different key naming scheme.

READING SECRETS (sealed_inputs)

  target: "//deploy/api"
  toolchain: "k8s"
  sealed_inputs: {
      DB_PASS:      "sops:secrets/prod.yaml#database.password"
      API_TOKEN:    "sops:secrets/prod.yaml#api.token"
  }

  Single-line scalars come back as their literal value (sops appends a
  trailing newline on extract; the plugin strips it). Multi-line
  scalars (private keys stored as YAML block scalars or escaped JSON
  strings) round-trip as written.

  There is no `raw:` prefix. Each sops value is a structured field —
  the first-line/full-content distinction `pass` needs doesn't apply.

WRITING SECRETS (sealed_outputs)

  Pair a target's sealed_outputs with the configured secret-write
  policy. Modes:
    create            Fail if the value already exists at this path.
    overwrite         Always set; create intermediate keys as needed.
    create_if_absent  No-op if the value exists; create otherwise.

  Example: route a generated DB password into prod.yaml.

    target: "//bootstrap/db-pass"
    toolchain: "secret-gen"
    config: {
        ref:        "sops:secrets/prod.yaml#database.password"
        derivation: ["openssl", "rand", "-base64", "24"]
    }
    sealed_output_modes: {VALUE: "create_if_absent"}

  Or capture a TF output via the terraform plugin's
  config.sealed_output_outputs and route it to a sops file:

    sealed_outputs: {DBPASS: "sops:secrets/rds.enc.yaml#master.password"}

WRITE POLICY

  In your project mu.cue, allow-list the sops refs you intend to write:

    secrets: writable_refs: [
        "sops:secrets/prod.yaml#*",
        "sops:secrets/rds.enc.yaml#master.*",
    ]

  Patterns are glob-matched against the full ref including the scheme.
  Without an entry the runner rejects sealed_output writes at plan time.

CAPABILITIES

  discover, resolve_secret, store_secret

CURRENT LIMITATIONS

  - Array indexing in key paths is not supported. Use object-only
    layouts in your sops files.
  - The plugin does not create new encrypted files. Stub files via
    `sops <file>` first, or commit an empty encrypted skeleton.
  - sops `set` rewrites the file. If multiple targets in one build
    write to the same file, races are not coordinated by the plugin —
    rely on mu's DAG to serialize them via deps if order matters.
