mu guide plugin doppler — secret provider (Doppler, read-only)

Resolves secrets from Doppler (https://doppler.com) through the doppler
CLI. The plugin does not build or converge anything, and it cannot write:
it serves only resolve_secret for mu's sealed_inputs.

SETUP

  Requires the doppler CLI (3.x) on PATH, logged in with `doppler login`
  for a scope that covers the project root. The plugin never reads a token
  from the environment: it passes --no-read-env, so DOPPLER_TOKEN,
  DOPPLER_API_HOST and the other DOPPLER_* variables are ignored.

USAGE IN mu.cue

  plugins: [{name: "doppler", script: "plugins/doppler/plugin.bb"}]

REF GRAMMAR

  doppler:<project>/<config>/<NAME>

  Examples:
    doppler:hosts/prd_dalian/SSH_PASS
    doppler:hosts/prd/TAILNET

  - <project> and <config> are Doppler slugs: lowercase letters, digits,
    '-' and '_', starting with a letter or digit.
  - <NAME> is a secret name: letters, digits and '_', not starting with a
    digit.
  - Anything else is rejected before the CLI runs.

  The value is the secret's computed value (references expanded), byte for
  byte: no trimming, no first-line rule. A value seeded from `pass show`
  keeps the trailing newline that pass's "raw:" refs strip.

READING SECRETS (sealed_inputs)

    target: "//omniroute/env"
    toolchain: "remote-exec"
    sealed_inputs: {
        SSH_PASS:   "doppler:hosts/prd_dalian/SSH_PASS"
        JWT_SECRET: "doppler:hosts/prd_dalian/OMNIROUTE_JWT_SECRET"
    }

  Use file delivery (sealed_input_modes) for multi-line values such as SSH
  private keys.

FETCHING AND CACHING

  The first ref to a (project, config) runs one
    doppler secrets download --no-file --format json --no-fallback ...
  and keeps the result in the plugin's memory until mu closes its stdin.
  Every other ref to that config is served from memory. A failed download
  is remembered too, so 30 refs to a broken config cost one CLI call.

  The CLI gets two attempts of 10s each, and the plugin kills it after 25s,
  inside mu's 30s resolve_secret deadline.

WHAT IT WON'T DO

  - Write to disk. --no-fallback disables the CLI's encrypted fallback
    file, and --no-check-version stops the update check that writes into
    ~/.doppler.
  - Work offline. Without a fallback file there is nothing to read when
    Doppler is unreachable; the build fails with the CLI's error.
  - Write to stderr, or put a value in an error. Errors carry only the ref,
    the names involved and the CLI's own "Doppler Error:" line, with
    anything shaped like a Doppler token (dp.*) redacted.
  - Return a broken reference. If a referenced secret is renamed, deleted
    or not readable, Doppler leaves the literal "${...}" in the value. The
    plugin turns that into an error rather than delivering the string.
  - store_secret. Requests get an error.

CAPABILITIES

  discover, resolve_secret

TESTS

  bb plugins/doppler/test/plugin_test.bb

  The tests run the plugin against test/doppler, a fake CLI, and fail if a
  canary value shows up in an error, a non-value field or stderr. One test
  waits out the 25s CLI deadline.
