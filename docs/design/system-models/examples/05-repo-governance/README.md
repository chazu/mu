# 5 — Repo governance (GitLab branch protection)

**Goal.** Extend the GitLab inventory: catalog every repo *and* require the default
branch be protected (no force-push, requires review). Observe + flag by default,
optional enforcement where you have authority.

**Fixed point.** Observation (→ optional convergence). **Fully v1-real** as
observe + flag.

## Under the current design — the fan-out showcase

This is the example the first adversarial review said "does not run." Here is the
form that does, under the current design:

```
#HttpAll (page repos) → pure build req list → #HttpBatch (N fetches) → pure join
```

- **No effect inside a comprehension (E2).** The original sketch ran `op.#Http`
  inside `for r in _repos.result` — illegal. Instead: pure CUE builds a list of
  per-repo requests, **one `#HttpBatch`** runs them (parallel, in Go), pure CUE
  joins results by index.
- **Secret as a header ref (S1):** `auth: { header: { name: "PRIVATE-TOKEN", ref:
  "GITLAB_TOKEN" } }` — never `headers: { "PRIVATE-TOKEN": _t.result }` built in CUE.
- **Per-item failures (E4):** `#HttpBatch` returns one envelope per repo
  (`{ok, value} | {ok:false, status, error}`); the join carries it through so the
  Datalog rule flags a *failed fetch* distinctly from an actual *violation* — a
  partial inventory is never silently scored as "compliant".
- **Populate is a program** ([`populate.cue`](populate.cue)), `eweSource`-referenced.

## What `pudl run gitlab-governance` does

1. **Accumulate** — paginate projects; batch-fetch protected-branches; join in CUE
   → one enriched `git.repository.gitlab` record each → catalog.
2. **Unify** — `branch_protection.cue` derives `default_unprotected` (and
   `protection_fetch_failed`).
3. **Check** — `default_branch_protected` warns, listing offenders. Report = the
   governance audit. Findings ≠ run failure (V2).
4. **Freshness** — every 6h; stable unless a repo's protection actually changes.

## Enforcement is a one-field upgrade (V1-OPEN)

Add `desired` + `converge: #PluginPlan & {plugin:"gitlab", input:{apply:"protected_branches"}}`
and the same model *applies* standard protection to repos you administer — while
still only flagging repos you merely observe. The observe→enforce upgrade is the
same shape as example 2. The loop is ledger V1.

## Files
- [`model.cue`](model.cue) — the model declaration.
- [`populate.cue`](populate.cue) — the ewe populator (**the batch + join showcase**).
- [`rules/branch_protection.cue`](rules/branch_protection.cue) — the protection relation.
