# Adversarial review #2 — pressure-testing the ewe-extensions rebuttal

Skeptical, **empirically-verified** review of
[ewe-extensions.md](ewe-extensions.md) (the rebuttal to
[adversarial-review.md](adversarial-review.md)). The reviewer wrote and ran
throwaway tests against the real `ewe` engine. **Verdict up front: the rebuttal
does not rescue the ewe-in-CUE approach.** Its flagship "corrected example 5"
does not run on the current engine — and not for the reason the first review
gave. There is a deeper defect in `extractArgsWithFallback` that the rebuttal
never names and its fixes do not touch.

---

## CRITICAL

### CRIT-1 — Arg extraction is all-or-nothing per element; a `.result` reference embedded inside *any* composite arg (struct / list / interpolated string) is unresolvable. Breaks C1, C2, C3, and example 5 at once.

This is the crux, and the rebuttal misses it entirely. `extractArgsWithFallback`
(`ewe/processor.go:176-214`) resolves each arg element by exactly two routes:

1. `cueExprToGo(elt)` — a pure-AST literal converter (`convert.go:119-186`).
   Handles `BasicLit`, `Ident(true/false/null)`, `ListLit` of literals,
   `StructLit` of literals, negative numbers. **No case for `*ast.SelectorExpr`,
   `*ast.Interpolation`, `*ast.Comprehension`, or `CallExpr`.** Any of those
   *anywhere inside* the element fails the whole conversion.
2. Fallback `selectorExprToPath(elt)` → `resolvePathInValue` — but
   `selectorExprToPath` (`processor.go:218-235`) only matches a **top-level**
   `Ident`/`SelectorExpr` chain. For a `StructLit`/`ListLit`/`Interpolation` it
   returns `""` and the code bails (`processor.go:203`).

So an arg element resolves **only if** it is (a) wholly literal, or (b) a bare
top-level reference like `step1.result`. A composite arg that *embeds* a
`.result` at any depth satisfies neither. Verified empirically:

- `args: [{ headers: { "PRIVATE-TOKEN": tok.result } }]` — the rebuttal's **own
  C3 "dodge,"** README §populator, and four of five examples → `cueExprToGo`
  fails (`unsupported expression type *ast.SelectorExpr`); `selectorExprToPath`
  returns `""` → `ewe: pass 1: no progress, 1 unresolved call: #Http`. **The
  whole-value-ref header form does not run.**
- `args: ["\(op.#Env.result.MU_OUT)/repos.json", json.Marshal(_out)]` — the
  `#WriteFile` line in the rebuttal's "now-runs" example 5 → arg 0 is
  `*ast.Interpolation` (unconvertible, `selPath=""`); arg 1 is a `CallExpr`.
  Unresolvable.

`callReady`/`collectResultRefs`/secret-by-reference address ordering and
leakage; **none touch `extractArgsWithFallback`**, where every example actually
dies. Severity: **Critical** — single common cause behind C1, C2(i), C3, and
example 5; the rebuttal is silent on it.

### CRIT-2 — The rebuttal's exact C1 code is double-broken: hidden fields (`_foo`) are unresolvable, and inline comprehension args hit CRIT-1.

The rebuttal writes everything as hidden fields (`_repos`, `_reqs`, `_prot`,
`_tok`, `_out`). Resolution uses `cue.Value.LookupPath` (`processor.go:237-246`),
and **CUE rejects hidden labels in LookupPath**: verified, `_reqs` →
`invalid path: hidden label _reqs not allowed`. So `_prot: op.#HttpBatch &
{args:[_reqs]}` can never resolve `_reqs`. Running the rebuttal's exact C1 shape
verbatim → `ewe: pass 1: no progress, 1 unresolved call: #HttpBatch`.

Rewritten with **non-hidden** names (`reposx`/`reqsx`/`protx`) it **does**
resolve (`protx.result = ["got 2 specs"]`) — but only because `reqsx` is a *bare
top-level reference*, so the fallback `resolvePathInValue` materializes the
comprehension via the partial compile. Inline the comprehension into the arg
(`args:[[for r in …]]`) instead of naming it → CRIT-1 → fails.

So C1 "works on the current engine" is **false as written** and true only under
an unstated rewrite: no hidden fields, and every cross-effect value a
separately-named top-level field referenced as a *bare* selector — never
embedded, never interpolated. Materially smaller, clumsier language than the
examples imply. Severity: **Critical**.

### CRIT-3 — Secret-by-reference cannot survive any transformation, and the untransformed case doesn't run either.

(a) **Untransformed**: `headers: { "PRIVATE-TOKEN": _tok.result }` is a
struct-arg-with-ref → CRIT-1 → does not run. The "easy" case is already broken.

(b) **Any transformation reveals the secret in CUE.** `resolveSecrets`
(ewe-extensions:190-214) replaces a map of *exactly* `{"$secret": name}`,
`len==1`, leaf-only, inside sinks. That works **only for a whole, standalone
secret value.** Real APIs need the secret as a *substring*:
- `"Bearer \(_tok.result)"` — you cannot `\()`-interpolate a struct (CUE type
  error); and string interpolation happens at the final `CompileString`, i.e. in
  the CUE layer, which would require the real value in CUE source → C3 returns.
- basic-auth `base64(user+":"+pass)`, `"token <x>"`, query params
  `?private_token=<x>`, secrets embedded in JSON bodies — all need the secret as
  a substring, which a leaf-only `len==1` match cannot produce. Building them
  forces a reveal in CUE → **C3 in full**.

So secret-by-reference is not "strictly better" — it is strictly *narrower*,
working for exactly the one shape (whole-value header) that CRIT-1 also blocks.
It excludes Bearer, basic auth, signed-URL params, and most body auth. Severity:
**Critical** (a narrowing framed as a strict win).

(c) **Cache relocation, not fix.** `ComputeActionKey` (`dag/actionkey.go:42-119`)
hashes sealed-input **refs/modes, not values** (`:91-104`). If `GITLAB_TOKEN`
rotates, `env:GITLAB_TOKEN` is unchanged → identical key → stale-auth assumption
cached. M1 relocated, not eliminated. (Largely moot — see MAJOR-1.)

---

## MAJOR

### MAJOR-1 — The whole caching discussion is moot: every populator is `impure`, and impure skips cache.

`executeAction` (`dag/executor.go:222-237`): cache lookup gated `if !a.Impure`;
storage gated `if e.Store != nil && !a.Impure` (`:382`). Every populator sets
`impure: true` (README:324, ewe-extensions example 5:264). **No ewe populator is
ever cached.** The rebuttal's "fixes M1 for free" (C3) and "the batch primitive
caches internally" (ewe-extensions:57) argue about a never-executed code path.
ewe has no memoization (`Function.Cacheable`, `function.go:59`, is never read).
Severity: **Major**.

### MAJOR-2 — C2(i) "store the ewe block as raw source text" is hand-waved.

mu loads `mu.cue` *through CUE*. To recover "raw source of the nested `ewe:`
field" you must either (a) re-read the file and byte-slice the field span
yourself (`cue.Value` does not hand back verbatim sub-node source; `format.Node`
on the *evaluated* value would already have unified/normalized it — defeating the
point of keeping effects unevaluated), or (b) make `ewe:` a string field — not
type-checked CUE, eroding "the program is CUE." Also: processed as isolated
source via a per-execute registry, the block can reach upstream DAG
`target/output` only through a registered `#Output` function — left as a `/* */`
comment (ewe-extensions:105). A non-trivial new effect surface, presented as a
"framing fix, no ewe change." Severity: **Major**.

### MAJOR-3 — The `after:`/`callReady` gate is real but cosmetic, and rests on the broken substrate.

`callReady`/`collectResultRefs` inherit CRIT-2 (hidden `_converge.result`
unresolvable) and CRIT-1 (nested refs). And they add no new sequencing: ewe
already retries calls whose args aren't concrete (`processor.go:65-69`). `after:`
only gates on refs *outside* `args`, and only orders `#Notify` after `#Converge`
if `#Converge` has a resolvable `.result` — which, for any converge body with a
struct/interpolated arg, it won't (CRIT-1). Sound in isolation, useless on the
real substrate. Severity: **Major** (described as closing the ordering gap; the
gap that bites is arg resolution).

### MAJOR-4 — Batch splice-back and multi-level convergence are asserted, not shown; `maxPasses=16` is a hard ceiling.

`maxPasses` is a constant 16 (`processor.go:17`). Each dependent batch level
consumes passes; every pass re-parses, re-runs `compilableSource` (full AST walk
+ reformat), and recompiles the whole source. `#HttpBatch` over hundreds of repos
splices a huge `ListLit` back as source text (`:88-104` → `format.Node` →
re-parse), which the next level must re-parse and re-resolve each pass. No ewe
test exercises large lists or chained batches; termination of nested
batch-comprehension chains within 16 passes is unverified. "Just more batch
passes" (ewe-extensions:58) is an assertion. Severity: **Major** (scalability +
correctness unproven).

---

## MINOR

- **MIN-1** — The `goToCUEExpr` secret guard guards the case that can't happen
  (a sink returning a revealed secret — sinks return responses) and misses the
  one that can (a user revealing a secret in CUE to build `"Bearer \(tok)"`,
  which leaks via `format.Node`/`CompileString`, invisible to a Go-side guard).
- **MIN-2** — No partial-failure semantics for batch: `fn.Execute` returning an
  error aborts the entire `ProcessSource` (`processor.go:84-86`). Inventory
  fan-out over hundreds of repos needs per-item tolerance. Unspecified.
- **MIN-3** — `#Now` determinism: combined with MAJOR-1 (never cached) there is
  no cache key to seed; "minor" framing hides that there's no mechanism at all.
- **MIN-4 (sub-hypothesis correction)** — `.result` *does* exist in the partial
  compile for **already-executed** calls (splice-back writes a literal `result:`
  field that survives into the next pass's `compilableSource`), and does **not**
  exist for unexecuted ones. So `.result` chaining works strictly downstream of
  an executed call (existing tests cover this). Not the blocker; CRIT-1/CRIT-2
  are.

---

## The meta-claim — dishonest framing

The headline ("addressable by extending ewe within its public-API rewrite
model — no internal CUE packages, no second evaluator… ewe's elegant rewrite
model is intact and sufficient") does not survive the tally. To make example 5
*run* you need, minimum:

1. Rewrite `extractArgsWithFallback` to resolve refs **nested inside**
   struct/list/interpolation args (CRIT-1) — the heart of the engine, not a
   peripheral helper.
2. Hidden-field support in the resolution path, or ban hidden fields across all
   populator code (CRIT-2).
3. Comprehension-aware arg handling for inline (not just bare-named)
   comprehensions (CRIT-2).
4. `callReady` + `collectResultRefs` (C2ii).
5. A sink-function suite + `resolveSecrets` + per-execute registry + `#Output`
   wiring (C2i, C3).
6. mu-side raw-source extraction of a sub-node + execute-time `ProcessSource`
   (C2i, unspecified).
7. The `goToCUEExpr` secret guard (C3).
8. Batch primitives with parallelism, per-item error handling, pagination-in-Go.

Items 1-3 are surgery on the core of the rewrite engine
(`extractArgsWithFallback`, `cueExprToGo`, the resolution model), not "a few
dozen lines reusing `resolvePathInValue`." The rebuttal's summary table lists C1
and C2(i) as "**none — new Go funcs / doc reframe**." That is the dishonest line:
**C1 requires the most invasive engine change of all, and example 5 does not run
without it.**

---

## Verdict

**The rebuttal confirms the first review's path (a): you are building a
substantially new effectful evaluator on a pure-rewrite library not designed for
nested-reference args, ordering, or secrets.** Both prior docs mis-aimed:

- **Under-stated:** the real blocker is not "effects inside comprehensions" —
  it's that `extractArgsWithFallback` can't resolve a `.result` ref embedded in
  *any* composite arg. That kills the *non*-comprehension examples too (every
  `headers: {token: x.result}`). The rebuttal sidesteps the comprehension framing
  and walks into this deeper defect without naming it.
- **Over-stated:** the comprehension build/join *is* viable for bare-named,
  non-hidden, top-level list fields (verified running). So "ewe structurally
  cannot express N-fan-out" is too strong — it can, under heavy restriction.

### Minimal honest version that ships

Path (b) from review #1, with the arg-resolution defect made explicit:

1. **Forbid effects in comprehensions** (both docs agree).
2. **Also forbid any `.result` reference embedded inside a composite arg.** Every
   cross-effect value must be a separately-named, **non-hidden**, top-level field
   passed as a *bare* arg element (`args: [reqs]`, never `args: [{x: reqs}]` or
   `args: ["\(reqs)"]`). This is the contract `extractArgsWithFallback` enforces
   today — which makes Bearer auth, interpolated paths, nested headers, and
   `json.Marshal(_out)` **non-expressible** without engine work. Be honest about
   that scope.
3. **Per-item fan-out as separate mu DAG actions**, or a Go-internal
   `#HttpAll`/batch taking only literal/bare-ref args and looping in Go.
4. **Secrets:** secret-by-reference only for whole-value headers; anything needing
   concat/encode/interpolation reveals in-Go inside the sink with the secret never
   named in CUE — i.e. the *request shape itself* (header name, encoding) is
   Go-side, not CUE-side. Equivalently: keep pith's reveal-at-sink discipline; do
   not pretend `{"$secret":name}` survives transformation.
5. **Scope convergence out of v1** (both docs agree; unimplemented end-to-end).
6. Observe-only on this is shippable **only after** `extractArgsWithFallback` is
   either reworked (engine work) or the language is restricted to bare-ref args —
   and the restricted language is much smaller than README/examples advertise.

The rebuttal's real value: it kills the "second evaluator / internal packages"
strawman and shows secrets-as-refs is the right *direction*. Its failure: it
declares victory ("now runs on the extended engine") on examples that do not run,
attributes zero engine change to C1 when C1 needs the most invasive change, and
frames a secret-model narrowing as a strict win. **Treat C1/C3 as still open
until `extractArgsWithFallback` is reworked to resolve nested references — that
one function, not comprehensions or `callReady`, is the redesign the project is
avoiding.**

## Evidence index

- `ewe/processor.go:176-214` — `extractArgsWithFallback` (all-or-nothing per element)
- `ewe/processor.go:218-246` — `selectorExprToPath` / `resolvePathInValue` (top-level only; hidden labels rejected)
- `ewe/processor.go:248-278` — `compilableSource` (strips calls to struct; `.result` absent until executed)
- `ewe/processor.go:17` — `maxPasses = 16`
- `ewe/processor.go:84-86` — any function error aborts the whole pass
- `ewe/convert.go:119-186` — `cueExprToGo` missing `SelectorExpr`/`Interpolation`/`Comprehension`/`CallExpr` cases
- `ewe/function.go:59` — `Cacheable` declared, never read
- `mu/internal/dag/executor.go:225,382` — `impure` skips cache lookup and store
- `mu/internal/dag/actionkey.go:42-119` — values excluded from cache key (`:91-104`)
