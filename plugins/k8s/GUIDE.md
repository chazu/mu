mu guide plugin k8s — Kubernetes convergence plugin

Applies Kubernetes manifests and detects drift between desired and live state.

USAGE IN mu.cue

  targets: [{
    target: "//deploy/myapp"
    toolchain: "k8s"
    sources: ["deploy/*.yaml"]
    config: {
      namespace: "production"
      context: "my-cluster"
      server_side: true
    }
  }]

CONFIG FIELDS

  namespace             Kubernetes namespace.
  context               kubectl context name.
  kubeconfig            Path to kubeconfig file (default: ~/.kube/config).
  server_side           Use server-side apply (default: true).
  prune                 Prune resources not in manifest (default: false).
  dry_run               Run kubectl --dry-run=server (default: false).
  ignore_paths          List of dot-separated field paths to ignore in drift
                        detection (e.g. ["metadata.annotations.kubectl"]).
  inventory             Optional live-cluster inventory mode. Set `kinds` to a
                        non-empty list of kubectl resource kinds; use
                        `namespace` for one namespace or `all_namespaces: true`
                        for cluster-wide inventory. Inventory mode does not
                        require source manifests.
  sealed_output_secrets Capture-mode map: sealed_output NAME ->
                        {namespace, secret, key}. See "Sealed outputs" below.

EXAMPLES

  Apply manifests:
    {"namespace": "default", "context": "minikube"}

  Server-side apply with pruning:
    {"namespace": "prod", "server_side": true, "prune": true}

  Dry-run only:
    {"namespace": "staging", "dry_run": true}

  Inventory namespaced workloads:
    {"inventory": {"kinds": ["pods", "deployments"], "namespace": "production"}}

  Inventory cluster-wide services:
    {"inventory": {"kinds": ["services"], "all_namespaces": true}}

OBSERVATION (DRIFT DETECTION)

  mu observe //deploy/myapp

  The plugin compares desired state (from source manifests) against live
  state (from kubectl get). It strips server-managed fields like:
  - metadata.managedFields, resourceVersion, uid, creationTimestamp
  - generation, selfLink, status

  It projects live state down to only the keys present in the desired
  state, then reports differences as dotted-path diffs.

  Manifest drift records describe the desired resource comparison. Inventory
  records contain the live Kubernetes object and include _schema
  "k8s.resource" so PUDL can route them to its open Kubernetes resource
  envelope. The object kind remains in the `kind` field, allowing a project to
  add a stricter kind-specific CUE schema later.

ACTIONS GENERATED

  apply           Applies manifests with kubectl apply. Requires network.
  fetch-secrets   Reads each sealed_output_secrets entry via
                  `kubectl get secret -n NS NAME -o jsonpath="{.data.KEY}"`,
                  base64-decodes, and writes to $MU_SEALED_OUT_DIR/NAME
                  (chmod 0600). Depends on apply. Only generated when
                  sealed_outputs is set.

All actions are impure. Sealed inputs declared at target-level are
forwarded to every action.

SEALED INPUTS

  Forward kubeconfig and any other secrets via target.sealed_inputs.
  Single-line tokens use env delivery (the default):

    "sealed_inputs": {"K8S_TOKEN": "pass:k8s/prod/svc-account"}

  Kubeconfig and other multi-line secrets should declare file mode so
  the bytes are written to a 0600 temp file and $NAME holds the path:

    "sealed_inputs":      {"KUBECONFIG": "pass:raw:k8s/prod/kubeconfig"},
    "sealed_input_modes": {"KUBECONFIG": "file"}

  kubectl reads $KUBECONFIG natively (path env var), so no command-line
  changes are needed. For other tools consuming the file, refer to it
  as "$NAME".

SEALED OUTPUTS (cluster Secret -> secret backend)

  Capture values from a cluster-side Secret resource (a generated
  database password, a ServiceAccount token, an external operator's
  output) into your secret backend without round-tripping through
  CAS or stdout.

  Pair target.sealed_outputs with config.sealed_output_secrets, mapping
  each sealed-output NAME to a {namespace, secret, key} triple:

    targets: [{
      target: "//deploy/db-creds-capture"
      toolchain: "k8s"
      sources: ["deploy/db.yaml"]
      config: {
        context: "prod"
        sealed_output_secrets: DBPASS: {
          namespace: "db"
          secret: "creds"
          key: "password"
        }
      }
      sealed_outputs: DBPASS: "pass:rds/master-password"
      sealed_output_modes: DBPASS: "create_if_absent"
    }]

  After the apply action succeeds, fetch-secrets runs:

    kubectl --context prod get secret -n db creds \
      -o jsonpath="{.data.password}" | base64 -d \
      > $MU_SEALED_OUT_DIR/DBPASS

  and the runner stores the decoded bytes via the configured provider.

  Constraints
    - Keys in sealed_output_secrets MUST exactly match sealed_outputs.
      Each value MUST contain namespace, secret, and key.
    - The matching ref must appear in secrets.writable_refs in the
      project mu.cue.
    - The Secret must exist by the time fetch-secrets runs — typically
      because apply just created it. For Secrets created by an operator
      that takes time to populate, use a separate target with a
      remote-exec / shell-toolchain `kubectl wait` step in between.
    - Actions with sealed_outputs are forced impure — the cache is
      skipped so store_secret always runs.

CAPABILITIES

  discover, plan, observe
