mu guide plugin k8s — Kubernetes convergence plugin

Applies Kubernetes manifests and detects drift between desired and live state.

USAGE IN mu.json

  {
    "target": "//deploy/myapp",
    "toolchain": "k8s",
    "sources": ["deploy/*.yaml"],
    "config": {
      "namespace": "production",
      "context": "my-cluster",
      "server_side": true
    }
  }

CONFIG FIELDS

  namespace       Kubernetes namespace.
  context         kubectl context name.
  kubeconfig      Path to kubeconfig file (default: ~/.kube/config).
  server_side     Use server-side apply (default: true).
  prune           Prune resources not in manifest (default: false).
  dry_run         Run kubectl --dry-run=server (default: false).
  ignore_paths    List of dot-separated field paths to ignore in drift
                  detection (e.g. ["metadata.annotations.kubectl"]).

EXAMPLES

  Apply manifests:
    {"namespace": "default", "context": "minikube"}

  Server-side apply with pruning:
    {"namespace": "prod", "server_side": true, "prune": true}

  Dry-run only:
    {"namespace": "staging", "dry_run": true}

OBSERVATION (DRIFT DETECTION)

  mu observe //deploy/myapp

  The plugin compares desired state (from source manifests) against live
  state (from kubectl get). It strips server-managed fields like:
  - metadata.managedFields, resourceVersion, uid, creationTimestamp
  - generation, selfLink, status

  It projects live state down to only the keys present in the desired
  state, then reports differences as dotted-path diffs.

  Records include _schema "k8s.<kind>" (e.g. "k8s.deployment").

ACTIONS GENERATED

  kubectl-apply   Applies manifests with kubectl apply.
                  Requires network access.

CAPABILITIES

  discover, plan, observe
