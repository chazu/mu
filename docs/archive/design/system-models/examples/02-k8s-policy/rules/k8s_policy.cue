// Datalog policy rules for example 2 (illustrative).
// Evaluated by pudl run via datalog.Evaluate (P1) — the same engine `pudl query`
// uses. Relations referenced by the model's `checks`.

package rules

// A workload is non-compliant if any container lacks resource requests or limits.
workload_missing_resources: {
	// workload_missing_resources(ns, name) :-
	//     workload(ns, name, container),
	//     container(container, reqs, lims),
	//     (reqs == null ; lims == null).
	_ = "see #Rule definitions; head: (ns, name)"
}

// A workload is uncovered if no PodDisruptionBudget selects it.
workload_without_pdb: {
	// workload_without_pdb(ns, name) :-
	//     workload(ns, name, _),
	//     not pdb_covers(ns, name).
	_ = "see #Rule definitions; head: (ns, name)"
}
