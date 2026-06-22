# Issue ledger — System Models / ewe-in-CUE

Consolidated, deduplicated, dependency-ordered tracker for every finding from
[adversarial-review.md](archive/adversarial-review.md) (C/M/m) and
[adversarial-review-2.md](archive/adversarial-review-2.md) (CRIT/MAJOR/MIN), plus the
state of [ewe-arg-resolution-spec.md](ewe-arg-resolution-spec.md). Each row is
resolved one at a time; the relevant design doc is amended as we go.

Status: ⬜ open · 🔬 validated/decided, doc pending · ✅ resolved + doc updated.

## End goal (the thing all of this serves)

A `#SystemModel` is one declaration that bundles **shape + populate + relate +
check + freshness** (+ optional **desired + converge**), and `pudl run <model>`
drives the IDEA/ACUTE loop to a fixed point — observation fixed point
(inventory stable) for observe-only models, convergence fixed point
(observed == desired) when a desired state is declared. The populator is an
**ewe-in-CUE** action body kind in mu; checks/relations are pudl Datalog;
secrets are sealed refs revealed only at sinks; capability extension is mu
plugins via `#Plugin`. No second evaluator, no embedded Lisp.

---

## ENGINE — ewe repo (gates every populator example)

| ID | Folds in | Issue | Status |
|----|----------|-------|--------|
| E1 | CRIT-1, CRIT-2, C1(arg part) | Nested-`.result` args + hidden fields unresolvable → LookupPath-the-args-subpath redesign | 🔬 validated empirically (below) |
| E2 | C1, m2 | Effects inside comprehensions unsupported → reject loudly + batch/join pattern | 🔬 decided (below) |
| E3 | MAJOR-4 | `maxPasses=16` ceiling + large-list splice re-parse cost | 🔬 decided (below) |
| E4 | MIN-2 | Batch partial-failure (one item errors → whole pass aborts) | 🔬 decided (below) |
| E5 | C2(ii), MAJOR-3 | Pure ordering (`after:`/`callReady`) — needed, or cut? | 🔬 decided: CUT (below) |
| E6 | NEW-1 | `cueValueToGo` keeps quotes on quoted struct labels (`"PRIVATE-TOKEN"` → `"\"PRIVATE-TOKEN\""`) | 🔬 fix located (below) |

## SECRETS / TAINT

| ID | Folds in | Issue | Status |
|----|----------|-------|--------|
| S1 | C3, CRIT-3, MIN-1 | Secret-by-reference + secret-as-substring template + `goToCUEExpr` fail-closed guard | ✅ [ewe-secrets-spec.md](ewe-secrets-spec.md) |

## CACHING / DETERMINISM

| ID | Folds in | Issue | Status |
|----|----------|-------|--------|
| K1 | M1, MAJOR-1, MIN-3 | Every populator `impure` → never cached; ewe has no memoization; `#Now` has no seed mechanism | ✅ reframed (below) |

## MU INTEGRATION

| ID | Folds in | Issue | Status |
|----|----------|-------|--------|
| I1 | C2(i), MAJOR-2 | `ewe` action body kind: raw-source storage vs string field; per-execute registry; `#Output` wiring | ✅ [ewe-body-kind-spec.md](ewe-body-kind-spec.md) |
| I2 | m3 | Pagination vocabulary (cursor / Link-header), all Go-side | ✅ [ewe-http-pagination-spec.md](ewe-http-pagination-spec.md) |

## PUDL INTEGRATION

| ID | Folds in | Issue | Status |
|----|----------|-------|--------|
| P1 | M4 | `#DatalogQuery` has no driver; rule-file loading unwired | ✅ dissolved (below) |
| P2 | M3 | Inline `#Plugin` lifecycle vs dataflow form (4/5 examples use inline) | ✅ cut (below) |

## CONVERGENCE / ORCHESTRATION (end-goal clarity; build scoped out of v1)

| ID | Folds in | Issue | Status |
|----|----------|-------|--------|
| V1 | M2 | No reconcile loop exists; convergence half unimplemented end-to-end — **expanded into V1.1–V1.6 below** | ✅ **design resolved** (V1.1–V1.4, V1.6 resolved; V1.5 cut) — ready to build |
| V1.1 | M2 | **The loop** — re-observe → drift → transform → execute → repeat, in `pudl run` | ⬜ (loop-owner fork resolved: **pudl-driven**, extends observe-only) |
| V1.2 | M2 | **Termination / fixed-point** — stop at drift = ∅ + guard (max iters; drift must monotonically shrink) | ✅ **resolved** — drift==∅ fixed point + hard cap; **monotonic guard DEFERRED** (dialectic: [`v1-2-loop-termination.ndjson`](../dialectics/v1-2-loop-termination.ndjson)) |
| V1.3 | — | **Drift-gating** — does converge *fire* or only flag? severity threshold / explicit opt-in | ✅ **resolved** — explicit opt-in via `--converge`; no severity magic |
| V1.4 | m1 | **Convergence-failure reporting** — "applied but still drifts" / "drift grew" / action failed mid-loop (distinct from drift) | ✅ **resolved** — reuse `failed` status; 2 modes (`cap_exhausted`/`execute_error`); mandatory partial-state warning |
| V1.5 | — | **Partial-apply / rollback** — execute fails halfway against a live system | ❌ **CUT** — out of scope (owner decision, V1 session) |
| V1.6 | — | **`converge` field paths** — `#PluginPlan` (≈ exists via `export-actions`) + the ewe-converge path (`#EweTarget` emitting actions) | ✅ **resolved** — V1 converge = `#PluginPlan` only; ewe-**converge** deferred. ewe-**populate** (pull) kept, untouched |
| V2 | m1 | `pudl run` error handling / per-target status / partial-failure | ✅ scoped to observe (below) |
| V3 | m4 | "Delete pith" premature — sequence behind a working observe-only spike | ✅ defer deletion (below) |

---

## E1 — RESOLVED (validated)

**Finding (CRIT-1/CRIT-2):** `extractArgsWithFallback` (`processor.go:176-214`)
resolves an arg element only if it is wholly literal or a bare top-level ref;
a `.result` embedded in a struct/list/interpolation is unresolvable, and
`LookupPath` rejects hidden labels. Kills every real example.

**Resolution:** the spec's redesign — stop hand-converting the args AST; instead
`LookupPath` the call's `args` subpath in the already-evaluated partial compile,
let the CUE engine evaluate nested refs / interpolation / builtins, and convert
the concrete result with the existing `cueValueToGo`. Hidden fields addressed by
building a typed `cue.Path` with `cue.Hid(name, pkg)` instead of `ParsePath`.

**Empirical validation (throwaway test against real CUE engine, 2026-06-19):**
a partial-compile fixture where `_repos` (hidden) has `args` embedding
`_tok.result` (nested in a struct) and `"\(_env.result.MU_OUT)/repos.json"`
(interpolation):

- `LookupPath(cue.MakePath(cue.Hid("_repos","_"), cue.Str("args")))` → exists ✓
- `Validate(cue.Concrete(true))` → nil once refs concrete ✓
- interpolation evaluated to `/out/repos.json` ✓
- negative control (before `_tok.result` spliced) → `undefined field: result`
  → pass correctly skips/retries ✓

So the LookupPath redesign genuinely closes CRIT-1 + CRIT-2. Spec stands; carry
E6 (quoted-label bug) as a rider fix in the same change.

## E2 — RESOLVED (decided)

**Finding (C1/m2):** effects inside a `for`/comprehension body are unsupported —
`findCallSites` only matches static `op.#X & {…}` fields; per-item calls don't
exist as nodes until the final `CompileString`, long after effects ran. The
flagship example 5 ("N fetches, one per item") was written this way.

**Resolution:** *forbid* effects in comprehensions; detect and fail loudly
(spec Change 3, `flagComprehensionCalls`). The sanctioned N-fan-out is
**batch effect + pure build/join**:

```cue
_reqs: [ for r in _repos.result { {url: "…/\(r.id)/…", headers: {…}} } ] // pure CUE
_prot: op.#HttpBatch & { args: [_reqs] }                                  // ONE effect → list
_out:  [ for i, r in _filtered { {…, protections: _prot.result[i]} } ]    // pure CUE join
```

Review #2 showed this pattern *only* worked with non-hidden, bare-named,
top-level fields — because of CRIT-1/CRIT-2. **E1 removes exactly that
restriction**, so batch+join now works with hidden fields and nested refs. This
is review #1's recommended path (b); both reviews agree on forbidding effects in
comprehensions. Comprehension-unrolling (Route B) stays deferred sugar — not
built. Gate: confirm batch+join evaluates end-to-end against a stub registry
(spec sequencing step 2).

## E3 — RESOLVED (decided)

**Finding (MAJOR-4):** `maxPasses=16` is a hard ceiling; each pass re-parses,
re-runs `compilableSource`, recompiles whole source; a batch returning hundreds
of items splices a huge `ListLit` back as source re-parsed every later pass.
Termination/cost of chained batches unverified.

**Resolution — separate the two concerns:**

1. **Pass count is not the worry.** Passes-needed ≈ *longest `.result`
   dependency chain*, **not** the number of calls — the inner loop resolves
   every ready call each pass. A realistic populator chain
   (secret→fetch→build→batch→join→write) is ~4–6 levels; 16 is generous.
   Defensive only: make `maxPasses` a `Processor` option (default 16), raisable
   if a real model needs it.
2. **Splice cost is real but measure-first.** In the canonical pattern a large
   list threads through CUE only for the index-join, then is written out via a
   sink. Ship v1 as-is + add a benchmark (500-item batch + join). **Escape
   hatch if it bites:** store executed results out-of-band keyed by call path and
   inject a *reference* into `compilableSource` instead of re-splicing literal
   source (already named in spec Risks). Built only on demonstrated cost.

## E4 — RESOLVED (decided) — contract lives in the sink layer, not the engine

**Finding (MIN-2):** `fn.Execute` returning an error aborts the entire
`ProcessSource` (`processor.go:84-86`). Inventory fan-out over hundreds of repos
needs per-item tolerance.

**Resolution:** keep the engine's "Execute error = fatal" (correct for genuine
faults — bad config, whole-call auth failure). Per-item tolerance is the **batch
function's ABI**: a batch effect catches per-item errors and returns a list of
**result envelopes** —

```cue
_prot.result: [ {ok: true, value: {…}}, {ok: false, status: 403, error: "forbidden"}, … ]
```

The pure-CUE join decides per item (skip / flag as a `#Check` finding / default).
The batch errors the whole call only if it cannot run at all (arg not a list).
Engine semantics unchanged; envelope shape documented as the batch ABI in the
sink-function spec (I1 layer). No ewe-repo change.

## E6 — RESOLVED (fix located)

**Finding (NEW-1):** `cueValueToGo` (`convert.go:284`) keys structs with
`iter.Selector().String()`, which returns the *source* form — a quoted label
`"PRIVATE-TOKEN"` becomes Go map key `"\"PRIVATE-TOKEN\""`, breaking header
names. Surfaced by the E1 validation test.

**Resolution:** unquote string labels —

```go
sel := iter.Selector()
key := sel.String()
if sel.Type() == cue.StringLabel {
    key = sel.Unquoted()
}
result[key] = val
```

Few lines; rider on the E1 change. Add a test with a quoted label.

## E5 — RESOLVED (decided: CUT from v1)

**Finding (C2-ii/MAJOR-3):** ewe has no pure-ordering primitive — "run A before B
though B doesn't consume A's value." No `#Seq`; the rebuttal proposed an
`after:`/`callReady` gate (gate a call on every `.result` ref in its struct, not
just `args`). Review #2 called it cosmetic *because it rested on the broken arg
substrate* — which E1 has since fixed, so the gate would now be sound and is
nearly free under `resolveCallArgs` (also LookupPath+validate an `after:` field).

**Decision: do not build `after:`/`callReady` in v1.** The reasoning is
**layering, not YAGNI:**

1. **Observe-only never needs it.** Every effect threads the previous one's
   `.result` (fetch→build→batch→write); data-dependency ordering already
   sequences the whole chain. There is no "A before B without dataflow" shape in
   an inventory populator.
2. **Pure cross-effect ordering is the DAG's job, not the ewe body's.** mu already
   sequences *actions* by dependency. Two ordered-but-independent effects
   (`#Converge` then `#Notify`) should be **two mu actions** — the DAG orders them
   for free, which is the charter ("mu is the executor; ordering is the DAG's
   job") and the doc's own "dataflow preferred" path. An `after:` field inside one
   ewe body duplicates a mechanism mu already has and blurs the boundary.
3. **A body that wants `after:` is a smell** that says "these belong in separate
   actions." Keeping intra-body ordering purely data-driven yields one clean rule:
   *one ewe body = one dataflow DAG; ordering across independent effects = mu
   actions.* Two overlapping ordering mechanisms is the thing to avoid.

**The clean rule, recorded:**

> Inside an ewe body, ordering is **only** via `.result` data dependencies. If you
> need A before B without B consuming A, split them into two mu DAG actions; the
> coordinator orders them. ewe never gets a pure-sequencing primitive.

**Revisit trigger:** if the convergence layer (V1) demonstrates a real
ergonomic need for a tight `converge; notify` sequence inside *one* body (vs. the
two-action form + `#Output` wiring), reopen E5 then — the gate stays cheap to add
later. Until a concrete convergence consumer exists, it stays cut.

**Independent check:** this decision was re-derived by a 3-persona `dlktk`
dialectic (grounded semantics, not assertion) — recorded at
[../dialectics/e5-pure-ordering.ndjson](../dialectics/e5-pure-ordering.ndjson),
discussion `vagoh-dativ`. The engine labelled CUT=IN, BUILD=OUT *without* the
tie-break preference being load-bearing. BUILD fell on two surviving objections:
DAG already orders independent actions (charter), and no concrete convergence
consumer exists yet (speculative). `dlktk check` can re-verify it for drift.

## S1 — RESOLVED → [ewe-secrets-spec.md](ewe-secrets-spec.md)

Secret-by-reference (three layers: `{"$secret":name}` → `$secretTemplate`/`#Secretf`
→ full `auth:` vocab), reveal only in Go sinks, fail-closed `goToCUEExpr` guard as
backstop. Keystone safety property (CUE rejects struct-into-string interpolation)
verified empirically. No `pith.Secret` on the ewe path. Egress/exfil is a sandbox
concern, not a taint concern — boundary recorded. Full design in the spec.

## K1 — RESOLVED (reframed: caching is the wrong tool for observation)

**Findings (M1/MAJOR-1/MIN-3):** every populator sets `impure: true`, and
`executeAction` skips both cache lookup and store when `a.Impure`
(`executor.go:222,382`) — so "no ewe populator is ever cached" contradicts
"cached by input hash." ewe's `Function.Cacheable` is declared but never read.
`#Now` "seedable for determinism" specifies no mechanism.

**Grounding (verified in mu source):**
- `observe` is **not new** — a shipping plugin capability (`manager.go:237`,
  `process.go:290`) + `mu observe` → `Coordinator.Observe` (`coordinator.go:746`).
- `Coordinator.Observe` is single-pass and **not a DAG action** — it talks to
  plugins directly, never touches the action cache. Comment `:744`: "Convergence
  decisions are made downstream (by pudl), not by mu." mu already draws the line.
- The cache concern is about the **build DAG** (the ewe populator action), not
  `mu observe`.

**Resolution — `impure` is correct, not a workaround. Caching and observation are
antithetical:**

1. **Observe reads live state; live state is not in the key.** Caching reuses
   output for unchanged inputs, but observe's output changes when external reality
   changes with no input change. The external system is the thing measured, not a
   cache input. Caching would serve stale reality. `impure: true` is the right and
   intended setting. **Doc fix:** drop the "cached by input hash" claim for
   populators wherever it appears — it was wrong.
2. **Pure-transform granularity follows the DAG.** A populator is impure fetch +
   pure CUE transform in one body; the transform re-runs each time (cheap). If a
   transform is ever expensive, split it into its own **pure** action consuming the
   fetch's output, which caches on that output's content hash. **Deferred: do not
   build the pure-transform action in v1** — one impure action; split only on
   demonstrated cost (same YAGNI logic as E5).
3. **Remove `Function.Cacheable` (ewe repo).** Within one ewe body the multi-pass
   loop runs each call once (splice-back → literal), so intra-body memoization is
   already implicit. The field implies a caching layer that doesn't exist and isn't
   needed; cross-run caching is the *action* cache, not ewe's job. Delete it — it's
   misleading dead code.
4. **`#Now` (MIN-3) is moot.** No cache key to seed (impure) → nothing to make
   deterministic. `#Now` returns wall-clock; re-running re-reads it — correct for
   "observe live state at time T." No seed mechanism in v1; revisit only if a
   pure, cacheable ewe action ever needs deterministic time (none in observe-only).
5. **Currency = freshness, not caching.** Re-observation cadence (every N) keeps a
   model current — the opposite of caching. That's the orchestration/loop concern
   (V1), not K1.

**One distinction recorded (kills the confusion):** there *is* a legitimate
observe-side "stable when unchanged" mechanism, but it is **not** action-input
caching — it is the README's *observation fixed point*: re-observing unchanged
reality yields a byte-identical catalog → **pudl catalog content-hash dedup, no
new version**. That lives downstream in pudl's catalog (skip *versioning*), not in
the action cache (skip *execution*). Action cache = "don't run it" (wrong for
observe). Catalog dedup = "ran it, result identical, don't version it" (right for
observe). Two different mechanisms, two different layers.

**Net:** M1/MAJOR-1/MIN-3 dissolve — no missing caching story to build; caching is
the wrong tool for observation. Deferred: pure-transform cacheable action (build
on demonstrated cost). Doc fixes: drop "cached by input hash" for populators;
remove `Function.Cacheable`.

## I1 — RESOLVED → [ewe-body-kind-spec.md](ewe-body-kind-spec.md)

Populator-as-program: an ewe populator is a normal `.cue` file, content-addressed
into CAS (the plugin idiom), the action carries an `EweRef` digest. Resolves
MAJOR-2 (the "recover source from a cue.Value" problem never arises — the program
is never loaded as config). Per-execute registry closes over sandbox + `reveal`
(ties to existing `SealedInputs`) + `MU_OUT`; third dispatch arm in
`executeAction`. `#Output` dropped — cross-action data is a declared `input`
(content digest + implicit `DependsOn`, `resolve.go:24-55`) read via `#ReadFile`.
actionkey adds one stanza hashing the program *digest* (honors K1). Inline
struct/string, AST-in-memory, and populate-as-phase all rejected with reasons.
All integration points grounded in current mu source.

## I2 — RESOLVED → [ewe-http-pagination-spec.md](ewe-http-pagination-spec.md)

`#HttpAll` paging is greenfield Go (mu has only single-request `http/request`
today). Fixed strategy enum — `none`/`page`/`link`/`cursor` — discriminated by
`style`, parameterized by where signals live (not a DSL). `maxPages` enforced
default 1000 (hard backstop on unbounded live-HTTP spend; errors on hit).
Fail-loud on mid-pagination error (one logical fetch ≠ batch's per-item envelope,
E4). Build all four in v1 (GitHub needs link/cursor day one); `total-count`
deferred. Tied into the secrets-spec sink suite (`auth:`/`resolveSecrets` per page).

## P1 — RESOLVED (dissolved: the bridge already exists)

**Finding (M4):** `pithdriver/register.go` registers catalog/fact/schema/drift —
**no datalog driver word**; `#DatalogQuery` must be wired fresh to
`datalog.Evaluate`/`pkg/factstore`, and rule-file loading is unwired. Presented as
the one lake primitive that doesn't exist.

**Grounding (pudl source, 2026-06-20):** M4 measured the wrong seam.
- `pudl query` **already evaluates datalog directly** — `datalog.LoadRulesFromPaths`
  (`cmd/query.go:103`, the "unwired" rule loading) + `datalog.Evaluate`
  (`cmd/query.go:119`) — **bypassing the pith driver layer entirely**.
- A clean library API sits above it: `pkg/eval.LoadRulesFromPaths`,
  `pkg/factstore.Store.Query(QueryOptions)` (`factstore.go:111`).

So the engine is directly callable and already called; the orchestrator never
goes through the driver layer.

**Resolution:**
1. **Checks in `pudl run` = direct engine call**, reusing the exact path
   `pudl query` ships (`pkg/factstore.Query` / `datalog.Evaluate` + severity). No
   new engine, no new bridge — lift the existing call. The only datalog wiring
   observe-only v1 needs.
2. **No datalog *pith driver word*.** M4's "add the missing driver to match the
   other four" is backwards — pith is deprecating (V3); don't extend the dying
   layer. The orchestrator calls the package directly.
3. **`#DatalogQuery` *ewe function*: defer.** Its only named consumer was
   "evaluate a `#Check` from inside a model build" — but check evaluation moved to
   `pudl run` (orchestrator), not the populator body. A populator *writes* the
   catalog; it does not *query relations mid-fetch*. No observe-only consumer →
   defer (YAGNI); add only if in-populate enrichment proves real.
4. **The whole Tier-2 lake ewe vocabulary (`#CatalogQuery`/`#FactQuery`/`#Schema*`/
   `#Drift`/`#DatalogQuery`): defer.** Bigger cut than the review implied, made
   explicitly. The populate→catalog path is **ewe writes JSON (`#WriteFile`) →
   action output → `pudl run` ingests** (the `_schema` tag routes records). The
   populator never calls a catalog ewe function, so Tier-2 has no v1 consumer.

**Net:** P1 needs no new datalog machinery — `pudl run` reuses the direct call
`pudl query` already ships; pith stays untouched; the ewe-side lake functions are
deferred for lack of an observe-only consumer. **Carries into V1:** the catalog
*ingest* step in `pudl run` (read populator JSON output → catalog records) is
`pudl run` orchestration plumbing — specced under V1, not here.

## P2 — RESOLVED (cut: inline `#Plugin` and the `#Plugin` ewe function both unbuilt)

**Finding (M3):** plugins start only at coordinator boundaries
(`manager.go:131` — `Manager.Start` batch-spawns, queried via `mgr.Plan`/
`mgr.Observe`), never from inside an action body. Inline `#Plugin` (a plugin
subprocess mid-ewe-execute) is entirely new machinery — subprocess lifecycle,
sandbox ownership, NDJSON from inside a sandboxed action. Bite: 4 of 5 examples use
the inline form despite the doc recommending dataflow.

**Grounding (mu source, 2026-06-20):** there is no path to spawn a single plugin
from inside a sandboxed action. A sandboxed action spawning subprocesses is
sandbox-escape-shaped — it cuts against the boundary that keeps the privileged
surface auditable.

**Structural fact:** `#SystemModel.populate` is already typed
`#EweTarget | #PluginObserve` — two disjoint paths that converge on the same
ingest, neither of which is inline `#Plugin`:
- **`#EweTarget`** (GitLab): ewe body fetches via ewe *sinks* (`#Http`/`#HttpAll`)
  → writes JSON → action output → `pudl run` ingests. No plugins.
- **`#PluginObserve`** (Proxmox): reuse an existing observer = the **existing
  observe phase** (`Coordinator.Observe` → `ObserveResult`, `coordinator.go:746`)
  → `pudl run` ingests. Not an ewe function — `mu observe` on a plugin target.

Both end at "`pudl run` ingests structured records" (consistent with P1).

**Resolution:**
1. **Inline `#Plugin` (subprocess mid-execute): do NOT build.** New machinery
   against the sandbox boundary; no v1 consumer the two populate kinds don't serve.
2. **Plugin-populate = `#PluginObserve` kind** = existing observe phase + ingest.
   A populate *kind*, not an `op.#Plugin` call.
3. **The `#Plugin` ewe function — even the dataflow form — defer.** Only needed to
   *mix* ewe-fetch + plugin-call in one body; no v1 consumer (populate is ewe OR
   plugin-observe, never both in one body). Cross-source enrichment (e.g. GitLab
   repos + Proxmox VMs in one model) is a **join across two catalogs = pudl Datalog
   relations**, not an in-body plugin call — reinforces the cut. Same YAGNI as the
   Tier-2 lake functions (P1).
4. **DOC TASK — fix examples.md:** rewrite the Proxmox (and other inline-`#Plugin`)
   examples as `populate: #PluginObserve & {…}`, not
   `op.#Plugin & {args:[{op:"observe"}]}`. The examples must *show* the dataflow
   path, not contradict the recommendation. (Flagged, not done here.)

**Net:** M3's "new machinery" never needs building — inline `#Plugin` and the
`#Plugin` ewe function are both cut from v1. The plugin-populate path is
`#PluginObserve` (existing observe phase + ingest); cross-source joins are pudl
relations. Only real work: the examples rewrite.

## V3 — RESOLVED (decided: defer pith deletion until ewe observation ships)

**Finding (m4):** "Delete pith" is premature — given the (now-resolved) engine
findings, pith is the only thing that works today; sequencing deletion behind "ewe
proves out" must not be treated as a formality.

**Decision:** keep pith running until **observation via ewe is shipped end-to-end**
(the observe-only v1 gate: arg-resolution engine + sink suite + `ewe` body kind +
a real populator evaluating against live APIs). Only then revisit deletion. The
taint-type extraction (arg-resolution spec) proceeds regardless — it is worth
saving independently — but `pudl exec` removal and the pith VM deletion wait behind
a working ewe observe spike. The execute-time-cost (CUE-free IR) question remains
the only thing that could grant pith a permanent reprieve; unmeasured, so deletion
stays the default *after* the spike, not before.

## V2 — RESOLVED (scoped to the observe-only boundary)

**Finding (m1):** the `pudl run`→`mu build` shell-out precedent
(`pudl/cmd/memory.go`) has one-line error handling — exit-code propagation only, no
partial-failure, no per-target status. A convergence loop needs far more.

**Grounding (2026-06-20):** mu *already* emits structured output — `mu build
--json` (`mu/cmd/mu/build.go:230`) over `BuildResult` carrying `ExecResult
*dag.ExecuteResult` (per-action detail, `coordinator.go:66`). The memory-cycle
precedent simply doesn't consume it (wires stdout/stderr to the terminal, reads
only `c.Run()`'s exit code). So richer status is available, not blocked.

**Resolution — scope `pudl run` error handling to observe-only; expand as
uncovered:**
1. **Three distinct per-model outcomes** (the core distinction): **failed-to-run**
   (populate/ingest errored) vs **ran-with-findings** (a `fail`-severity `#Check`
   flagged something — this is *success*, the run completed; flagging is the point)
   vs **ran-clean**. Never conflate "a check found a problem" with "the run failed."
2. **Per-model isolation:** models are independent; one model's populate failure
   does not abort the others. Aggregate a per-model status table — this is m1's
   "per-target status," cheap because each model is its own `mu build` invocation.
3. **Consume `mu build --json`**, not just exit codes — parse `BuildResult`/
   `ExecResult` so the report names the failing action, not just "build failed."
   Available today, no mu change; strictly better than the precedent.
4. **No rollback in v1** — observation has nothing to undo (reads external state;
   writes immutable, content-versioned catalog records). Rollback is a convergence
   concern; out of scope here and not raised. One line, deliberately.

Error handling beyond this (convergence-loop failures, "drift didn't shrink"
reporting) is uncovered as we build, and defers with V1.

---

# Decided design points (beyond the review findings)

Points settled while writing the worked examples. These are *decisions*, not review
findings, but they pin down `#SystemModel` shape and resolve a README open question.

## D1 — `#SystemModel` lives in pudl

Settled. pudl is the modeling/catalog/Datalog brain; mu is the dumb executor.
A model binds catalog schemas (pudl), relations (pudl Datalog), checks (pudl),
report (pudl) — all pudl — and delegates only *populator execution* to mu. **mu
never imports `#SystemModel`:** `pudl run` compiles a model down to a mu build
(eweSource digest + sealed inputs + outputs); mu just runs the DAG. Closes the
README open question "does `#SystemModel` live in mu, pudl, or a shared module?"

**Repo split, recorded:**
- **ewe** (library) — the CUE rewrite engine (arg resolution, `#Secretf`, guard).
- **mu** — the ewe *runtime*: the `ewe` body kind + the effect **sink registry**
  (`op.#Http`/`#Secret`/`#HttpAll`/`auth`/…) + executes populator programs.
- **pudl** — `#SystemModel`, the model definitions **including their `populate.cue`
  programs**, relations, checks, `pudl run`. Populator programs are pudl-authored
  but written against the `op.#*` vocabulary mu registers.

## D2 — every record has a pudl schema; custom schemas live in pudl

User/custom schemas live in pudl — **repo-scoped `.pudl/`** or the **global
`~/.pudl`** schema repo. A model that *reuses* a shipped schema references it
(`pudl/linux`, `pudl/git`); a model that *introduces* one (tls/dns/k8s) **ships its
own pudl schema package** alongside the model (in `.pudl/`). Nothing is untyped:
every catalog record is an instance of some pudl schema definition.

## D3 — `#SystemModel.schema` carries `#SchemaRef` definition references

The `schema:` field is a list of **schema-definition references**
(`pudl/linux.#Package`), **not** dotted resource-type strings. This is the *same
identifier the plugin SDK already uses*: `DiscoverResponse.OutputSchema *SchemaRef`
(`mu/sdk/muplugin/types.go:66,82`) is a `{module, version, #definition}` pointer
"consumed by downstream tools (notably pudl) to classify imported data without
re-inferring." So the two populate kinds converge on one identifier:
- **`#EweTarget`** — the model declares `schema: [linux.#Package, …]`.
- **`#PluginObserve`** — the schema comes from the plugin's declared `OutputSchema`.

Why def references over strings: the model declares the actual CUE *shape* its
records must satisfy (validatable, not just tag-matched), and a typo fails at load
(`pudl/linux.#Pakage`) instead of silently at ingest (`"linux.pakage"`).

## D4 — no author-facing `resource_type`; records self-tag with a def reference

`resource_type` is **not a distinct concept** — verified: it appears *only* inside a
schema's `_pudl` metadata (`schemagen` generates it *from* the definition; it is
never a free-standing taxonomy). It is a schema's **internal derived identity
string**. A resource is always an instance of a schema; there is no resource-type
that isn't "which schema."

Therefore the authoring surface names **schema definitions only**; the dotted
`resource_type` is an internal handle authors never type. Concretely (decision (a)):
- A populator record self-identifies with a **`_schema` definition reference** —
  the `"pudl/<module>.#<Def>"` string form already used by `base_schema`
  (`pudl/git.git.cue:63`). Example: `_schema: "pudl/linux.#Package"`.
- (a) over (b "no `_schema`, infer from the model"): one populator can emit
  **multiple** schemas into one output (example 1: packages + users + services +
  files), so each record must self-identify.

**Implied pudl change:** the catalog binding resolves `_schema` as an **exact
definition reference** (bind directly to the def), superseding the current fuzzy
match of `_schema` against `resource_type` (`inference/heuristics.go:94`). Exact
binding is stronger — the model declares the shape; ingest validates against it
rather than guessing. The inference heuristic remains for *untagged* imported data;
tagged populator output binds exactly.

---

# V1 — convergence: remaining surface (OPEN, scoping only)

Not resolved — this records the *shape and rough size* of the convergence work, so
V1 can be opened later with a clear agenda. Convergence is **out of v1** (ship
observe-only first); everything below is the post-v1 design.

## What already exists — convergence reuses, does not build

The single-iteration ACUTE primitives mostly ship today (verified earlier under
M2 + the P1 grounding):

- **Accumulate** (observe) — `mu observe` / the populate path (now specced).
- **Unify** (drift) — `pudl drift check` exists (one-shot, `drift/checker.go:117-123`).
- **Transform** (drift → actions) — `pudl export-actions` exists (emits mu.json
  once, marks defs `"converging"`, `cmd/export_actions.go:151-167`).
- **Execute** — `mu build` runs the actions.
- **`desired`** declaration + convergence **plugins** (`plan` op) + catalog/versioning.

So *one turn of the crank* is largely assembled. This is why convergence is **less
net-new machinery than observe-only was** — the gap is orchestration + policy +
safety, not new execution primitives.

## What's genuinely unspecified (the six open points)

| ID | Surface | Size | Risk |
|----|---------|------|------|
| V1.1 | **The loop** in `pudl run` (re-observe→drift→transform→execute→repeat) | medium | mostly wiring existing one-shots into a cycle |
| V1.2 | **Termination / fixed-point** (stop at drift=∅; guard: max iters, drift monotonically shrinks) | small code | **HIGH** — correctness/safety, not LOC |
| V1.3 | **Drift-gating** (fire vs flag; severity threshold / opt-in) | small | medium policy |
| V1.4 | **Failure reporting** (applied-but-still-drifts / drift-grew / mid-loop action fail; distinct from drift) | medium | medium |
| ~~V1.5~~ | ~~Partial-apply / rollback~~ | — | ❌ **CUT** — owner decision (V1 session): out of scope, period |
| V1.6 | **`converge` field paths** (`#PluginPlan` ≈ exists via export-actions; ewe-converge `#EweTarget` newer) | small–medium | low–medium |

## Decided (V1 session) — `pudl run` CLI contract

Settles the convergence-gating surface (V1.3) and bounds V1.1.

| Invocation | Behaviour |
|------------|-----------|
| `pudl run <model>` | **observe-only** (default): populate → drift → checks → report. **No mutation.** Drift is flagged, never fired. Stops at observation fixed point. |
| `pudl run <model> --converge` | Opt into the convergence loop. Converges **all** drifted resources. |
| `pudl run <model> --converge --only a,b` | Converge **only** the named definitions. Drift still computed whole-model; Transform filters emitted actions to the selected set. |
| `pudl run <model> --converge --dry-run` | Print the **plan** (the `mu.json` `export-actions` already emits, `export_actions.go:139`) and **execute nothing**. Single pass — `observe → drift → transform → print → stop`. |

Rules:
- **`--converge` is the gate.** Convergence (production mutation) never happens without it — observe-only is the safe default. This *is* the resolution of V1.3: explicit opt-in, **no severity-threshold magic**.
- **`--only` and `--dry-run` both require `--converge`** (else error). One rule: convergence flags need the convergence gate. Prevents accidental firing by naming a resource.
- **`--only` selects on definition name** (the unit drift / `export-actions` already key on).
- **`--dry-run` is inherently single-pass.** Iterations 2+ depend on execution actually changing live state, which dry-run doesn't do — so it can only show "what iteration 1 would hand mu." `--dry-run` respects `--only`.
- Flag-name leans (not yet locked): selector = `--only` (rejected `--target`: collides with mu/build vocab).

## Decided (V1 session) — V1.2 termination + V1.1 loop structure

**Fixed point:** `drift == ∅` (every definition clean) → mark `"converged"`.
**Termination guarantee:** a hard **max-iter cap** (default 5, override `--max-iters`).
Hit cap with residual drift → mark `"failed"` (→ V1.4). The cap is the halting proof.

**Why loop (not apply-once):** re-observe after execute (a) *verifies* the live
system actually reached desired — mu reporting an action ran ≠ world changed — and
(b) handles dependency chains (create parent → child now appliable).

**Loop structure** (also closes the V1.1 re-observe-placement question):
```
populate                       # initial observe (observe-only setup already does this)
loop:
  drift
  if drift == ∅:  → converged, break        # fixed-point test at TOP
  if iters >= cap: → failed, break          # safety stop
  converge → execute
  populate                                   # re-observe at BOTTOM
```

**Monotonic-drift-shrink guard: DEFERRED** (not cut — revisit-trigger: *first real
oscillating consumer*). Argued out via dlktk dialectic
([`../dialectics/v1-2-loop-termination.ndjson`](../dialectics/v1-2-loop-termination.ndjson),
4 personas). Grounded semantics independently confirmed the lean: **cap + drift==∅
justified (IN), guard defeated (OUT).** The decisive, robust-through-steelman
arguments — (1) no worked consumer oscillates (examples 1/3/4 are all set-difference),
and (2) the cap already bounds any *future* oscillator, so the guard's only value is
saving iterations no consumer needs. Even after steelmanning the guard (set-identity
metric to kill the ambiguity objection; future-consumer "insurance" defense), CAP
re-won once the cap-subsumes-insurance counter landed. **Basis on record:** YAGNI +
cap-as-halting-guarantee.

## Decided (V1 session) — V1.4 failure reporting

Surface convergence failure **distinctly from drift**. The status enum already
carries the vocabulary, so no new statuses:

- `drifted` = world ≠ desired, **untouched** (observe signal).
- `converging` = mid-loop (export-actions ran).
- `converged` = loop reached drift==∅ (V1.2).
- `failed` = loop ran, couldn't converge (V1.4).

**Two failure modes, both → `failed` + reason:**
1. `cap_exhausted` — hit `--max-iters`, residual drift ≠ ∅.
2. `execute_error` — `mu build` returned nonzero during a converge iteration.

**Mandatory partial-state warning.** On `execute_error`, rollback being cut
(V1.5), the loop **stops, marks `failed`, leaves the half-applied state**. The
report **MUST** state the live system may be in a partial state with no rollback.
This is non-negotiable — it is the honesty the cut-rollback decision demands;
silent partial-apply would be the real failure.

**Report carries:** mode, iteration count, residual drift set, and (for
`execute_error`) the failing action + the partial-state warning. V1.4 is the
**convergence analog of V2** (per-target status / partial-failure, scoped to
observe-only) — same machinery, extended to the loop.

## Decided (V1 session) — V1.6 converge field paths

**Two ewe directions, do not conflate:**

| arm | direction | consumer | V1 status |
|-----|-----------|----------|-----------|
| **populate** | PULL state in (observe) | GitLab fetch (ex. with `#EweTarget`) | **must-have, KEPT** — `#EweTarget \| #PluginObserve`, observe-only, already specced. Not part of V1.6. |
| **converge** | PUSH / mutate out | none use ewe; all 5 use `#PluginPlan` | V1 = **`#PluginPlan` only** |

**V1 converge = `#PluginPlan` only.** Schema narrows for V1:
```
converge?: #PluginPlan          // V1
// converge?: #EweTarget | #PluginPlan   // when an ewe-converge consumer appears
```
Path is mostly shipped: `drift → ExportMuConfig (export.go:80) → MuConfig{Targets}
(toolchain-mapped) → mu.json → mu build`. `pudl export-actions` *is* this path;
`ActionSpec`/`MuConfig` already match mu's plugin protocol (export.go:141).

**V1.6 actual work (small):**
1. Write the `#PluginPlan` CUE def (plugin name + input) — README sketch only today.
2. Wire the model's `converge` arm → the export-actions invocation in the V1.1
   loop's Transform step (today export-actions runs standalone on drift reports).
3. Drop `#EweTarget` from the **converge** union for V1 (keep it in **populate**).

**ewe-converge (mutate via ewe): DEFERRED** — zero consumers (YAGNI / default-to-cut,
cf. E5, Tier-2, `#Plugin`). Revisit-trigger: first model needing custom mutation
logic a plugin `apply` op can't express. **ewe-populate is unaffected** — pulling
external state (GitLab) stays a first-class, must-have V1 path.

## Rough size (approximation, not commitment)

- **Breadth: ~40–50% of the observe-only effort** — fewer points (6 vs ~13),
  several reuse shipped pieces.
- **Difficulty: front-loaded with the project's hardest cell.** Observe-only
  was broad-but-mechanical; convergence is narrow-but-sharp. **V1.2 (loop
  termination/correctness)** carries the real risk. (V1.5 rollback was **cut** —
  out of scope; the system's production-mutation risk is acknowledged but not
  guarded against in V1: execute is best-effort, failures are *reported* (V1.4),
  not *undone*.) V1.2 likely warrants a dialectic before any code.

## One grounding caveat to check when V1 opens

This sizing assumes `pudl drift check` + `export-actions` are **loop-ready as-is**.
If their one-shot internals bake in assumptions that fight iteration (e.g. the
`"converging"` state is not re-enterable), V1.1 grows. Verify with a grounding pass
at the start of V1; it would not change the overall "small surface, two hard cells"
shape.

**Resolved (V1 session, grounding pass):** one-shots are **loop-ready as-is**.
`UpdateStatus` (`pudl/internal/database/catalog_status.go:18`) is a blind set with
validity-check only — **no FSM transition guard** — so `converging→drifted→converging`
cycles freely across iterations. `drift check` (`checker.go:117`) and
`markConverging` (`export_actions.go:153`) are both idempotent / re-enterable. V1.1
stays "wiring existing one-shots into a cycle"; no growth. **Bonus:** the valid-status
set already includes `"converged"` and `"failed"` (line 21) but **nothing writes
them** — they are the pre-existing terminal vocabulary for V1.2 (fixed-point reached →
`converged`) and V1.4 (failure → `failed`). No schema migration needed.
