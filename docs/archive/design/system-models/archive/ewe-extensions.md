# Extending ewe to realize the populator vision

Response to the [adversarial review](adversarial-review.md). Critical findings
C1–C3 are addressable by **extending ewe within its public-API rewrite model** —
no internal CUE packages, no second evaluator. Two of the three mostly leverage
ewe's *existing* multi-pass engine. This note specifies the resolutions with
concrete Go signatures against ewe's real `Function` type (`ewe/function.go:51`).

## The organizing principle

ewe's defining mechanism: **every function result becomes CUE source text.**
`fn.Execute` → `goToCUEExpr` → spliced into `field.Value` → `format.Node` →
re-parsed (`ewe/processor.go:83-123`). Resolution is multi-pass retry over a
partial compile (`:60-69`), not a scheduler.

Therefore the ewe layer must **only ever traffic in data that is safe to render
as CUE source.** Three consequences, one per critical finding:

1. Effects return **values and lists**, never opaque control objects → fan-out
   is *data-driven* (batch), not *control-driven* (effects in comprehensions).
2. Effects are scheduled by **data dependency** (`.result` references) → ordering
   is expressed by reference, not by an imperative sequence.
3. Secrets are **references**, never values → the real secret never enters CUE
   source; reveal happens only inside sink functions.

Hold to that and ewe's elegant rewrite model is intact and sufficient.

---

## C1 — fan-out without effects in comprehensions

**Limitation:** `findCallSites` (`processor.go:144-172`) matches only *static*
`*ast.Field` values of the form `op.#X & {…}`. A call inside a `for` body is not
a static field — CUE expands comprehensions only at the final `CompileString`,
after effects have run. So `for r in repos { op.#Http{…r} }` cannot work.

**Resolution: batch effects + pure-CUE build/join.** Make per-item fan-out a
*single* effect over a *list*; build the list and join the results with pure
comprehensions (which CUE evaluates in the partial compile):

```cue
_repos: op.#HttpAll & { args: [{ url: "…/projects", paginate: {param:"page", until:"empty"} }] }

_reqs: [ for r in _repos.result {                       // pure CUE — no effect
    { url: "…/projects/\(r.id)/protected_branches", headers: { "PRIVATE-TOKEN": _tok.result } } } ]

_prot: op.#HttpBatch & { args: [_reqs] }                // ONE effect, N requests → list

result: [ for i, r in _repos.result {                   // pure CUE — join by index
    { _schema: "git.repository.gitlab", name: r.path_with_namespace, protections: _prot.result[i] } } ]
```

`_reqs` is a pure comprehension over the now-concrete `_repos.result`;
`#HttpBatch` is a single static call site that resolves once `_reqs` is concrete
in a later pass; the join is pure. **This works with the current engine** — the
only new piece is the Go batch primitive, which also gets to parallelize and
cache internally. Multi-level dependent fan-out (repos → branches → commits) is
just more batch passes; ewe's `maxPasses` loop handles it.

```go
// #HttpBatch: [ [reqSpec, …] ] -> [ response, … ]   (order preserved)
ewe.Function{
    Name:       "#HttpBatch",
    ParamTypes: []ewe.CUEType{ewe.TypeList},
    ResultType: ewe.TypeList,
    Execute: func(ctx context.Context, args []any) (any, error) {
        specs, ok := args[0].([]any)
        if !ok {
            return nil, fmt.Errorf("#HttpBatch: arg 0 must be a list")
        }
        out := make([]any, len(specs))
        // bounded-parallel fan-out (sem of N); each request:
        //   spec, _ := resolveSecrets(specs[i], resolver)  // reveal at the sink
        //   out[i] = doHTTP(ctx, spec)
        return out, nil
    },
}
```

**Deferred (Route B):** teaching ewe to *unroll* comprehensions whose source list
is concrete (clone body per item, substitute the loop var to literals, emit N
static call sites) is possible on the public AST API but fiddly (scoping, nesting,
`if` guards). Build it only as later sugar if authors tire of explicit batch+join.

---

## C2 — execute-time evaluation and ordering

### (i) "kept unevaluated, run at execute time" — a framing fix, not an ewe gap

ewe evaluates whenever `ProcessSource(text)` is called. The doc's
"mu holds an unevaluated `cue.Value` sub-tree" model was wrong; the correct model
is **mu stores the `ewe:` block as raw CUE source text** and calls `ProcessSource`
at execute time with a **per-execute registry** whose functions close over the
sandbox, secret resolver, and output dir:

```go
func newExecRegistry(sb *sandbox.Sandbox, sec SecretResolver, outDir string) *ewe.Registry {
    r := ewe.NewRegistry()
    r.MustRegister(secretFunc())                 // returns a reference (see C3)
    r.MustRegister(envFunc())
    r.MustRegister(httpFunc(sb, sec))            // sink: resolves secrets internally
    r.MustRegister(httpBatchFunc(sb, sec))
    r.MustRegister(writeFileFunc(sb, sec, outDir))
    r.MustRegister(outputFunc(/* dep outputs */))
    return r
}
// at execute time:
val, err := ewe.NewProcessor(newExecRegistry(sb, sec, outDir)).ProcessSource(ctx, block)
```

Context enters through *registered functions*, not CUE injection. Self-contained
source in, processed late. No ewe change, no internal packages.

### (ii) Ordering — covered by data deps, plus one small gate for the rest

ewe orders by `.result` data dependency via multi-pass retry (`processor.go:65-69`):
a call whose args reference an unresolved `.result` is skipped and retried. In the
populator domain effects almost always thread data (fetch→transform→write), so
this covers nearly everything.

For the rare **pure ordering** case (A before B though B doesn't consume A), a
small principled extension: today `extractArgsWithFallback` gates only on the
`args` field. Extend the gate so a call will not fire until **every `.result`
reference anywhere in its call struct is concrete** — which makes a reserved
`after:` field a real ordering primitive, ignored by the function body:

```cue
_notify: op.#Notify & { args: ["converged"], after: [_converge.result] }
```

Gate sketch (public AST API only):

```go
// callReady reports whether every `<name>.result` reference in the call struct
// resolves to a concrete value in the partial compile. Used in addition to the
// existing args extraction; if false, the call is skipped this pass.
func (p *Processor) callReady(cs callSite, partial cue.Value) bool {
    for _, ref := range collectResultRefs(cs.argsStruct) { // walk for selector chains ending in ".result"
        if _, err := resolvePathInValue(partial, ref); err != nil {
            return false
        }
    }
    return true
}
```

---

## C3 — taint by reference, reveal only in sinks

**Limitation (worse than it first looks):** because results become source text, a
`#Secret` that returned the real value would have `goToCUEExpr` (`convert.go:21`)
render it as a quoted literal **into the intermediate CUE source string**, which
`format.Node` writes and re-parses — plaintext secret in a source buffer, a leak
pith never has. And `convert.go` has no `pith.Secret` case anyway.

**Resolution: secrets are references in CUE; resolve + reveal only inside sinks.**

`#Secret` returns a tagged reference, never the value:

```go
// #Secret: [name] -> { "$secret": name }   (a reference; the value never enters CUE)
ewe.Function{
    Name:       "#Secret",
    ParamTypes: []ewe.CUEType{ewe.TypeString},
    ResultType: ewe.TypeStruct,
    Execute: func(_ context.Context, args []any) (any, error) {
        name, ok := args[0].(string)
        if !ok { return nil, fmt.Errorf("#Secret: name must be a string") }
        return map[string]any{"$secret": name}, nil
    },
}
```

```cue
_tok: op.#Secret & { args: ["GITLAB_TOKEN"] }     // result: { "$secret": "GITLAB_TOKEN" }
headers: { "PRIVATE-TOKEN": _tok.result }          // header holds the reference, not the value
```

The real secret never enters the CUE layer — only the inert, redaction-safe ref
does. **Sink functions** (`#Http`, `#HttpBatch`, `#WriteFile`) resolve refs
internally, immediately before the syscall, via a shared helper closing over the
per-execute resolver:

```go
// resolveSecrets deep-walks v, replacing every {"$secret": name} with the real
// secret from resolve(name). Called ONLY inside sink functions, immediately
// before the network/file write. Its output is never returned into the CUE layer.
func resolveSecrets(v any, resolve func(name string) (string, error)) (any, error) {
    switch t := v.(type) {
    case map[string]any:
        if name, ok := t["$secret"].(string); ok && len(t) == 1 {
            return resolve(name) // reveal — leaf, at the sink only
        }
        out := make(map[string]any, len(t))
        for k, val := range t {
            rv, err := resolveSecrets(val, resolve)
            if err != nil { return nil, err }
            out[k] = rv
        }
        return out, nil
    case []any:
        out := make([]any, len(t))
        for i, e := range t {
            rv, err := resolveSecrets(e, resolve)
            if err != nil { return nil, err }
            out[i] = rv
        }
        return out, nil
    default:
        return v, nil
    }
}
```

Properties:

- **No plaintext is ever rendered to source** — guaranteed, since real values
  never enter CUE. Non-sink functions never call `resolveSecrets`, so a ref that
  reaches a non-sink stays an inert tag (fail-closed).
- **No `pith.Secret` on the ewe path** — drops the marshaling problem; pith's
  taint type stays pith's concern.
- **It is swamp's Vault model** ("encrypted secret storage, *referenced by
  expression*") and matches mu's existing sealed-input refs (`env:…`, `pass:…`).
- **Fixes the caching finding (M1) for free** — the body source contains only
  refs, so hashing the action body is safe: refs in the cache key, values out —
  mu's existing sealed-input rule.

Defensive guard (fail-closed): keep a sentinel `revealedSecret` type that
`resolve` could wrap returns in, and have `goToCUEExpr` **error** if it ever sees
one. Normally never triggered (sinks consume secrets and return responses, not
secrets), but it turns "a future sink forgot the discipline" into a loud error
rather than a silent leak.

---

## Engine-change summary

| Finding | Resolution | ewe change |
|---|---|---|
| C1 fan-out | batch effects + pure-CUE build/join | none — new Go funcs (`#HttpBatch`, `#Plugin` batch) |
| C2(i) deferral | mu stores ewe block as source text; per-execute registry via closures | none — doc/integration reframe |
| C2(ii) ordering | gate call resolution on all `.result` refs in struct → `after:` | small — `callReady` in the pass loop |
| C3 taint | secret-by-reference; `resolveSecrets` in sinks; `goToCUEExpr` guard | small — new convention + one helper + a guard case |

No internal CUE packages. No second evaluator. The biggest *new code* is the set
of sink functions (which mu needs regardless), and `callReady` is a few dozen
lines reusing `resolvePathInValue`.

---

## Worked: example 5 (GitLab governance), corrected

The flagship example the review showed could not run, re-expressed in the
batch + secret-reference form — and it now runs on the extended engine:

```cue
import "op"

populate: {
    sealed_inputs: { GITLAB_TOKEN: "env:GITLAB_TOKEN" }
    plan: [{
        id: "fetch", outputs: ["repos.json"], network: true, impure: true
        ewe: {
            _tok: op.#Secret & { args: ["GITLAB_TOKEN"] }       // reference

            _repos: op.#HttpAll & { args: [{
                url:      "https://gitlab.com/api/v4/groups/garner-health/projects"
                query:    { include_subgroups: true, per_page: 100 }
                headers:  { "PRIVATE-TOKEN": _tok.result }
                paginate: { param: "page", until: "empty" }
            }]}

            // pure CUE: build one request spec per repo
            _reqs: [ for r in _repos.result if r.default_branch != null {
                url:     "https://gitlab.com/api/v4/projects/\(r.id)/protected_branches"
                headers: { "PRIVATE-TOKEN": _tok.result }
            }]

            // ONE batch effect: N protected-branch fetches → list (parallel in Go)
            _prot: op.#HttpBatch & { args: [_reqs] }

            // pure CUE: join repos with their protections by index
            _out: [ for i, r in [ for r in _repos.result if r.default_branch != null { r } ] {
                _schema:        "git.repository.gitlab"
                name:           r.path_with_namespace
                default_branch: r.default_branch
                protections:    _prot.result[i]
            }]

            write: op.#WriteFile & { args: ["\(op.#Env.result.MU_OUT)/repos.json", json.Marshal(_out)] }
        }
    }, "action/emit"]
}
```

Effects: `#Secret` (ref), `#HttpAll` (paginate in Go), `#HttpBatch` (fan-out in
Go), `#WriteFile` (sink). Everything between is pure CUE. The secret is a
reference throughout, revealed only inside `#HttpAll`/`#HttpBatch`/`#WriteFile`.
No effect inside a comprehension; ordering by data deps; cacheable refs.

---

## What this does *not* solve

- **Convergence (M2)** is independent of the engine and still unimplemented
  end-to-end. Ship observe-only populators on the extended ewe first; tackle the
  `desired`/`converge` loop afterward.
- **Inline `#Plugin` (M3)** still needs subprocess/sandbox lifecycle from inside
  an action; prefer the dataflow form (plugin `observe` as its own action, ewe
  consumes its output) until that lifecycle is designed. A `#PluginBatch` mirrors
  `#HttpBatch` once it exists.
- **Comprehension unrolling (C1 Route B)** is deferred sugar, not required.
- **`#Now` determinism** still needs a seed-in-cache-key story (minor).

## Sequencing

1. ewe: `callReady` gate + `goToCUEExpr` secret guard (small, in ewe repo).
2. mu: per-execute registry + sink functions (`#Secret`, `#Env`, `#Http`,
   `#HttpAll`, `#HttpBatch`, `#WriteFile`, `#Output`) with `resolveSecrets`.
3. mu: `ewe` action body kind (store source text, process at execute).
4. Validate against the corrected example 5 end to end (observe-only).
5. Only then: `#SystemModel`, `pudl run`, convergence.
