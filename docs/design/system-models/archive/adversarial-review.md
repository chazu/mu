# Adversarial review — System Models / ewe-in-CUE

Skeptical code-verified review of [README.md](README.md),
[examples.md](examples.md), and [../expressiveness-sketches.md](../expressiveness-sketches.md).
Every load-bearing claim was checked against the actual source of `ewe`, `mu`,
and `pudl` (not the docs). Findings ranked Critical / Major / Minor. The docs
are **not** yet amended for these — this file records the findings; the redesign
is a follow-up.

**One-line verdict:** the product vision is sound and the observe-only half is
backed by real primitives, but the headline execution mechanism rests on a model
of `ewe` that does not exist, and the convergence half rests on machinery that
does not exist.

---

## CRITICAL

### C1. ewe cannot run effects inside comprehensions — the primary populator pattern is unsupported

**Claim attacked:** README §"populator surface" and examples.md §5 (`_withprot`):
`for r in _repos.result { let prot = op.#Http & {...}; {... protections: prot.result} }`.
The whole pitch ("pure transforms are CUE comprehensions, effects are `op.#Func`
calls, mixed freely") depends on effect calls *inside* comprehensions.

**Why it's wrong:** ewe's `findCallSites` (`processor.go:144-172`) only registers
a call when an `*ast.Field`'s `.Value` is directly a `BinaryExpr` matching
`pkg.#Name & {struct}`. An `op.#Http` buried in a `for` clause is not a static
field at a stable path. The rewrite mutates `cs.field.Value` in place
(`processor.go:93`) on an AST parsed once per pass — it cannot expand a
comprehension into N fields, and the per-item calls don't exist as nodes until
CUE evaluates the comprehension, which only happens in the final
`CompileString` (`processor.go:131`), long after all effects have run. No test
or example for comprehension-driven effects exists in ewe.

**Impact:** Example 5 — the flagship "gist generalized" — does not run. "N
fetches, one per item from a prior fetch" is the most common real shape and ewe
structurally cannot express it.

### C2. ewe is an eager pre-compile source rewriter, not a lazy CUE-eval-time evaluator — the "kept unevaluated, run at execute time, ordering from data deps" framing is inverted

**Claim attacked:** README:120-128 and expressiveness-sketches.md (the ewe action
body kind): mu "keeps the `ewe:` block unevaluated at load," then "runs it
through the ewe evaluator at execute time… Effect ordering falls out of CUE data
dependencies." Open-question in README even hand-waves "it should, via data deps."

**Why it's wrong (`processor.go:46-136`):** ewe parses CUE → AST, finds call
sites, **runs the Go functions eagerly in a Go loop** (`fn.Execute`, line 83),
splices literal results back as concrete CUE, reformats to a *source string*, and
only at the very end (line 131) hands that fully-resolved string to
`cuecontext.CompileString`. CUE never "calls" anything — by the time CUE's
evaluator runs, all effects are already constants. Consequences:

- "Run it through the ewe evaluator at execute time" works *only* if mu extracts
  the block's raw source text and feeds it to `ProcessSource`. There is **no API
  to defer evaluation of a sub-path of an already-loaded `cue.Value`**
  (`ProcessValue` is in ewe's PLAN.md but never implemented). The doc's model of
  "mu holds a CUE value with an unevaluated ewe sub-tree" has no hook in ewe.
- "Ordering falls out of CUE data dependencies" — **only via `.result`
  chaining**, through a fixed-point multi-pass loop (`maxPasses=16`): a call whose
  args reference an unresolved `.result` is skipped this pass and retried next
  (lines 67-69). There is no topological scheduler. **Pure effect ordering (A
  before B though B doesn't consume A's value) is inexpressible** — there is no
  `#Seq`, and the doc explicitly celebrates not needing one.

### C3. The taint/Secret type does not survive the ewe/CUE boundary — "reuse pith's Secret/Reveal" is a from-scratch reimplementation

**Claim attacked:** README:128 and sketches: "taint carries via pith's `Secret`
type"; phase 1: "Reuse pith's `Secret`/`Reveal`."

**Why it's wrong:** `pith.Secret` is `struct{ inner any }` with an **unexported
field and no `MarshalJSON`** (`pith@v0.3.0/taint.go:13`). Taint safety in mu
today is purely convention: every pith sink word calls `pith.Reveal(v)` before
the syscall. No language- or marshaling-level enforcement. Across a *different*
evaluator:

- ewe's `goToCUEExpr` (`convert.go`) handles string/number/bool/`[]any`/
  `map[string]any` — **no case for `pith.Secret`** → either errors or the value
  was already unwrapped to a plain (untainted) string flowing through CUE with no
  redaction.
- If a `Secret` reached `json.Marshal`, it serializes to `{}` — silent data
  loss, not redaction.

**Impact:** every example's `headers: { "PRIVATE-TOKEN": _token.result }` either
passes a bare revealed string through CUE (taint lost — can leak into traces,
diagnostics, errors) or fails conversion. The ewe arm must re-implement the
entire reveal-at-sink discipline from scratch in a different value model. It
inherits nothing for free. This is the riskiest single line in the design
because it sounds done.

---

## MAJOR

### M1. Caching + non-determinism has no coherent story

`ComputeActionKey` (`actionkey.go:51-55`) JSON-marshals the entire body verbatim
into the key — for an ewe body that's the program source, `#Http`/`#Now` calls
included. Two identical-source bodies → same key → a cache hit serves a **stale
HTTP response or frozen `#Now`**. The only escape is `impure: true`, which skips
caching (`executor.go:225,382`) — and every populator example sets it. So **no
ewe populator is ever cached**, contradicting "cached by input hash." ewe itself
has no memoization (`Function.Cacheable` is declared but never read). `#Now`
"seedable for determinism/caching" specifies no mechanism.

### M2. The convergence half is built on machinery that does not exist

There is **no loop anywhere.** `pudl drift check` is one-shot
(`drift/checker.go:117-123`); `pudl export-actions` emits mu.json once and marks
defs `"converging"` (`export_actions.go:151-167`), nothing re-observes;
`mu Observe` is single-pass (`coordinator.go:746`). Grep for
ACUTE/Accumulate/reconcile-loop across mu's Go = zero hits; BRICK is opaque
passthrough metadata (`config/types.go:49-50`). brick-ecosystem.md is
reference/aspirational, present-tense. So "the BRICK loop already does this" is
false — the loop is at most a future cron pipeline. The doc admits
termination/gating are open but understates it: it's not "semantics to iterate,"
it's "the convergence loop is unimplemented end to end." (Read-after-write within
a run is fine — SQLite, no caching; cross-tool populate→converge→re-observe
ordering has no orchestrator.)

### M3. Inline `#Plugin` (the "extensibility keystone") has no execution machinery

Plugins start only via `Manager.Start` at coordinator/plan/execute boundaries
(`manager.go:131-199`) — never from inside an action body. The execute-phase
vocabulary has http/exec/cas/file/secret/env/format drivers but **no
plugin-speaking driver**. Inline `#Plugin` is entirely new machinery (subprocess
lifecycle, sandbox ownership, NDJSON from inside a sandboxed action). The doc
flags this open and recommends the dataflow form — but **four of five examples
use the inline form anyway**. Examples contradict the recommendation.

### M4. `#DatalogQuery` bridge is asymmetric — no driver exists

`pithdriver/register.go:15-30` registers `catalog`, `fact`, `schema`, `drift` —
**no datalog driver word.** The other four rest on standalone-callable Go funcs;
`#DatalogQuery` must be wired fresh to `datalog.Evaluate`/`pkg/factstore`, and
the caller must also load rule files. Presented as existing parity; it's the one
that doesn't exist.

---

## MINOR

- **m1. `pudl run` orchestrating mu is the cleanest part — but "charter-consistent" is oversold.** The precedent is real (`cmd/memory.go:168-186` shells `mu build`), so it's not a 2-headed-control problem. But that precedent's error handling is one line — exit-code propagation, no partial-failure, no per-target status, no rollback. A convergence loop (M2) needs far more. The boundary is fine; state/error handling is not a solved consequence of the precedent.
- **m2. Example 3 (TLS) double-jeopardy with C1.** `op.#Exec` (openssl) inside `for d in _domains` — same comprehension-driven-effect problem, plus the `not_after` string → `days_until` Datalog math is unspecified ("parsed downstream").
- **m3. `until:"empty"` is the only viable paging form.** GitLab/GitHub/Cloudflare use cursor/keyset/`Link` headers. `#HttpAll` must paginate internally in Go (ewe can't drive it from CUE), so every paging strategy is a Go feature, not a config knob, until built.
- **m4. "Delete pith" is premature.** Given C1-C3, pith is the only thing that actually works today. The "execute-time cost" reprieve is the least important reason to keep it; sequencing deletion behind "ewe proves out" must not be treated as a formality.

---

## Biggest risk + path forward

**Biggest risk: C1 + C2 together.** The core promise — "effects as `op.#Func`,
pure transforms as CUE comprehensions, mixed freely, ordered by data deps, run at
execute time" — assumes ewe is a lazy, comprehension-aware, deferred
eval-time effect evaluator. It is actually an eager, whole-source, pre-compile
AST rewriter that cannot see effects inside comprehensions and runs everything
before CUE evaluates. The flagship example doesn't run; ordering only works
through value-threading; there's no deferral hook. The populator surface, four of
five examples, and the `#SystemModel.populate` story all inherit this.

**Two paths (engine needs rethinking, not iterating):**

- **(a) Extend ewe substantially** — comprehension expansion, a real dependency
  scheduler, deferred sub-tree evaluation, a taint-aware value/convert path. At
  that point you're building a new evaluator and the "zero new syntax, reuse what
  exists" argument that beat jq/expr-lang evaporates.
- **(b) Restrict the effect model (recommended).** `#HttpAll`/`#Plugin` paginate
  and loop *internally in Go* (ewe can return a list); **forbid effects inside
  `for`**; do per-item fan-out as separate mu DAG actions (the doc's own
  "dataflow preferred" path). Charter-clean and viable — but a *much* smaller
  language than the examples imply; example 5's "two HttpAll joined in CUE" must
  become two actions.

**Also:** scope the **convergence half out of v1** (unimplemented end to end —
M2); ship observe-only first (primitives support it); **demote pith deletion**
behind a working observe-only spike. As written, phases 1-5 quietly depend on the
unimplemented loop and the non-existent ewe semantics.

**Verdict: the vision survives; the headline mechanism does not.** Treat C1-C3 as
design-invalidating until ewe is either extended (a) or the effect model is
restricted to Go-internal loops with effects banned from comprehensions (b).

---

## Evidence index

- `ewe/processor.go:46-174` — eager rewrite loop, comprehension-blind call-site matcher
- `ewe/convert.go` — `goToCUEExpr` has no `pith.Secret` case
- `pith@v0.3.0/taint.go:13` — `Secret struct{ inner any }`, no `MarshalJSON`
- `mu/internal/dag/actionkey.go:51-55` + `executor.go:225,382` — whole-body hashing; `impure` skips cache
- `mu/internal/coordinator/manager.go:131-199` — plugins start only at coordinator level
- `pudl/internal/drift/checker.go:117-123` + `cmd/export_actions.go:151-167` — one-shot, no loop
- `pudl/internal/pithdriver/register.go:15-30` — no datalog driver
- `pudl/cmd/memory.go:168-186` — the real `mu build` shell-out precedent
