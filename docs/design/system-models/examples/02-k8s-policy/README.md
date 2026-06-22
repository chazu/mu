# 2 — Kubernetes resource policy compliance

**Goal.** Across a cluster, require every workload to set CPU/memory requests +
limits and have a PodDisruptionBudget. Audit a system you may not own end-to-end.

**Fixed point.** Observation. **Fully v1-real** — observe + flag, no mutation.

## Under the current design

- **Populate is `#PluginObserve`** (P2) — the `k8s` plugin's observe lists
  Deployments/StatefulSets/DaemonSets/PDBs → catalog.
- **Checks run in `pudl run`** by calling `datalog.Evaluate` directly — the same
  path `pudl query` ships. There is **no `#DatalogQuery` ewe function** (P1); the
  populator never queries the lake, it only writes to it (here, via the plugin).
- **Two fixed points stack:** the Datalog fixed point (rule evaluation terminates)
  lives inside the observation fixed point (catalog stabilizes).

## What `pudl run k8s-resource-policy` does

1. **Accumulate** — `k8s observe` → catalog (`k8s.#Workload`, `k8s.#PDB`,
   model-shipped schemas — D2).
2. **Configure** — schema inference types each record.
3. **Unify** — `datalog.Evaluate` runs `rules/k8s_policy.cue` to a fixed point,
   deriving `workload_missing_resources` and `workload_without_pdb`.
4. **Check** — non-empty `resources_set` → run reports findings (`fail` severity);
   non-empty `pdb_present` → `warn`. **A finding is success, not a run failure**
   (V2): the run completed and flagged offenders (ns/name) as markdown + JSON.
5. **Freshness** — every 15m; a compliant cluster re-observes to a byte-identical
   catalog → no new version (the observation fixed point).

## Optional convergence (V1-OPEN)

Owners wanting auto-remediation of the PDB case would add `desired` (a PDB per
workload) + `converge: #PluginPlan & {plugin:"k8s", input:{apply:["PodDisruptionBudget"]}}`.
Requests/limits stay flag-only (mutating pod specs is owner territory) — one model
converging *part* of a system and flagging the rest. The loop is ledger V1.

## Files
- [`model.cue`](model.cue) — the model declaration.
- [`rules/k8s_policy.cue`](rules/k8s_policy.cue) — the policy relations (illustrative).
