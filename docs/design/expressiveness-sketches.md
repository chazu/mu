# Expressiveness sketches: the GitLab-repos gist, four ways

The motivating artifact is a real plan body that fetches GitLab projects across
9 paginated API calls and reshapes each into a catalog record. In raw pith it is
~200 lines: the fetch block is copy-pasted 9× (one per page, `page=1..9`
hardcoded), and the reshape quotation is copy-pasted 9× alongside it.

This document rewrites that gist in four candidate styles so the authoring
surfaces can be compared directly. Each section notes what is **real today** vs.
**proposed/invented** so nothing here is mistaken for shipping syntax.

## The canonical intent

Strip the gist of its mechanics and it does exactly this:

1. **Fetch** `GET https://gitlab.com/api/v4/groups/garner-health/projects`
   with `include_subgroups=true&per_page=100`, paging `page=1..N`,
   header `PRIVATE-TOKEN: <GITLAB_TOKEN secret>`.
2. **Reshape** each project `r` into:
   ```
   {
     name:           "gitlab.com/" + r.path_with_namespace,
     default_branch: r.default_branch,
     visibility:     r.visibility,
     namespace:      r.namespace.full_path,
     _schema:        "git.repository.gitlab",
   }
   ```
3. **Filter** out repos whose `default_branch` is null.
4. **Concat** all pages into one array, write to `$MU_OUT/repos.json`.

Two orthogonal problems are tangled together here:

- **Effects** — paginated fetch, file write. The 9× copy-paste is a *pagination*
  problem, not a language problem. A declarative source kills it in any paradigm.
- **Pure transform** — map / filter / reshape JSON. This is the part that wants
  real expressiveness, and it is a solved problem domain.

Every sketch below separates the two.

---

## Style 1 — pith + sugar

Concatenative, but with the three agreed additions (record literals, `format`,
named bindings) plus `def` (program-defined words, shareable across targets and
`pudl exec`) and `http/paginate` (declarative pagination word that loops until a
page comes back empty).

**Real today:** the VM, `map`/`filter`/`get`/`set`/`concat`, `secret/get`,
`http/request`, `file/write`, `target/config`, `action/emit`.
**Proposed:** `def`, record literal `{| k v ... |}`, `format`, `with` (locals),
`http/paginate`.

```jsonc
// lib/git.pith.cue  — shared word library, loaded by mu AND pudl
{
  defs: [
    // ( repo -- record )  reshape one GitLab project into a catalog record
    ["def", "to-repo", [
      "with", ["r"], [                       // bind TOS to name `r`
        "{|",
          "'name",           ["'gitlab.com/{path_with_namespace}", "r", "format"],
          "'default_branch", ["r", "'default_branch", "get"],
          "'visibility",     ["r", "'visibility", "get"],
          "'namespace",      ["r", "'namespace.full_path", "path"],
          "'_schema",        "'git.repository.gitlab",
        "|}"
      ]
    ]],
    // ( -- bool )  predicate: keep repos that have a default branch
    ["def", "has-branch?", ["'default_branch", "get", "null?", "not"]]
  ]
}
```

```jsonc
// target body — collapses ~200 lines to this
{
  "target": "//inventory/gitlab-repos",
  "sealed_inputs":      { "GITLAB_TOKEN": "env:GITLAB_TOKEN" },
  "sealed_input_modes": { "GITLAB_TOKEN": "env" },
  "plan": [
    { "id": "fetch", "outputs": ["repos.json"], "network": true, "impure": true,
      "body": [
        // build headers: { PRIVATE-TOKEN: <secret> }
        "{|", "'PRIVATE-TOKEN", ["'GITLAB_TOKEN", "secret/get"], "|}",
        "'headers", "swap",
        // paginate until empty; yields the concatenated array of all pages
        "{|",
          "'url",     "'https://gitlab.com/api/v4/groups/garner-health/projects?include_subgroups=true&per_page=100&page={page}",
          "'paginate", "{|", "'param", "'page", "'until", "'empty", "|}",
        "|}",
        "merge", "http/paginate",
        // pure transform
        "[", "to-repo", "]", "map",
        "[", "has-branch?", "]", "filter",
        // write
        "format/json", ["'{MU_OUT}/repos.json", "env/format"], "swap", "file/write"
      ]
    },
    "action/emit"
  ]
}
```

**Verdict.** ~90% of the gist pain gone: pagination is one word, the reshape is
named once and reused, `format`/records kill the `swap dup set` choreography.
But the body is still a flat list of words a human reads linearly with no nesting
cues. The ceiling is real — concatenative resists reading the moment control flow
or >2 live values appear. Best kept as effect-glue / a codegen target, not the
primary human authoring surface.

---

## Style 2 — ewe (CUE custom functions) + comprehensions

ewe functions are declared in CUE as `op.#Func & {args:[...]}` and rewritten to
include `result`. Functions **may be non-hermetic**, so a `#HttpGetAll` that
paginates and fetches is legal. The reshape is then a native CUE comprehension —
which is exactly what CUE is good at.

**Real today:** ewe registry, `op.#Func` rewriting, CUE comprehensions / string
interpolation / `if` guards.
**Proposed:** registering `#Secret`, `#HttpGetAll` (paginating fetcher),
`#WriteJSON` as ewe functions in mu/pudl.

```cue
import "op"

// --- effectful functions, registered in Go (non-hermetic) ---
_token: op.#Secret & { args: ["GITLAB_TOKEN"] }

_raw: op.#HttpGetAll & { args: [{
    url:      "https://gitlab.com/api/v4/groups/garner-health/projects"
    query:    { include_subgroups: true, per_page: 100 }
    paginate: { param: "page", until: "empty" }
    headers:  { "PRIVATE-TOKEN": _token.result }
}]}

// --- pure transform: a CUE comprehension does the whole reshape+filter ---
repos: [
    for r in _raw.result if r.default_branch != _|_ && r.default_branch != null {
        name:           "gitlab.com/\(r.path_with_namespace)"
        default_branch: r.default_branch
        visibility:     r.visibility
        namespace:      r.namespace.full_path
        _schema:        "git.repository.gitlab"
    }
]

// --- write ---
_out: op.#WriteJSON & { args: [repos, "\(op.#Env.result.MU_OUT)/repos.json"] }
```

**Verdict.** The transform is the most readable of all four — it *is* the data
shape, declaratively. CUE's `for ... if ...` comprehension and `\(...)` interp
are tailor-made for this. Effects live behind a thin function vocabulary, and
since the authoring language is CUE itself there is zero new syntax to learn or
maintain. Cost: every effect must be wrapped as an `op.#Func`, and CUE's
evaluation model (lazy, total, unification) can be surprising for procedural
flows (e.g. ordering effects, threading one fetch's result into the next).
Strong contender specifically because the host language is already CUE.

---

## Style 3 — gojq (embedded jq, pure-Go)

The domain *is* JSON transform; jq was built for it. `github.com/itchyny/gojq`
is a pure-Go jq with no CGo. Driver words become custom jq builtins. Pagination
either stays declarative on the source, or becomes a custom `fetch` builtin jq
drives with `reduce`.

**Real today:** gojq is a mature library (not yet a dependency here).
**Proposed:** embedding gojq in mu/pudl, exposing `secret`, `fetch`,
`paginate` as custom builtins.

```jsonc
// target: declarative source + a jq transform string
{
  "target": "//inventory/gitlab-repos",
  "sealed_inputs":      { "GITLAB_TOKEN": "env:GITLAB_TOKEN" },
  "sealed_input_modes": { "GITLAB_TOKEN": "env" },
  "source": {
    "http": {
      "url":      "https://gitlab.com/api/v4/groups/garner-health/projects",
      "query":    { "include_subgroups": true, "per_page": 100 },
      "paginate": { "param": "page", "until": "empty" },
      "headers":  { "PRIVATE-TOKEN": { "$secret": "GITLAB_TOKEN" } }
    }
  },
  "transform": "jq",
  "jq": "map(select(.default_branch != null) | { name: (\"gitlab.com/\" + .path_with_namespace), default_branch, visibility, namespace: .namespace.full_path, _schema: \"git.repository.gitlab\" })",
  "outputs": ["repos.json"]
}
```

The transform, expanded for reading:

```jq
map(
  select(.default_branch != null)
  | {
      name:           "gitlab.com/" + .path_with_namespace,
      default_branch,                       # field punning: .default_branch
      visibility,
      namespace:      .namespace.full_path,
      _schema:        "git.repository.gitlab",
    }
)
```

Pagination *inside* jq, if you prefer no declarative source (jq drives a custom
`fetch` builtin):

```jq
[ range(1; 100) as $p
  | fetch("https://gitlab.com/api/v4/groups/garner-health/projects";
          {include_subgroups: true, per_page: 100, page: $p};
          {"PRIVATE-TOKEN": secret("GITLAB_TOKEN")})
  | if length == 0 then empty else .[] end
]
| map(select(.default_branch != null) | { ... })
```

**Verdict.** The transform is dense but *purpose-built* and widely known — field
punning, `select`, `map` read at a glance. Pure Go, easy embed, battle-tested.
Cost: a second syntax in the stack, and jq's terseness can tip into write-only.
The most pragmatic "stop hand-rolling JSON transforms" option.

---

## Style 4 — applicative expr-lang (lodash / pipe flavour)

A small new language embedded as a string in CUE: `let`, lambdas, `|>` pipe,
`map`/`filter`/`reduce`, record literals with punning, string interpolation,
`try`. Effect vocabulary exposed as plain functions. This is the "Dhall/Jsonnet
with a pipe operator and a lodash stdlib" option — maximum control over the
surface and the taint story, maximum implementation cost (own parser + evaluator).

**Real today:** nothing — this is a greenfield language design.
**Proposed:** everything below.

```
// program string embedded in the target
let token = secret("GITLAB_TOKEN")

let pages = http.paginate({
  url:      "https://gitlab.com/api/v4/groups/garner-health/projects",
  query:    { include_subgroups: true, per_page: 100 },
  headers:  { "PRIVATE-TOKEN": token },
  paginate: { param: "page", until: empty },
})

pages
  |> map(r => {
       name:           "gitlab.com/${r.path_with_namespace}",
       default_branch: r.default_branch,
       visibility:     r.visibility,
       namespace:      r.namespace.full_path,
       _schema:        "git.repository.gitlab",
     })
  |> filter(r => r.default_branch != null)
  |> writeJson("${env.MU_OUT}/repos.json")
```

Lodash-chaining variant (same semantics, method-chain instead of pipe):

```
_.chain(http.paginate({ url, query, headers, paginate }))
 .map(r => ({
    name:           `gitlab.com/${r.path_with_namespace}`,
    default_branch: r.default_branch,
    visibility:     r.visibility,
    namespace:      r.namespace.full_path,
    _schema:        "git.repository.gitlab",
 }))
 .filter(r => r.default_branch != null)
 .thru(writeJson(`${env.MU_OUT}/repos.json`))
 .value()
```

**Verdict.** The most readable and the most *familiar* — anyone who has written
JS/lodash reads this with zero ramp. Named bindings, lambdas, and the pipe make
intent obvious. But it is a language you design, parse, evaluate, type, and
taint-track forever. pudl already removed a Glojure runtime to escape exactly
this maintenance weight; only justified if Styles 2/3 genuinely fail to fit.

---

## Side-by-side: the reshape, one repo

```
pith+sugar:  "{|", "'name", ["'gitlab.com/{path_with_namespace}", "r", "format"], "'default_branch", ["r","'default_branch","get"], ... "|}"
ewe/CUE:     { name: "gitlab.com/\(r.path_with_namespace)", default_branch: r.default_branch, visibility: r.visibility, namespace: r.namespace.full_path, _schema: "git.repository.gitlab" }
gojq:        { name: ("gitlab.com/" + .path_with_namespace), default_branch, visibility, namespace: .namespace.full_path, _schema: "git.repository.gitlab" }
expr-lang:   r => { name: "gitlab.com/${r.path_with_namespace}", default_branch: r.default_branch, visibility: r.visibility, namespace: r.namespace.full_path, _schema: "git.repository.gitlab" }
```

## The ewe approach, as a mu target

The preferred direction (no embedded strings; the program is CUE; logic extends
in CUE, only new *capabilities* touch Go) needs one integration decision: **where
in mu's pipeline does ewe run?**

ewe is normally eval-time AST rewriting — it would fire at CUE *load*. But these
effects (http, file, secret) must run at **execute time**: inside the sandbox,
after sealed inputs resolve, with caching. They cannot run when mu parses
`mu.cue`. So ewe slots in exactly where pith already lives: a new action body
*kind*. An action is `command:` or `body:` (pith) today; add `ewe:` — a CUE
program fragment mu keeps unevaluated and runs through the ewe evaluator at
execute time, with the privileged effect registry bound to the sandbox + sealed
env. Same phase, same secret wiring, same output capture as the pith VM. Just a
different body interpreter.

**Pure helpers used here are CUE stdlib, not ewe funcs.** `json.Marshal`,
`strings.*`, `list.Sort`, `regexp.*` are imported from CUE directly. ewe supplies
only effects (`#Secret`, `#HttpAll`, `#WriteFile`, `#Env`) and the one iteration
primitive CUE lacks (pagination, baked into `#HttpAll`).

```cue
import "op"

targets: [{
    target:             "//inventory/gitlab-repos"
    sealed_inputs:      { GITLAB_TOKEN: "env:GITLAB_TOKEN" }
    sealed_input_modes: { GITLAB_TOKEN: "env" }

    // trivial plan: emit one action whose body kind is `ewe`
    plan: [{
        id:      "fetch"
        outputs: ["repos.json"]
        network: true
        impure:  true

        // the body — a CUE program, run by the ewe evaluator at execute time
        ewe: {
            _token: op.#Secret & { args: ["GITLAB_TOKEN"] }

            _raw: op.#HttpAll & { args: [{
                url:      "https://gitlab.com/api/v4/groups/garner-health/projects"
                query:    { include_subgroups: true, per_page: 100 }
                headers:  { "PRIVATE-TOKEN": _token.result }
                paginate: { param: "page", until: "empty" }
            }]}

            _repos: [
                for r in _raw.result if r.default_branch != null {
                    name:           "gitlab.com/\(r.path_with_namespace)"
                    default_branch: r.default_branch
                    visibility:     r.visibility
                    namespace:      r.namespace.full_path
                    _schema:        "git.repository.gitlab"
                }
            ]

            // final effect — write declares the action's output
            out: op.#WriteFile & { args: ["\(op.#Env.result.MU_OUT)/repos.json", json.Marshal(_repos)] }
        }
    }, "action/emit"]
}]
```

### How it executes

1. **Load.** mu parses `mu.cue`; the `ewe:` block is kept as an unevaluated AST
   fragment — `op.#*` funcs are *not* resolved (no http at load).
2. **Plan.** plan emits the action; coordinator attaches target `sealed_inputs`,
   builds the DAG. The body's refs+structure go in the cache key, secret *values*
   do not — identical to pith bodies today.
3. **Execute.** coordinator runs the ewe evaluator on the fragment with a
   privileged registry: `#Secret`→sealed resolver (taint-tracked `Secret`),
   `#HttpAll`→sandboxed http, `#WriteFile`→confined to `$MU_OUT`. Data deps
   (`out`→`_repos`→`_raw`→`_token`) give evaluation order for free — no `#Seq`.
4. **Capture.** `#WriteFile` lands `repos.json` in `$MU_OUT`, matching `outputs`.
   Cached by input hash. Taint carries as pith's `Secret`: `Redact` in traces,
   `Reveal` only at the http sink.

### Coordinator wiring

`ewe` slots beside the pith branch in `internal/coordinator/coordinator.go` /
`internal/dag` execute. Today the executor dispatches: `Body` set → run pith VM;
else → run `Command` in sandbox. Add a third arm: `Ewe` set → run ewe evaluator
with execute-phase effect registry (the ewe peer of `pithvm.RegisterExecDrivers`).
The `Action` struct (`dag/graph.go`) gains an `Ewe` field; `mapToActionSpec`
parses an `ewe` key alongside `body`/`command`.

### Open design choice

The `plan: [{...}, "action/emit"]` wrapper is boilerplate for the common
"one target = one action" case. Two options:

- **Faithful** (above): plan emits actions, action body kind is `ewe`. Composes
  cleanly with multi-action plans and with pith.
- **Sugar:** a target-level `compute:` field mu auto-wraps into a single action.
  Cleaner for the 1:1 case, second code path.

Ship the faithful form first; add `compute:` sugar only if 1:1 dominates. Running
*plan itself* in ewe (emit ActionSpecs via a comprehension) is a separate
integration — `RegisterPlanDrivers` gains an ewe path — not needed for this gist.

## Recommendation summary

1. **Declarative paginated source** — do this regardless of language. It deletes
   the 9× copy-paste, the single biggest win, and is paradigm-independent.
2. **Transform layer** — the choice is between **ewe/CUE comprehensions** (zero
   new syntax, host language is already CUE, but procedural flows are awkward) and
   **gojq** (purpose-built, pure-Go, familiar, but a second syntax). Both crush
   the gist.
3. **pith** — keep as effect-glue and codegen target; add `def` + records +
   `format` + `with` so the *generated* / agent-authored form is sane. Don't grow
   it into the primary human authoring language.
4. **expr-lang (Style 4)** — hold unless 2 and 3 prove insufficient; the Glojure
   removal is the cautionary precedent.
5. **User-defined words/functions outside targets** — yes in every paradigm: a
   shared lib (pith `def`s, ewe registry, or jq module) loaded once, available to
   all targets and to `pudl exec`. Stop redefining `to-repo` per target.
