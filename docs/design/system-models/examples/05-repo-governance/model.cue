// Example 5 — Repo governance (GitLab branch protection)
// Illustrative; #SystemModel schema not finalized.
//
// Fixed point: OBSERVATION (→ optional convergence). Fully v1-real as observe+flag.

import (
	"pudl/git"  // #GitLabRepository is a shipped pudl schema
	"gitgov"    // #BranchProtection is model-introduced (.pudl/gitgov, D2)
)

gitlabGov: #SystemModel & {
	name: "gitlab-governance"
	// schema: definition references (D3) — one shipped, one model-introduced.
	schema: [git.#GitLabRepository, gitgov.#BranchProtection]
	vault: {GITLAB_TOKEN: "env:GITLAB_TOKEN"}

	// POPULATE — ewe program (see populate.cue): paginate projects, then fetch each
	// repo's protected-branches via ONE #HttpBatch, joined in pure CUE. The gist
	// generalized, current-design form.
	populate: #EweTarget & {
		eweSource: "populate.cue"
		outputs: ["repos.json"]
		network:  true
		sealed_inputs: {GITLAB_TOKEN: "env:GITLAB_TOKEN"}
		sealed_input_modes: {GITLAB_TOKEN: "env"}
	}

	// RELATE — derive unprotected default branches (see rules/branch_protection.cue).
	relations: ["rules/branch_protection.cue"]

	// CHECK — observe-only flag (v1-real).
	checks: [{
		name: "default_branch_protected", query: "default_unprotected", expect: "empty"
		severity: "warn", message: "default branch not protected"
	}]

	// OBSERVE-ONLY by default. To ENFORCE on repos you admin (V1-OPEN), add:
	//   desired:  [ for r in repos { protection: standard_protection } ]
	//   converge: #PluginPlan & {plugin:"gitlab", input:{apply:"protected_branches"}}
	// → flags everywhere, enforces only where desired is declared.

	freshness: {every: "6h", drift: true}
}
