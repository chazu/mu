mu guide plugin envsecret — resolve secrets from environment variables

A minimal, read-only secret provider. A sealed_input ref of the form
`env:NAME` resolves to the value of the `NAME` environment variable in the
plugin process. Intended for local development, CI, and demos where a full
keyring (pass/sops) is unavailable or unwanted — it lets an existing
environment variable flow through mu's real sealed-input machinery.

It is deliberately read-only: `store_secret` returns an error, so the plugin
advertises only `resolve_secret`. Use pass or sops for anything that must
persist a secret.

SETUP

  No external dependencies. Build with the bundled target (`go build`), or
  reference the compiled binary directly.

USAGE IN mu.cue

  plugins: [
      {name: "env", command: ["./plugins/envsecret/envsecret"]},
  ]

  targets: [{
      target: "//inventory/gitlab-repos"
      sealed_inputs: {GITLAB_TOKEN: "env:GITLAB_TOKEN"}
      sealed_input_modes: {GITLAB_TOKEN: "env"}
      plan: [
          {id: "fetch", body: ["'GITLAB_TOKEN", "secret/get", /* ... */], outputs: ["repos.json"], network: true, impure: true},
          "action/emit",
      ]
  }]

REF GRAMMAR

  env:NAME   ->  the value of $NAME in the plugin's environment.
                 Errors if NAME is unset (fail loud — no silent empty value).

SECRETS

  - resolve_secret only. The scheme name is "env" (the plugin's name in
    plugins[]); the ref path is the variable name.
  - Pairs naturally with the pith `secret/get` word: a sealed_input named
    GITLAB_TOKEN resolved via `env:GITLAB_TOKEN` is read in a pith body with
    `["'GITLAB_TOKEN", "secret/get"]` and stays taint-tracked thereafter.

SECURITY NOTE

  The plugin can read ANY environment variable visible to the mu process, so
  the bound on what it can resolve is whatever you declare in sealed_inputs —
  mu only resolves refs the config names. Do not use `env:` to pull a variable
  the target should not see; prefer pass/sops with an explicit allow-list for
  anything sensitive in shared environments.

TROUBLESHOOTING

  "environment variable %q is not set" — the named var is absent from the mu
  process environment. Export it before `mu build`, or switch to a keyring
  provider.
