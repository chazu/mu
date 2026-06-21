// Example 2 — Kubernetes resource policy compliance
// Illustrative; #SystemModel schema not finalized.
//
// Fixed point: OBSERVATION. Fully v1-real — observe + flag, no convergence.

import "op"

k8sPolicy: #SystemModel & {
	name:   "k8s-resource-policy"
	schema: ["k8s.workload", "k8s.pdb"]
	vault: {KUBECONFIG: "pass:clusters/prod/kubeconfig"}

	// POPULATE — #PluginObserve: reuse the k8s plugin's observe op (P2). One record
	// per workload + per PDB, ingested under k8s.* schemas.
	populate: #PluginObserve & {
		plugin: "k8s"
		input: {
			kubeconfig: vault.KUBECONFIG
			kinds: ["Deployment", "StatefulSet", "DaemonSet", "PodDisruptionBudget"]
		}
	}

	// RELATE — Datalog derives the policy violations (see rules/k8s_policy.cue).
	relations: ["rules/k8s_policy.cue"]

	// CHECK — the policy gates. expect: "empty" means compliant. These run in
	// `pudl run` by calling datalog.Evaluate directly (P1) — no #DatalogQuery.
	checks: [
		{name: "resources_set", query: "workload_missing_resources", expect: "empty", severity: "fail", message: "workload missing requests/limits"},
		{name: "pdb_present", query: "workload_without_pdb", expect: "empty", severity: "warn", message: "workload has no PodDisruptionBudget"},
	]

	// No desired/converge → OBSERVE-ONLY. Optional gated convergence (apply missing
	// PDBs) would add `desired` + a #PluginPlan converge arm — V1-OPEN.

	freshness: {every: "15m", drift: true}
}
