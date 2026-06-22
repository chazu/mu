// Example 5 — GitLab repo governance, ewe populator program (current design).
//
// Content-addressed; referenced by `eweSource` (I1). This is THE fan-out showcase:
// "N fetches, one per item from a prior fetch" — expressed without any effect
// inside a comprehension (E2).
//
// Shape:  #HttpAll (page repos)  →  pure build req list  →  #HttpBatch (N fetches)
//         →  pure join by index.
// Secrets are header refs via auth.header (S1) — revealed only at the sinks.

import (
	"op"
	"encoding/json"
)

// 1. EFFECT — fetch all projects (paginated, Go-internal).
_repos: op.#HttpAll & {args: [{
	url:   "https://gitlab.com/api/v4/groups/garner-health/projects"
	query: {include_subgroups: true, per_page: 100}
	auth: {header: {name: "PRIVATE-TOKEN", ref: "GITLAB_TOKEN"}}
	paginate: {style: "page", param: "page", stop: "empty"}
}]}

// 2. PURE CUE — keep repos that have a default branch.
_have: [for r in _repos.result if r.default_branch != null {r}]

// 3. PURE CUE — build one protected-branches request per repo. NO effect here.
_reqs: [for r in _have {
	url: "https://gitlab.com/api/v4/projects/\(r.id)/protected_branches"
	auth: {header: {name: "PRIVATE-TOKEN", ref: "GITLAB_TOKEN"}}
}]

// 4. ONE EFFECT — N protected-branch fetches → list of E4 result envelopes
//    (parallel in Go). Replaces the illegal op.#Http-inside-`for`.
_prot: op.#HttpBatch & {args: [_reqs]}

// 5. PURE CUE — join each repo with its protections by index. The envelope
//    ({ok, value} | {ok:false, status, error}) is carried through so the
//    branch_protection rule can flag fetch failures distinctly from violations.
_out: [for i, r in _have {
	_schema:        "pudl/git.#GitLabRepository" // def reference (D4); shipped pudl schema
	name:           r.path_with_namespace
	default_branch: r.default_branch
	protections:    _prot.result[i] // E4 envelope; rule reads .ok / .value
}]

out: op.#WriteFile & {args: ["\(op.#Env.result.MU_OUT)/repos.json", json.Marshal(_out)]}
