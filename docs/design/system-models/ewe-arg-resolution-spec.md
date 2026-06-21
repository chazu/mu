# Spec: rework ewe arg resolution (fixes CRIT-1, CRIT-2)

Targets the **ewe repo** (`/Users/chazu/dev/go/ewe`). Addresses the blocker named
by [adversarial-review-2.md](adversarial-review-2.md): `extractArgsWithFallback`
cannot resolve a `.result` reference embedded inside a composite arg
(struct/list/interpolated string), and hidden fields (`_foo`) are rejected by
`LookupPath`. This single function — not comprehensions, not `callReady` — is the
redesign the project kept avoiding.

## Scope

**Fixes:** CRIT-1 (nested-reference args), CRIT-2 (hidden fields + named
comprehension build/join). After this, `headers: {"PRIVATE-TOKEN": _tok.result}`,
`"\(env.result.MU_OUT)/repos.json"`, `json.Marshal(_out)`, and hidden `_foo`
fields all resolve.

**Does NOT fix (out of scope, tracked separately):**
- **CRIT-3 secret-as-substring** — `"Bearer \(_tok.result)"` still can't work,
  because `_tok.result` is a struct ref and CUE can't interpolate a struct into a
  string. Needs the separate "secret template" sink design (below).
- **MAJOR-1 caching** — populators are `impure`; impure skips cache. Unchanged.
- **MAJOR-4 maxPasses/large lists** — pass-count and big-splice cost unchanged;
  noted under Risks.
- **Effects inside comprehensions** — explicitly rejected with a clear error,
  not supported.

## Root cause (recap, verified)

`extractArgsWithFallback` (`processor.go:176-214`) resolves each arg element by:
1. `cueExprToGo(elt)` — AST literal converter (`convert.go:119-186`), no case for
   `SelectorExpr`/`Interpolation`/`Comprehension`/`CallExpr`; fails if any appear
   *anywhere inside* the element.
2. fallback `selectorExprToPath`+`resolvePathInValue` — only matches a *top-level*
   ref; returns `""` for a struct/list/interpolation.

So an element resolves only if wholly literal or a bare top-level ref. Everything
real (nested headers, interpolated paths, `json.Marshal`, hidden names) dies.

## Design: evaluate args as a CUE value, not as AST

The partial compile (`compilableSource` → `cctx.CompileString`, `processor.go:60-62`)
already holds a fully-evaluated CUE value of the whole document, with every call
stripped to its `{args: …}` struct. CUE there evaluates structs, lists,
**interpolation**, **builtin calls** (`json.Marshal`), **references**, and
**comprehensions** for us. So instead of hand-converting the args AST, look up the
call's `args` *subpath* in that value and convert the concrete result.

```
for each call site cs:
    argsVal := partial.LookupPath(cs.path + "args")     // cs.path computed below
    if !argsVal.Exists() || argsVal.Validate(cue.Concrete(true)) != nil:
        skip this pass (dependency not yet resolved)     // existing retry semantics
    args := cueValueToGo(argsVal)                         // already handles struct/list/etc.
    run fn, splice {args: <original AST>, result: ...}    // unchanged
```

This is *less* code than patching `cueExprToGo` for every node type, and strictly
more powerful — it delegates evaluation to the embedded CUE engine, which is the
point of being CUE-hosted. `cueValueToGo` (`convert.go:233-302`) already covers
String/Bool/Int/Float/Null/List/Struct/Number, so **it needs no changes**: a
secret ref `{"$secret":"NAME"}` arrives as a map, an interpolation as a string, a
`json.Marshal` as a string.

The one genuinely new piece is computing each call site's `cue.Path`.

## Change 1 — compute `cue.Path` for each call site

`findCallSites` (`processor.go:144-174`) uses `ast.Walk`, which gives no ancestry.
Replace with a recursive traversal that threads a `[]cue.Selector` path. `callSite`
gains a `path cue.Path` field.

```go
type callSite struct {
    field      *ast.Field
    funcName   string
    argsStruct *ast.StructLit
    path       cue.Path      // NEW: full path to this field in the document
}

func (p *Processor) findCallSites(f *ast.File) []callSite {
    var sites []callSite
    pkg := packageName(f) // "" → "_" for hidden-label resolution
    var walk func(decls []ast.Decl, prefix []cue.Selector)
    walk = func(decls []ast.Decl, prefix []cue.Selector) {
        for _, d := range decls {
            field, ok := d.(*ast.Field)
            if !ok {
                continue // skip embeddings, comprehensions, etc. at this level
            }
            sel, ok := labelSelector(field.Label, pkg)
            if !ok {
                continue // dynamic/unsupported label — not addressable
            }
            here := append(append([]cue.Selector{}, prefix...), sel)

            if fn, args, isCall := p.matchCall(field.Value); isCall {
                sites = append(sites, callSite{
                    field: field, funcName: fn, argsStruct: args,
                    path: cue.MakePath(here...),
                })
                continue // do not descend into a call's own args for call-finding
            }
            // descend into nested structs / lists
            switch v := field.Value.(type) {
            case *ast.StructLit:
                walk(v.Elts, here)
            case *ast.ListLit:
                for i, e := range v.Elts {
                    if s, ok := e.(*ast.StructLit); ok {
                        walk(s.Elts, append(append([]cue.Selector{}, here...), cue.Index(i)))
                    }
                    // NOTE: a call directly as a list element has no field label →
                    // not addressable by path; reject in matchCall-in-list (rare).
                }
            case *ast.Comprehension:
                // effects inside comprehensions are unsupported — detect & error (Change 3)
                p.flagComprehensionCalls(v, here)
            }
        }
    }
    walk(f.Decls, nil)
    return sites
}
```

`labelSelector` maps an AST label to a typed selector — **this is what fixes
hidden fields (CRIT-2)**:

```go
func labelSelector(label ast.Label, pkg string) (cue.Selector, bool) {
    switch l := label.(type) {
    case *ast.Ident:
        n := l.Name
        switch {
        case strings.HasPrefix(n, "#"):
            return cue.Def(n), true            // definition
        case strings.HasPrefix(n, "_"):
            return cue.Hid(n, pkgOrUnderscore(pkg)), true  // hidden — was unresolvable
        default:
            return cue.Str(n), true
        }
    case *ast.BasicLit:
        if l.Kind == token.STRING {
            s, err := strconv.Unquote(l.Value)
            if err == nil {
                return cue.Str(s), true
            }
        }
    }
    return cue.Selector{}, false // dynamic labels, interpolated labels: not addressable
}

func pkgOrUnderscore(pkg string) string { if pkg == "" { return "_" }; return pkg }
```

`packageName(f)` reads `f`'s package clause (CUE files with no package use `_` for
hidden labels). `matchCall` is the existing `binExpr.Op==AND && matchSelector` +
`StructLit` logic, returned as `(fnName, *ast.StructLit, bool)`.

## Change 2 — replace `extractArgsWithFallback` with `resolveCallArgs`

```go
// resolveCallArgs evaluates the call's `args` list in the partial compile and
// returns it as Go values. notReady=true means a dependency (another call's
// .result) is not yet concrete — retry next pass (existing semantics).
func (p *Processor) resolveCallArgs(cs callSite, partial cue.Value) (args []any, notReady bool, err error) {
    argsVal := partial.LookupPath(cs.path.Selector(cue.Str("args"))) // path + "args"
    if !argsVal.Exists() {
        return nil, true, nil // args field/path not present yet
    }
    if e := argsVal.Validate(cue.Concrete(true)); e != nil {
        return nil, true, nil // references unexecuted .result, or otherwise incomplete
    }
    v, e := cueValueToGo(argsVal) // already handles struct/list/string/number/bool/null
    if e != nil {
        return nil, false, fmt.Errorf("args: %w", e)
    }
    list, ok := v.([]any)
    if !ok {
        return nil, false, fmt.Errorf("args must be a list, got %T", v)
    }
    return list, false, nil
}
```

Caller (`processor.go:65-106`) becomes:

```go
for _, cs := range calls {
    args, notReady, err := p.resolveCallArgs(cs, partial)
    if err != nil { lastErr = err; continue }   // collect; surfaced if pass makes no progress
    if notReady { continue }                     // retry next pass
    fn, found := p.registry.Get(cs.funcName)
    if !found { return cue.Value{}, fmt.Errorf("ewe: unknown function %q", cs.funcName) }
    result, err := fn.Execute(execCtx, args)
    if err != nil { return cue.Value{}, fmt.Errorf("ewe: executing %q: %w", cs.funcName, err) }
    resultExpr, _ := goToCUEExpr(result)
    cs.field.Value = &ast.StructLit{Elts: []ast.Decl{
        {Label: ast.NewIdent("args"),   Value: argsField(cs.argsStruct)}, // original AST, unchanged
        {Label: ast.NewIdent("result"), Value: resultExpr},
    }}
    resolved++
}
```

Splice-back is unchanged in shape (`{args, result}`); we reuse the *original* args
AST node (`argsField` extracts the `args:` field's value from `cs.argsStruct`), so
nothing about result rendering changes. `selectorExprToPath`, `resolvePathInValue`,
and the literal-only `cueExprToGo` arg path become **unused for argument
extraction** and can be removed (keep `cueValueToGo` and `goToCUEExpr`).

### Why this resolves the failing cases (verified shapes)

| Failing arg (review #2) | Why it works now |
|---|---|
| `{headers: {"PRIVATE-TOKEN": _tok.result}}` | struct evaluated by CUE → map; `_tok.result` is a concrete ref once `#Secret` ran |
| `"\(env.result.MU_OUT)/repos.json"` | CUE evaluates the interpolation to a string once `#Env` ran |
| `json.Marshal(_out)` | CUE evaluates the builtin (the `encoding/json` import survives `compilableSource`) |
| hidden `_reqs`, `_repos.result` | `cue.Hid(name, pkg)` path lookup, instead of `ParsePath` which rejects hidden labels |
| named comprehension `_reqs: [for r in _repos.result {...}]` | evaluated by CUE in the partial; `_prot: #HttpBatch & {args:[_reqs]}` resolves once `_repos.result` concrete |

## Change 3 — reject effects inside comprehensions with a clear error

Calls inside a `for`/comprehension body are unsupported (use a batch effect +
named comprehension instead). The traversal does not descend into comprehension
bodies for call-finding, so such a call would silently survive as
`op.#X & {...}` and blow up at the final `CompileString` with an opaque "op
undefined" error. Detect and fail loudly:

```go
func (p *Processor) flagComprehensionCalls(c *ast.Comprehension, prefix []cue.Selector) {
    ast.Walk(c, func(n ast.Node) bool {
        if f, ok := n.(*ast.Field); ok {
            if _, _, isCall := p.matchCall(f.Value); isCall {
                p.comprehensionErr = fmt.Errorf(
                    "ewe: function call inside a comprehension is not supported (near %v); "+
                    "compute the list with pure CUE and pass it to a batch effect "+
                    "(e.g. op.#HttpBatch) instead", cue.MakePath(prefix...))
            }
        }
        return true
    }, nil)
}
```

Surface `comprehensionErr` before the pass loop returns. This makes the
"forbid effects in comprehensions" contract explicit and friendly.

## What this unblocks

Example 5 (GitLab governance, batch+join+secret-ref form from
[ewe-extensions.md](ewe-extensions.md)) **runs** after this change: hidden fields
resolve, the nested `headers` struct resolves, the `_reqs` comprehension + batch
resolve, the interpolated `MU_OUT` path resolves, `json.Marshal(_out)` resolves.
That is the observe-only populator v1 gate.

It does **not** make `"Bearer \(_tok.result)"` work — see next.

## Follow-on (now specced separately): secrets & taint

Secret handling (whole-value refs, the secret-as-substring template, the full
`auth:` vocabulary, the fail-closed guard, and the security boundary) is resolved
in its own document: **[ewe-secrets-spec.md](ewe-secrets-spec.md)** (ledger S1).
It supersedes the earlier one-paragraph `$secretTemplate` sketch that lived here.

## Change 4 — rider fixes (same PR)

Two small engine fixes ride this change; both surfaced while validating Change 2.

**(a) `cueValueToGo` quoted-label bug (ledger E6).** `convert.go:284` keys structs
with `iter.Selector().String()`, the *source* form — a quoted label
`"PRIVATE-TOKEN"` becomes Go map key `"\"PRIVATE-TOKEN\""`, breaking header names
the moment args are decoded. Unquote string labels:

```go
sel := iter.Selector()
key := sel.String()
if sel.Type() == cue.StringLabel {
    key = sel.Unquoted()
}
result[key] = val
```

**(b) Parameterize `maxPasses` (ledger E3).** Make it a `Processor` field with a
default of 16 (constructor option), not a package constant. Passes-needed ≈ the
longest `.result` dependency chain (the pass loop resolves every ready call each
pass), not the call count — 16 is generous for realistic populator depth, but a
raisable knob removes it as a hard ceiling. The large-list splice *cost* is left
to measure-first (benchmark a 500-item batch+join); the out-of-band result store
is the escape hatch if it bites (see Risks).

## Tests to add (ewe repo)

1. Nested ref in struct arg: `f: op.#Echo & {args:[{h:{k: s.result}}]}` resolves
   after `s` runs.
2. Interpolated string arg referencing `.result`.
3. Builtin call arg (`json.Marshal(x)`), with `encoding/json` import.
4. Hidden-field call + hidden-field reference (`_a`, `_b.result`).
5. Named comprehension build → batch → indexed join (the example-5 core).
6. Effect-inside-comprehension → expect the friendly error from Change 3.
7. Chained-dependency ordering still works (regression: existing
   `processor_test.go` chained test).
8. Package-clause file: hidden labels resolve under the declared package.
9. Quoted struct label (`f: op.#Echo & {args:[{"PRIVATE-TOKEN": "x"}]}`) decodes
   to Go map key `PRIVATE-TOKEN` without quotes (Change 4a regression).

## Risks / open

- **Incomplete vs. error distinction.** `Validate(cue.Concrete(true))` returns an
  error for both "references unexecuted `.result`" (retry) and "genuine bad value"
  (fatal). We treat both as retry and rely on the no-progress check to surface a
  real bug after the loop — *less precise* error messages than today for malformed
  args. Mitigation: on a zero-progress pass, re-run validation and surface the
  collected `lastErr`. Acceptable; improve later if noisy.
- **`maxPasses` + large splices (MAJOR-4, ledger E3).** `maxPasses` is now a
  parameter (Change 4b), removing the hard ceiling. The remaining concern is
  *cost*: each pass recompiles the whole source, and a batch returning hundreds of
  items splices a large `ListLit` that later passes re-parse — O(passes ×
  result-size). Measure-first: add a benchmark with a 500-item batch + index-join.
  Escape hatch if it bites: store executed results out-of-band keyed by call path
  and inject a *reference* into `compilableSource` rather than re-splicing literal
  source. Built only on demonstrated cost.
- **Package detection.** Must read the file's package clause correctly so
  `cue.Hid` uses the right package; default `_` for package-less files. Cover in
  test 8.
- **List-element calls.** A call as a bare list element (no field label) is not
  path-addressable. Rare; reject with a clear error or require it be a named field.

## Sequencing

1. ewe: Change 1 (path tracking + `labelSelector`) + Change 2 (`resolveCallArgs`)
   + Change 3 (comprehension rejection) + tests 1-8.
2. Confirm the ewe-extensions example 5 evaluates end to end against a stub
   registry (no real HTTP — fake `#HttpAll`/`#HttpBatch` returning fixtures).
3. Only then: mu sink suite + per-execute registry + `ewe` action body kind.
4. Then the secret-as-substring follow-on spec.
5. `#SystemModel` / `pudl run` / convergence remain out until 1-3 land.
