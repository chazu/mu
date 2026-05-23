mu guide plugin keypair-gen — EC keypair generator (sealed outputs only)

(Implemented in Go using sdk/muplugin; the original Babashka version
 at plugin.bb remains in-tree for one release cycle. See 'mu guide sdk'.)

Generates an EC keypair (ed25519 or ecdsa P-256/P-384/P-521) and routes
both halves through sealed_outputs into a secret backend (pass / sops /
…). The plugin never writes the keys to disk persistently — they live
only in the per-action MU_SEALED_OUT_DIR (0700, removed on exit) until
the runner stores them via the configured provider's store_secret.

Pairs naturally with secret-gen: secret-gen handles single-stdout-value
secrets (passwords, tokens); keypair-gen handles the two-correlated-
output case that secret-gen can't.

SETUP

  Requires `ssh-keygen` on PATH (part of OpenSSH; preinstalled on every
  modern Linux/macOS). No other dependencies.

USAGE IN mu.cue

  plugins: [{name: "keypair-gen", script: "plugins/keypair-gen"}]

  // bootstrap a deploy keypair, store both halves under pass
  targets: [{
      target:    "//bootstrap/deploy-keypair"
      toolchain: "keypair-gen"
      config: {
          type:    "ed25519"
          comment: "deploy@example.com"
      }
      sealed_outputs: {
          PRIVATE: "pass:raw:deploy/key"
          PUBLIC:  "pass:deploy/key.pub"
      }
      sealed_output_modes: {
          PRIVATE: "create_if_absent"
          PUBLIC:  "create_if_absent"
      }
  }]

CONFIG FIELDS

  type    "ed25519" (default) or "ecdsa".
  curve   For type:"ecdsa", one of "P-256" (default), "P-384", "P-521".
          Ignored for ed25519.
  comment ssh-keygen -C value. Defaults to the target name. Used by
          tooling to identify the key; not security-relevant.

SEALED OUTPUTS — required, two named keys

  This plugin's whole job is sealed outputs. The target MUST declare
  exactly two keys named PRIVATE and PUBLIC:

    sealed_outputs: {
        PRIVATE: "pass:raw:..."   // use raw: so the full multi-line key survives reads
        PUBLIC:  "pass:..."
    }

  Anything else is a plan-time error. Both refs must appear (or be
  glob-matched) in secrets.writable_refs in the project mu.cue.

  Use sealed_output_modes: "create_if_absent" if you want bootstrap
  semantics — re-running the target after the first build is a no-op
  rather than rotating the key. Use "overwrite" for forced rotation.

CONSUMING THE KEY

  Downstream targets read each half via sealed_inputs:

    target: "//deploy/host"
    toolchain: "remote-exec"
    deps: ["//bootstrap/deploy-keypair"]   // ensures the key exists first
    sealed_inputs:      {SSH_KEY: "pass:raw:deploy/key"}
    sealed_input_modes: {SSH_KEY: "file"}   // multi-line; deliver as 0600 path
    config: {
        host:    "host.example.com"
        command: ["ssh", "-i", "$SSH_KEY", "user@host", "uptime"]
    }

  Use the "raw:" prefix on the private-key read so newlines survive.
  Use sealed_input_modes: "file" so $SSH_KEY is a path, not a literal
  containing embedded newlines (env vars and multi-line strings don't
  always mix cleanly across shells).

  The public key, as a single line, is fine in env mode:

    sealed_inputs: {AUTHORIZED_KEY: "pass:deploy/key.pub"}

ACTIONS GENERATED

  generate  Calls ssh-keygen (no passphrase, comment = config.comment)
            in a per-action 0700 tmp dir, moves both files to
            $MU_SEALED_OUT_DIR/{PRIVATE,PUBLIC} (0600), then cleans up
            via trap. Carries the target's sealed_outputs and
            sealed_output_modes so the runner stores via store_secret.

  Network: false. Impure: forced true (sealed_outputs side-effect).

CAPABILITIES

  discover, plan
