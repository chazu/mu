mu guide secret-gen — built-in toolchain for minting and storing secrets

The 'secret-gen' toolchain is built into mu — no plugin registration
required. Use it to declaratively bootstrap a secret: run a
derivation command, capture its stdout, route it through a
store_secret-capable provider plugin (today: 'pass', 'sops').

This is the natural complement to sealed_inputs. Where sealed_inputs
consumes a secret that someone has put in the store out of band,
secret-gen lets you say "this secret should exist" with a derivation
that produces it on first build.

BASIC USAGE

  target: "//secrets/admin-pass"
  toolchain: "secret-gen"
  config: {
      ref:        "pass:registry/admin"
      derivation: ["openssl", "rand", "-base64", "24"]
      // mode defaults to "create_if_absent"
  }

CONFIG FIELDS

  ref         (required) Destination secret ref, of the form
              scheme:path. The scheme must resolve to a plugin
              declaring the store_secret capability.
  derivation  (required) []string — argv whose stdout becomes the
              stored value.
  mode        (optional) "create" | "overwrite" | "create_if_absent"
              (default).
  env         (optional) Extra env vars for the derivation.
  keep_trailing_newline  (optional, default false) If false, strip
              a single trailing newline from the derivation's
              stdout before storing (the right thing for openssl
              rand, uuidgen, etc.).

PICKING A MODE

  create_if_absent  Bootstrap. Re-running is a cheap no-op once
                    the entry exists. Default.
  overwrite         Rotation. Always set to the fresh value.
  create            Strict bootstrap. Fail if the entry already
                    exists (catches out-of-band seeding).

  If your derivation is non-deterministic (openssl rand, uuidgen),
  'overwrite' rotates the value on every build — almost never
  what you want. Stick with create_if_absent.

CACHING

  secret-gen actions are always impure. Caching would skip the
  store_secret side-effect, which would be wrong if the provider
  entry has been wiped. The derivation runs every build; under
  create_if_absent the wasted work is bounded to the derivation
  itself.

WORKED EXAMPLE — bootstrap zot's admin password

  plugins: [{name: "pass", script: "plugins/pass/plugin.bb"}]
  secrets: writable_refs: ["pass:registry/*"]

  targets: [
      {
          target:    "//secrets/zot-admin"
          toolchain: "secret-gen"
          sources: []
          config: {
              ref:        "pass:registry/admin"
              derivation: ["openssl", "rand", "-base64", "32"]
          }
      },
      {
          target:    "//zot/htpasswd"
          toolchain: "remote-exec"
          deps: ["//secrets/zot-admin"]
          sealed_inputs: ADMIN_PASS: "pass:registry/admin"
          // ... uses $ADMIN_PASS in its remote command
      },
  ]

  First build: zot-admin runs the derivation, mints a fresh
  password, stores it under pass:registry/admin. The downstream
  htpasswd target's sealed_inputs resolves the same ref and
  consumes the value.

  Subsequent builds: the derivation re-runs, but pass insert is
  short-circuited by create_if_absent. Downstream sealed_inputs
  reads the stable value.

COOKBOOK — common derivations

  All examples below assume mode: create_if_absent (the default) and
  scheme: pass:. Swap "pass:..." for "sops:<file>#<key>" to land in a
  SOPS-encrypted file instead.

  Random password (32 bytes, base64, ~43 chars):
      derivation: ["openssl", "rand", "-base64", "32"]

  Random password (URL-safe, no '+' or '/' or '='):
      derivation: ["sh", "-c",
                   "openssl rand 32 | base64 | tr -d '=+/' | head -c 43"]

  Hex token (32 bytes -> 64 hex chars, e.g. an API key):
      derivation: ["openssl", "rand", "-hex", "32"]

  Short numeric PIN (6 digits):
      derivation: ["sh", "-c",
                   "od -An -N3 -tu4 /dev/urandom | tr -d ' \\n' | cut -c1-6"]

  UUID v4 (e.g. tenant id, idempotency key):
      derivation: ["uuidgen"]                      // macOS / util-linux
      derivation: ["cat", "/proc/sys/kernel/random/uuid"]   // any Linux

  Bcrypt-hashed password (htpasswd line for a webserver):
      // env-supplied plaintext keeps it off the argv. Pair this with a
      // secret-gen target that produces RAW_PASS into pass:foo/raw.
      config: {
          ref:        "pass:registry/htpasswd-line"
          derivation: ["sh", "-c",
                       "htpasswd -nbBC 12 admin \"$RAW_PASS\""]
          env: {RAW_PASS: ""}    // runner overrides via sealed_inputs below
      }
      sealed_inputs:      {RAW_PASS: "pass:registry/raw-pass"}
      sealed_input_modes: {RAW_PASS: "env"}

  Diceware-style passphrase (requires 'diceware' or similar; example
  uses pwgen):
      derivation: ["pwgen", "-s", "-1", "32"]

  Argon2-hashed password (for systems that take pre-hashed values):
      derivation: ["sh", "-c",
                   "echo -n \"$RAW_PASS\" | argon2 \"$(openssl rand -hex 16)\" -id -t 3 -m 16 -p 4 -e"]
      sealed_inputs: {RAW_PASS: "pass:bootstrap/raw-pass"}

  TIPS

  - Wrap multi-step derivations in 'sh -c' and quote carefully — argv
    bypasses shell expansion otherwise.
  - keep_trailing_newline: false (the default) is right for tools that
    print "value\\n". Set to true only if your tool emits a value with
    a meaningful trailing newline (rare).
  - For binary secrets that don't survive being sent over a JSON
    string, base64-encode in the derivation and base64-decode on the
    consumer side. The wire protocol is text-only.
  - Two-output secrets (an SSH keypair, a TLS cert+key) don't fit
    secret-gen — use the dedicated keypair-gen plugin instead.

WHAT SECRET-GEN IS NOT

  - Not a generic "run-once" toolchain. It only handles the
    derive-then-store pattern.
  - Not a way to read a secret. Use sealed_inputs for that.
  - Not aware of the value. mu sees the bytes only briefly,
    in memory, between the side-channel read and store_secret.

SEE ALSO

  mu guide secrets         The full secrets reference.
  docs/secret-gen-toolchain.md   In-depth design notes.
