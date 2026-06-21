# 1 — Remote server provisioning (Odroid HC2)

**Goal.** Bring `192.168.1.104` (OMV/Ubuntu) to a known state: packages, a service
user, a config file, a running systemd unit — and keep it there.

**Fixed point.** Convergence (desired state fully known). **In v1: observe + flag
only** — the converge arm and the reconcile loop are deferred (ledger V1).

## Under the current design

- **Populate is `#PluginObserve`, not inline `op.#Plugin`** (P2). The shipped
  `host` SSH observer runs as its own action; its observed packages/users/files/
  services are ingested into the catalog under `host.*`. No ewe program file — the
  populator *is* the plugin observe.
- **Secret** (`ROOT_SSH_KEY`) is resolved and used plugin-side; it never enters CUE.
- **`desired` + `converge`** are written but the converge field is flagged
  `# V1-OPEN`. What works in v1 is `populate` → ingest → drift-vs-`desired` flag.

## What `pudl run odroid-hc2` does

**v1 (observe + flag):**
1. **Accumulate** — `host observe` SSHes in → catalog under `host.*`.
2. **Flag** — drift relation (`host_drift`) diffs `desired` vs observed; the
   `no_residual_drift` check reports what's off (podman missing, `svc` absent, …).
3. **Report** — markdown + JSON of the drift. No mutation.

**Deferred to V1 (convergence):**
4. **Transform + Execute** — `converge` hands drift to mu; `remote-exec`/
   `remote-file` emit actions, `mu build` runs them over SSH.
5. **Loop** — re-observe; drift shrinks; iterate until `drift = ∅`. The loop's
   termination/gating is exactly what ledger V1 must design.

## Files
- [`model.cue`](model.cue) — the model declaration.
