mu guide plugin pass — secret provider (password-store)

Resolves secrets from password-store (pass) for use as sealed inputs.
This plugin does not build or converge anything — it provides secrets
to other plugins at runtime.

SETUP

  Requires 'pass' (https://www.passwordstore.org/) installed and
  configured with a GPG key.

USAGE IN mu.json

  Register the plugin:

    {"name": "pass", "script": "plugins/pass/plugin.bb"}

  Reference secrets in targets via sealed_inputs:

    {
      "target": "//deploy/app",
      "toolchain": "k8s",
      "sources": ["deploy/*.yaml"],
      "sealed_inputs": {
        "KUBECONFIG_TOKEN": "pass:deploy/k8s-token",
        "AWS_SECRET_KEY": "pass:aws/secret-key"
      }
    }

HOW IT WORKS

  1. A target declares sealed_inputs with "pass:" prefix.
  2. Before execution (or observation), mu calls the pass plugin:
     {"method": "resolve_secret", "secret_ref": "deploy/k8s-token"}
  3. The plugin runs 'pass show deploy/k8s-token' and returns the
     first line of output as the secret value.
  4. mu injects the value as an environment variable during execution.

  Secrets are never stored in CAS, cache keys, logs, or manifests.

EXAMPLES

  Store a secret:
    pass insert deploy/k8s-token

  Use in a target:
    "sealed_inputs": {"TOKEN": "pass:deploy/k8s-token"}

  The environment variable TOKEN will contain the secret value at runtime.

CAPABILITIES

  discover, resolve_secret
