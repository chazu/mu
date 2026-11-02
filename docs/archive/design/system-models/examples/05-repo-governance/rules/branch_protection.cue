// Datalog relation for example 5 (illustrative).
// Flags repos whose default branch lacks standard protection. Reads the per-repo
// `protections` field, which carries the #HttpBatch E4 envelope — so the rule also
// distinguishes a failed fetch (envelope .ok == false) from a real violation.

package rules

default_unprotected: {
	// default_unprotected(repo) :-
	//     repository(repo, branch, prot_envelope),
	//     prot_envelope.ok == true,
	//     not protects(prot_envelope.value, branch, {no_force_push, review_required}).
	//
	// A separate relation flags fetch failures so they are not silently treated
	// as "protected":
	//   protection_fetch_failed(repo) :-
	//     repository(repo, _, prot_envelope), prot_envelope.ok == false.
	_ = "head: (repo)"
}
