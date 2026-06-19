# System Models: a swamp-equivalent over pudl + mu

## Motivation

[swamp](https://swamp-club.com/) packages five primitives — Models, Workflows,
Data, Vaults, Reports — into one coherent product for "deterministic automation
for AI agents": agents build typed, repeatable workflows whose outputs are
searchable and versioned. The canonical example is *"inventory every Proxmox VM
and flag anything without monitoring."*

pudl + mu already provide every one of those primitives:

| swamp primitive | pudl + mu equivalent |
|---|---|
| **Models** (typed schemas in, validated data out) | pudl CUE schemas + catalog |
| **Workflows** (DAG, parallel, nested) | mu targets / action DAG |
| **Data** (immutable, versioned artifacts) | pudl catalog (content-hash, provenance, versions) + mu CAS |
| **Vaults** (encrypted secrets, by expression) | mu sealed inputs/outputs + `pass`/`sops` providers |
| **Extensions** (agent-created, persistent) | mu plugins / ewe functions |
| **Reports** (markdown + JSON after run) | mu advice plugins / pudl reports |

What is **missing** is *coherence*: the pieces are scattered. A system's shape
lives in a pudl schema, its populator lives in a pith `exec` program (or
nowhere), its relationships live in Datalog rules, its freshness lives in `mu
observe`/drift. There is no single artifact that says: *this is the Proxmox
model — here is its shape, here is how I populate it, here is how I keep it
current, here is what I flag.*

This document proposes that artifact (`#SystemModel`), the authoring surface for
its populator (ewe-in-CUE), the ewe function catalog needed to support it, and a
clear answer on runtime extensibility (mu plugins, **not** an embedded Lisp).

## Design principles

1. **The program is CUE.** No embedded program strings (no jq/expr-lang). Logic
   is expressed in CUE — comprehensions, struct literals, `if` guards,
   interpolation, and the CUE stdlib (`strings`, `list`, `regexp`,
   `encoding/json`, `crypto/sha256`, …).
2. **ewe supplies only what CUE cannot:** effects (I/O) and the one iteration
   primitive CUE lacks (bounded iteration / pagination). Everything pure is
   already CUE.
3. **Two extensibility axes, two mechanisms.**
   - *Logic* extends in CUE — no code, no recompile, unprivileged.
   - *Capabilities* (new external systems) extend as **mu plugins** — out of
     process, sandboxed, language-agnostic, CAS-distributed — surfaced to ewe
     through one generic `#Plugin` function. Adding a capability never edits
     mu/pudl Go.
4. **Effects are privileged and audited; they live in Go (or behind the plugin
   boundary). Logic is unprivileged and lives in CUE.** This is the boundary
   that keeps the dangerous surface small and reviewable.
5. **One notation for humans and agents.** ewe effects are `op.#Func &
   {args:[...]}` — pure struct *data*, no syntax, trivially machine-emitted.
   Pure transforms are CUE comprehensions — human-readable, and still
   machine-emittable. The same file reads well and writes easily. This
   dissolves pith's old trade-off ("agents can write it, humans can't read it").

## The `#SystemModel` artifact

A system model bundles shape + populate + relate + check + freshness into one
declaration. Most of it *binds existing pudl/mu pieces*; the only new surface is
the ewe populator.

```cue
#SystemModel: {
    name: string                          // "gitlab" | "proxmox"

    // SHAPE — the catalog schema(s) this model produces (pudl CUE defs).
    schema: [...#SchemaRef]

    // POPULATE — fetch the external system into the catalog. Either:
    //   (a) an ewe target (custom fetch — the GitLab case), or
    //   (b) a reference to an existing observer plugin (reuse aws/host/…).
    populate: #EweTarget | #PluginObserve

    // RELATE — derived relationships over catalog + facts (pudl Datalog rules).
    relations: [...#RuleRef]

    // CHECK — the "flag anything without monitoring" queries (pudl Datalog).
    checks: [...#Check]

    // DESIRED — declared desired state (IDEA Definition layer). Optional:
    //   present  → model can CONVERGE (drive the system toward this state).
    //   absent   → model is OBSERVE-ONLY (inventory + flag, the swamp case).
    desired?: [...#Definition]

    // CONVERGE — close drift between desired and observed (ACUTE Transform +
    //   Execute). Typically `pudl export-actions` → `mu build`. Only meaningful
    //   alongside `desired`.
    converge?: #EweTarget | #PluginPlan

    // FRESHNESS — how the model stays current (mu observe + drift cadence).
    freshness?: #Freshness

    // VAULT — sealed inputs the populator needs.
    vault?: {[string]: #SecretRef}
}

#Check: {
    name:     string
    query:    #RuleRef            // a Datalog relation to evaluate
    expect:   "empty" | "nonempty"
    severity: "info" | "warn" | "fail"
    message:  string
}

#Freshness: {
    observe?: #EweTarget | #PluginObserve   // re-read live state
    every?:   string                        // cadence hint, e.g. "1h"
    drift?:   bool                           // compute drift vs declared
}
```

A model is then evaluated as a small mu build: `populate` runs (a target),
`relations` load, `checks` evaluate via `pudl query`, `freshness` schedules
re-observation, and — when `desired`/`converge` are present — drift is closed.
The model *is* the swamp "Model + Workflow + Data + Vault + Report" bundle,
assembled from parts already shipped. See **Fixed points & the ACUTE mapping**
below for what `pudl run` converges to in each mode.

## The populator surface: ewe-in-CUE

The populator is the part that needs real expressiveness, and it is exactly the
GitLab gist generalized. It runs as an **action body of kind `ewe`** (see
[expressiveness-sketches.md](../expressiveness-sketches.md) for the body-kind
integration): a CUE program fragment mu keeps unevaluated and runs through the
ewe evaluator at execute time, with the privileged effect registry bound to the
sandbox + sealed env. Effect ordering falls out of CUE data dependencies; taint
carries via pith's `Secret` type (`Redact` in traces, `Reveal` only at sinks).

## ewe function catalog

Grouped by necessity. **Tier 0 is not built — it is CUE stdlib.** ewe supplies
only Tiers 1–3.

### Tier 0 — already free in CUE (do not build)

`[for x in xs if p {…}]` (map/filter), `if`/guards, `\(…)` interpolation,
hidden-field `let`, plus `strings.*`, `list.Sort`/`FlattenN`, `regexp.*`,
`math.*`, `encoding/json`, `encoding/yaml`, `encoding/base64`, `crypto/sha256`,
`path.*`, `time.*`. Document these so nobody reimplements them as ewe funcs.

### Tier 1 — NECESSARY effects (Go, non-hermetic, shared mu+pudl core)

| Function | Shape | Notes |
|---|---|---|
| `#Secret` | `[name] -> Secret` | sealed input; taint-tracked |
| `#Env` | `[name] -> string` | non-secret env; refuses sealed names |
| `#Http` | `[{url, method, query, headers, body}] -> resp` | single request |
| `#HttpAll` | `[{…, paginate:{param, until}}] -> [items]` | **the iteration primitive** — paginate until empty/cursor exhausted |
| `#ReadFile` | `[path] -> bytes/string` | confined to `MU_OUT`/inputs |
| `#WriteFile` | `[path, content] -> ok` | confined to `MU_OUT` |
| `#Exec` | `[{cmd, args, stdin}] -> {stdout, stderr, code}` | sandboxed subprocess |
| `#Now` | `[] -> ts` | seedable for determinism/caching |

These are the irreducible effects. Note `#HttpAll` is the single concession to
"CUE can't iterate unboundedly" — pagination is baked into the fetch rather than
exposed as a general higher-order `unfold`.

### Tier 2 — USEFUL: pudl lake vocabulary (Go, pudl-side)

Surface the existing pudl subsystems (today reached via pith driver words) as
ewe functions so CUE programs read/write the lake directly:

| Function | Backed by |
|---|---|
| `#CatalogQuery` / `#CatalogGet` / `#CatalogCount` | `internal/pithdriver/catalog.go` |
| `#FactQuery` / `#FactAssert` / `#FactRetract` | `internal/pithdriver/facts.go` |
| `#SchemaMatch` / `#SchemaInfer` / `#SchemaList` | `internal/pithdriver/schema*.go` |
| `#Drift` | `internal/pithdriver/drift.go` |
| `#DatalogQuery` | `internal/datalog` — run a rule relation inline |

`#DatalogQuery` is the bridge that lets a `#Check` be evaluated from inside a
model build, reusing the real query engine rather than reimplementing it.

### Tier 3 — USEFUL: pure helpers where CUE stdlib is clumsy (Go, tiny)

Build only on demonstrated friction:

| Function | Why CUE alone is awkward |
|---|---|
| `#SortBy` `[list, key]` | `list.Sort` needs a comparator struct; by-field-name is ergonomic |
| `#GroupBy` `[list, key]` | no native group; comprehension version is verbose |
| `#GetPath` `[obj, "a.b.c", default]` | CUE optional-chaining + `or` gets ugly for deep access |
| `#DeepMerge` `[a, b]` | `&` is unification not override-merge |

Everything else (split/join/regex/encode/hash) is Tier 0. Resist growing Tier 3.

## The keystone: a generic `#Plugin` function

**Do we need an ewe function for leveraging an arbitrary mu plugin? Yes — it is
the extensibility keystone.** It is how *capabilities* extend without editing
mu/pudl Go and without an embedded scripting language.

mu plugins already speak a stable NDJSON protocol with `discover` / `plan` /
`observe` / `resolve_secret` / `store_secret` / `advise`, run out of process,
are sandboxed, and are distributed through CAS. For *populating a model*, the
relevant op is **`observe`** (a plugin returns observed JSON — exactly what
`aws`/`host`/`lint` already do).

```cue
#Plugin: {
    args: [{
        name: string                 // plugin name, e.g. "aws"
        op:   "observe" | "plan"     // protocol method to invoke
        input: {...}                 // request payload (config, query, …)
    }]
    // result: the plugin's response (observed data, or emitted actions)
}
```

Example — populate from an existing observer instead of hand-rolling a fetch:

```cue
_vms: op.#Plugin & { args: [{
    name:  "proxmox"
    op:    "observe"
    input: { endpoint: "https://pve.local", token: _token.result }
}]}
```

This means: **adding a new external system = write a mu plugin** (any language,
sandboxed, CAS-distributed) and call it from ewe via `#Plugin`. No new ewe Go
function per system. The privilege boundary stays at the plugin sandbox, which
already exists and is already the sanctioned capability surface.

### Two ways to wire a plugin into a model — and which to prefer

- **Dataflow (preferred):** the plugin `observe` runs as its *own* mu action;
  the ewe populator consumes its output via `target/output`. mu-idiomatic;
  lifecycle/sandbox managed by the coordinator as today.
- **Inline `#Plugin` (escape hatch):** invoke the plugin from inside the ewe
  body. Convenient for ad-hoc/one-shot, but introduces a new pattern — starting
  a plugin subprocess mid-execute. Define lifecycle/sandbox rules before relying
  on it.

Recommend: `#SystemModel.populate` accepts either a `#EweTarget` (custom fetch,
the GitLab case) or a `#PluginObserve` (reuse an observer, the Proxmox case),
both as plain targets in the DAG. Keep inline `#Plugin` for genuine one-offs.

## Runtime-authored functions: pith vs embedded Lisp vs plugins

The question: should users author *new ewe functions at runtime* — via pith,
or by embedding a Go Lisp (let-go / Glojure / zygomys)? Analysis:

**What would a runtime-authored function actually do?**
- If **pure** (reshape, helper): CUE comprehensions + stdlib already cover it.
  Genuinely novel pure helpers are rare, and Tier 3 mops up the ergonomic gaps.
- If **effectful** (new capability): you do *not* want arbitrary user code doing
  I/O behind the privileged registry — that collapses the boundary that keeps
  the dangerous surface auditable. The sanctioned way to add an effect is a
  plugin (sandboxed), not a script with raw socket access.

So the demand for a runtime function-authoring *language* is **weak for pure**
(CUE covers it) and **wrong for effectful** (plugins are the boundary).

**Against embedding a Lisp (let-go / Glojure / zygomys):**
- pudl already **removed a Glojure runtime** to escape exactly this maintenance
  weight. Re-adding one reverses a deliberate, learned decision.
- It is a second evaluator to maintain, taint-track, sandbox, and document —
  alongside CUE/ewe and pith. Three runtimes for one job.
- It does not earn its keep: CUE covers pure logic; plugins cover capabilities.
- zygomys/let-go are fine libraries; the objection is architectural, not about
  the implementations.

**Recommendation.**
- *Logic extensibility* → CUE composition. No code.
- *Capability extensibility* → mu plugins via `#Plugin`. No mu/pudl Go.
- *Embedded Lisp* → **do not adopt.** Revisit only if a concrete need appears
  that none of the above can serve (none is currently identified).

This keeps exactly two human-facing notations — **CUE/ewe** (author models and
pipelines) and **Datalog** (query relations).

## What about pith itself?

The honest conclusion of this design is that **pith has no irreducible role once
ewe-CUE lands and the taint type is extracted.** Each prior justification fails
on its merits:

- *"Custodian of the taint type."* `Secret`/`Redact`/`Reveal` is a plain Go
  package — it does not need the VM. Extract it and taint survives without pith.
- *"Agent codegen target / data-as-program."* ewe effects are `op.#Func &
  {args:[…]}` — pure struct data, no syntax, equally machine-emittable **and**
  human-readable. CUE/ewe dominates pith here too.
- *"Runtime-loadable leaf-function substrate."* No concrete case exists. Pure
  helpers → CUE covers; effectful → plugins cover. Speculative.

The only **real** reasons to keep pith are transitional, not permanent:

1. **It works today; ewe does not yet.** Adopting ewe *swaps* the execute-time
   interpreter (pith VM → ewe evaluator) — it does not add a runtime. Keep pith
   running until ewe proves out end to end.
2. **pudl already imports it** (`pudl exec` + driver words). Removal means
   migrating or dropping that path.

One open question *could* justify permanent survival: **execute-time cost.** ewe
runs CUE evaluation (parse + unify) per action body; pith is a ~450-line
interpreter over `[]any` with no CUE dependency at execute time. If models
produce many tiny actions, a cheap CUE-free execution IR has value. This is
unproven and the *only* non-sentimental case for keeping pith.

**So: treat pith as deprecated-on-arrival of ewe, not a fixture.** Extract the
taint package now (worth saving regardless), build the ewe body kind, migrate or
drop `pudl exec`, then delete pith — unless the execute-time-cost question turns
up a real need for a CUE-free IR.

## Worked example: the GitLab model, end to end

```cue
import "op"

gitlab: #SystemModel & {
    name: "gitlab"

    schema: ["git.repository.gitlab"]

    vault: { GITLAB_TOKEN: "env:GITLAB_TOKEN" }

    // POPULATE — ewe target; fetch all repos, reshape, write catalog records.
    populate: {
        sealed_inputs:      { GITLAB_TOKEN: "env:GITLAB_TOKEN" }
        sealed_input_modes: { GITLAB_TOKEN: "env" }
        plan: [{
            id: "fetch", outputs: ["repos.json"], network: true, impure: true
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

                out: op.#WriteFile & { args: ["\(op.#Env.result.MU_OUT)/repos.json", json.Marshal(_repos)] }
            }
        }, "action/emit"]
    }

    // RELATE — derived relationships (Datalog rule files, authored in CUE).
    relations: ["rules/gitlab_ownership.cue"]

    // CHECK — flag repos missing a default branch (the swamp "flag X" pattern).
    checks: [{
        name:     "repos_without_default_branch"
        query:    "repo_missing_branch"      // a Datalog relation
        expect:   "empty"
        severity: "warn"
        message:  "repo has no default branch"
    }]

    // FRESHNESS — re-fetch hourly, compute drift vs the declared catalog.
    freshness: { observe: populate, every: "1h", drift: true }
}
```

Compare to the original gist: ~200 lines of copy-pasted stack ops → a readable
CUE bundle that is *also* the model's documentation. The Proxmox example is the
same shape with `populate: op.#Plugin & {args:[{name:"proxmox", op:"observe", …}]}`.

For five fuller walkthroughs — remote-server provisioning, Kubernetes policy
compliance, TLS certificate lifecycle, DNS zone convergence, and repo
governance — see [examples.md](examples.md). They span both fixed points
(observe-only and convergence) and exercise the full ewe / `#Plugin` / Datalog
surface.

## Running a model: `pudl run`

swamp's value is one coherent loop: define → run → catalog → check → report,
behind a single verb. A `#SystemModel` spans both tools — `populate` is a mu
target (ewe body → catalog), `relations`/`checks` are pudl Datalog, the report is
pudl — so *something* must drive the round trip. That driver is the loop's entry
point: **`pudl run <model>`**.

```
pudl run gitlab
  ├─ populate → delegates to `mu build //models/gitlab`   (mu executes the DAG)
  ├─ relations → load Datalog rules
  ├─ checks   → evaluate via pudl's Datalog engine, apply severities
  └─ report   → structured markdown + JSON
```

**pudl is the ergonomic entry point into mu's action graph.** mu is the more
abstract, harder-to-approach system; most users should drive their models
through pudl and never touch mu directly — *unless they choose to*, at which
point `mu build //models/<name>` is right there, doing exactly what `pudl run`
delegated to. pudl is a shortcut, not a wall: it does not hide mu, it spares you
from needing it.

This is **charter-consistent**. pudl's rule is *"pudl doesn't execute —
execution is mu's job."* `pudl run` *orchestrates*; the work happens in
`mu build`. Orchestration ≠ execution, and the precedent already ships: `pudl
memory cycle` shells to `mu build //memory:cycle` today. `pudl run` generalizes
that one-off into the model loop, and **replaces `pudl exec`** — the unit of
"run" rises from *a raw program* to *a model*.

### The fork, recorded

A model loop could instead be driven entirely by mu: make `checks` mu targets (a
Datalog-query ewe func / plugin that fails the build) so `mu build
//models/gitlab` runs everything — one executor, one entry point. Rejected:
that pushes reporting and temporal Datalog checks into mu, where they do not
belong (mu is the dumb executor; pudl is the modeling/reporting brain). `pudl
run` keeps mu dumb and keeps model knowledge — shape, relations, checks, reports
— in pudl. The cost is one orchestration command in pudl, which is also the UX
win, so the trade favours `pudl run`.

## Fixed points & the ACUTE mapping

`#SystemModel` is a packaging of the **IDEA** layers and **ACUTE** phases (see
[`architecture/brick-ecosystem.md`](../../architecture/brick-ecosystem.md)) behind
one declaration. Connecting it to that frame pins down *what `pudl run`
converges to* — because the BRICK loop is defined by its fixed point.

### Two fixed points

1. **Observation fixed point** (`pudl verify`-style idempotency). Re-observing a
   system whose reality is unchanged yields the *same* catalog: content-hash
   dedup, no new versions, drift = ∅. The inventory is a pure function of the
   external system. This is the model being *stable*, not the system being
   *correct*.
2. **Convergence fixed point** (the full BRICK loop). The ACUTE cycle repeats
   until **observed == desired**: Unify finds no drift, Transform emits no
   actions, Execute is a no-op. Same "stop when no new rows" idea as the Datalog
   fixed point, lifted to infrastructure.

### How the fields map

| `#SystemModel` field | IDEA layer | ACUTE phase |
|---|---|---|
| `schema` | Intention | — |
| `desired` *(optional)* | **Definition** | — |
| `populate` | → produces **Application** | **Accumulate** (+ Configure via inference) |
| `relations` / `checks` | — | **Unify** (read-only: query/flag) |
| `converge` *(optional)* | — | **Transform + Execute** |
| `freshness` | — | loop cadence |

### Two modes, two fixed points

`pudl run` iterates a model; which fixed point it converges to depends on
whether the model declares a desired state.

**Observe-only** (no `desired`/`converge` — the swamp "inventory and flag"
case, and the GitLab model):

```
populate (Accumulate) → checks/drift (Unify) → freshness re-observe → populate …
   └── converges to the OBSERVATION fixed point: catalog stable, checks evaluated.
```

**Convergence** (`desired` + `converge` present — a known target state):

```
populate (Accumulate) → drift vs desired (Unify) → converge (Transform+Execute) → populate …
   └── converges to the CONVERGENCE fixed point: observed == desired, drift = ∅.
```

`converge` is the natural home for the existing `pudl export-actions → mu build`
path — the BRICK loop already does this; the model just bundles it. The fixed
point is not bolted on: it is precisely what `pudl run`'s iteration settles into.

### Design intent

The model concept must be **able to** encapsulate convergence when the target
state is known, without *forcing* it. Observe-only models (most inventories)
declare neither `desired` nor `converge` and reach the observation fixed point.
Models with a known desired state add both and reach the convergence fixed point
— *the same artifact, the same `pudl run`*, one loop that does or does not have a
converge arm. This is an area to iterate on: the `desired`/`converge` shape,
how drift severity gates whether convergence fires (vs. only flags), loop
termination guarantees, and convergence-failure reporting are all open.

## Implementation phases

1. **ewe action body kind.** Add `Ewe` to the `Action` struct (`dag/graph.go`),
   teach `mapToActionSpec` the `ewe` key, add the execute-phase arm in
   `coordinator`/`dag` that runs the ewe evaluator with an effect registry (the
   ewe peer of `pithvm.RegisterExecDrivers`). Reuse pith's `Secret`/`Reveal`.
2. **Tier-1 ewe functions** + the shared effect/taint core extracted so pith
   driver words and ewe funcs call the *same* http/file/secret implementations.
3. **`#Plugin` ewe function** (start with `op: "observe"`, dataflow form first).
4. **Tier-2 lake functions** in pudl; `#DatalogQuery` bridge.
5. **`#SystemModel` schema** + `pudl run <model>`: orchestrate populate (delegate
   to `mu build`), then relations → checks → report. Generalizes the existing
   `pudl memory cycle` shell-out.
6. **Tier-3 helpers** only as friction demands.
7. **pith disposition (deprecate, don't freeze):** extract the taint type to its
   own Go package immediately (the one piece worth saving). `pudl exec` is
   **retired, not ported** — querying is `pudl query` (Datalog), running is
   `pudl run` (models). Once the ewe body kind ships and `pudl run` lands,
   **delete pith** — unless the execute-time-cost question (CUE-free IR) proves
   real. Do **not** build the earlier pith authoring sugar
   (`format`/`with`/records); ewe-CUE supersedes it.

## Open questions

- **`#Plugin` inline lifecycle.** If a plugin is invoked mid-execute (not as a
  dataflow dependency), what owns its subprocess/sandbox? Resolve before
  promoting inline `#Plugin` beyond an escape hatch.
- **ewe evaluation semantics for effects.** CUE is lazy and total; effectful
  `op.#Func` results must be forced in dependency order. Confirm ewe's
  rewrite-to-`result` model sequences side effects deterministically (it should,
  via data deps — verify with a multi-effect test).
- **Determinism / caching.** `#Now`, `#HttpAll` responses — what is part of the
  action cache key vs. captured output? Mirror pith's rule (refs+structure in
  key, values out).
- **Does `#SystemModel` live in mu, pudl, or a shared CUE module?** It binds
  both. Leaning a shared `brick`-adjacent module both import.
- **Pagination `until` vocabulary.** `"empty"` covers offset paging; add
  `"cursor"`/`"link-header"` forms for GitHub/GitLab-style cursors.
- **Convergence semantics (iterate here).** With `desired`/`converge` present:
  what gates convergence vs. flag-only (drift severity? an explicit opt-in?);
  how does `pudl run` guarantee loop termination (max iterations, drift must
  monotonically shrink); and how is a convergence *failure* reported distinctly
  from drift? The observe-only path is well-defined; the converge path is not.
- **Does pith get a CUE-free execution IR reprieve?** Measure per-action ewe
  evaluation cost (CUE parse + unify) against pith's `[]any` interpreter on a
  many-small-actions build. If ewe is materially slower at action scale, pith
  survives as a compile target / cheap IR; otherwise it is deleted. This is the
  single decision that determines whether pith lives.
