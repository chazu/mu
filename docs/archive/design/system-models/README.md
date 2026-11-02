# System Models: a swamp-equivalent over pudl + mu

> **Historical vision record (superseded 2026-08-05).** V1 convergence,
> `pudl run`, exact `run-set` approvals, and sealed routing are implemented.
> Current operator guidance is `mu guide pudl` and the PUDL repository's CLI and
> cross-resource wiring documentation. Retired commands such as
> `pudl export-actions` below are preserved only as design history.

> The former build contract is preserved in
> [`V1-BUILD-SPEC.md`](V1-BUILD-SPEC.md); it is no longer current usage text.

> **Status (2026-06-20).** This is the original vision doc. Every CRITICAL/MAJOR
> finding from the two adversarial reviews has since been worked through in
> **[issue-ledger.md](issue-ledger.md)** — read it for what is *resolved*,
> *deferred*, or *cut*. The **observe-only** half is fully specced (see
> [ewe-arg-resolution-spec.md](ewe-arg-resolution-spec.md),
> [ewe-secrets-spec.md](ewe-secrets-spec.md),
> [ewe-body-kind-spec.md](ewe-body-kind-spec.md),
> [ewe-http-pagination-spec.md](ewe-http-pagination-spec.md)). The **convergence**
> half (`desired`/`converge`, fixed points, ACUTE) is **design-resolved** (ledger
> V1.1–V1.4, V1.6; V1.5 rollback cut) and the sections below are reconciled to it.
> The ledger remains the source of truth for the detailed V1 decisions.

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
   already CUE. *(Resolved: effects are **forbidden inside comprehensions** —
   per-item fan-out is a batch effect + pure build/join. See ledger E2 and the
   pagination spec.)*
3. **Two extensibility axes, two mechanisms.**
   - *Logic* extends in CUE — no code, no recompile, unprivileged.
   - *Capabilities* (new external systems) extend as **mu plugins** — out of
     process, sandboxed, language-agnostic, CAS-distributed. *(Resolved: a plugin
     is reused as a **`#PluginObserve` populate kind** — its own `observe` action,
     consumed by dataflow — **not** an inline `#Plugin` ewe function. The generic
     `#Plugin` function is cut/deferred; see ledger P2.)* Adding a capability never
     edits mu/pudl Go.
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
    //   alongside `desired`. V1: `#PluginPlan` only (all consumers use it; the
    //   shipped export-actions path serves it). ewe-converge (`#EweTarget`
    //   mutate) deferred — no consumer; ewe stays first-class for `populate`.
    converge?: #PluginPlan   // V1; future: #EweTarget | #PluginPlan

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
GitLab gist generalized. It runs as an **action body of kind `ewe`**.

> **Resolved (ledger I1, [ewe-body-kind-spec.md](ewe-body-kind-spec.md)).** The
> populator is **a program, not an inline block**: a normal `.cue` file
> content-addressed into CAS (the plugin idiom), the action carrying an `EweRef`
> digest. mu never loads it as config — it is restored and run through the ewe
> evaluator at execute time, with a **per-execute** privileged registry bound to
> the sandbox + sealed resolver + `MU_OUT`. Effect ordering falls out of CUE
> `.result` data dependencies (no pure-ordering primitive — ledger E5). **Taint
> does not use `pith.Secret`** — secrets are references revealed only inside Go
> sinks (ledger S1, [ewe-secrets-spec.md](ewe-secrets-spec.md)). Cross-action
> data is a declared `input` (content digest + implicit `DependsOn`) read via
> `#ReadFile` — there is no `#Output` function. The "mu keeps a CUE value
> unevaluated" framing in earlier drafts was inverted; see the spec.

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
| `#Secret` | `[name] -> {"$secret":name}` | sealed-input **reference**; revealed only in sinks (S1) |
| `#Secretf` | `[tmpl, names…] -> {"$secretTemplate":…}` | secret-as-substring **reference** (S1) |
| `#Env` | `[name] -> string` | non-secret env; refuses sealed names |
| `#Http` | `[{url, method, query, headers, body, auth}] -> resp` | single request; `auth:` block resolves secret refs (S1) |
| `#HttpAll` | `[{…, paginate:{style, …}}] -> [items]` | **the iteration primitive** — paginate in Go (`none`/`page`/`link`/`cursor`, `maxPages` cap; I2) |
| `#HttpBatch` | `[[reqSpec…]] -> [envelope…]` | **the fan-out primitive** — N requests → list; per-item error envelopes (E2/E4) |
| `#ReadFile` | `[path] -> bytes/string` | confined to `MU_OUT`/declared inputs |
| `#WriteFile` | `[path, content] -> ok` | confined to `MU_OUT` |
| `#Exec` | `[{cmd, args, stdin}] -> {stdout, stderr, code}` | sandboxed subprocess |
| `#Now` | `[] -> ts` | wall-clock; no seed (moot under impure — K1) |

These are the irreducible effects. `#HttpAll` paginates and `#HttpBatch` fans out
*internally in Go* (CUE can't iterate unboundedly); per-item fan-out is batch +
pure build/join, never effects-in-comprehensions (E2). Secrets are references
throughout, revealed only at sinks ([ewe-secrets-spec.md](ewe-secrets-spec.md)).

### Tier 2 — DEFERRED: pudl lake vocabulary (no observe-only consumer)

> **Resolved (ledger P1): the entire Tier-2 set below is deferred, not built in
> v1.** It was premised on an ewe *body* reading/writing the lake, but the
> populate→catalog path is **ewe writes JSON (`#WriteFile`) → action output →
> `pudl run` ingests** (the `_schema` tag routes records); the populator never
> calls a catalog function. `#DatalogQuery`'s named consumer — "evaluate a `#Check`
> from inside a model build" — also went away: **checks run in `pudl run`** by
> reusing the direct `datalog.Evaluate` call `pudl query` already ships (no driver
> word, no ewe function). Build these only if a real in-populate consumer appears.

| Function (deferred) | Was backed by |
|---|---|
| `#CatalogQuery` / `#CatalogGet` / `#CatalogCount` | `internal/pithdriver/catalog.go` |
| `#FactQuery` / `#FactAssert` / `#FactRetract` | `internal/pithdriver/facts.go` |
| `#SchemaMatch` / `#SchemaInfer` / `#SchemaList` | `internal/pithdriver/schema*.go` |
| `#Drift` | `internal/pithdriver/drift.go` |
| `#DatalogQuery` | `internal/datalog` (now: `pudl run` calls it directly) |

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

> **Superseded (ledger P2).** This section is retained for rationale but its
> conclusion is reversed for v1. There is **no `#Plugin` ewe function** — neither
> the inline form (a plugin subprocess mid-execute is sandbox-escape-shaped and is
> not built) nor the dataflow form (no v1 consumer: a populate is *either* ewe
> *or* plugin-observe, never both in one body). **Reusing a plugin = the
> `#PluginObserve` populate kind** — the plugin's existing `observe` runs as its
> own action/phase and its output is ingested by `pudl run`. Cross-source joins
> (two systems in one model) are **pudl Datalog relations**, not an in-body plugin
> call. Read the rest of this section as the path *considered and not taken*.

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

> **Resolved (ledger V3): defer the deletion, not the deprecation.** pith deletion
> waits until **observation via ewe ships end-to-end** (the observe-only v1 gate:
> arg-resolution engine + sink suite + `ewe` body kind + a real populator running
> against live APIs). Taint-type extraction proceeds now; `pudl exec` removal and
> the VM deletion wait behind a working ewe observe spike. The CUE-free-IR question
> remains the only thing that could grant a permanent reprieve — unmeasured, so
> deletion stays the default *after* the spike. (Note: the "two notations" claim
> above already holds — secrets do **not** use `pith.Secret` on the ewe path; S1.)

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
governance — see [examples/](examples/). They span both fixed points
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

**Convergence** (`desired` + `converge` present *and* `pudl run --converge`):

```
populate (Accumulate) → drift vs desired (Unify) → converge (Transform+Execute) → re-populate …
   └── converges to the CONVERGENCE fixed point: observed == desired, drift = ∅.
```

`converge` is the natural home for the existing `pudl export-actions → mu build`
path — the BRICK loop already does this; the model just bundles it. The fixed
point is not bolted on: it is precisely what `pudl run`'s iteration settles into.

**Mutation is opt-in, even for a convergence-capable model.** Declaring `desired`
+ `converge` makes a model *able* to converge; it does not make a bare `pudl run`
mutate. Convergence (the only place the system touches production) fires **only**
under `pudl run --converge`. Without the flag, a convergence model behaves
observe-only — drift is flagged, never closed. The full CLI contract,
termination (drift==∅ + a hard max-iter cap), and failure semantics (`converged`
/ `failed`, the mandatory partial-state warning) are settled in the
[issue ledger](issue-ledger.md)'s V1 section. Loop termination's monotonic-guard
question was argued out in
[`dialectics/v1-2-loop-termination.ndjson`](../dialectics/v1-2-loop-termination.ndjson).

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

> **Reconciled to the ledger (2026-06-20).** The original phases are superseded by
> the resolved specs. The v1 scope is **observe-only**; convergence is out (V1).

1. **ewe arg-resolution engine** (ewe repo) — the LookupPath redesign + rider fixes
   ([ewe-arg-resolution-spec.md](ewe-arg-resolution-spec.md)). The blocker
   everything else sits on (E1–E6). Validated empirically.
2. **Secrets** — `#Secret`/`#Secretf` refs + `goToCUEExpr` guard (ewe), then the
   sink-side `resolveSecrets` + `auth:` vocab (mu)
   ([ewe-secrets-spec.md](ewe-secrets-spec.md), S1).
3. **`ewe` action body kind** (mu) — populator-as-program: `eweSource` → CAS digest
   → `Action.EweRef`; per-execute registry; `#ReadFile` + declared-input dataflow;
   actionkey stanza ([ewe-body-kind-spec.md](ewe-body-kind-spec.md), I1).
4. **Tier-1 effect sinks** including `#HttpAll` pagination + `#HttpBatch` fan-out
   ([ewe-http-pagination-spec.md](ewe-http-pagination-spec.md), I2/E2/E4).
5. **`#SystemModel` schema (observe-only) + `pudl run <model>`** — populate
   (delegate to `mu build`, consume `mu --json`) → ingest JSON to catalog →
   relations → checks (direct `datalog.Evaluate`, P1) → report. Per-model isolation;
   three outcomes; no rollback (V2). Generalizes `pudl memory cycle`.
6. **pith** — extract the taint package now; **defer deletion** until the above
   ships end-to-end (V3). `pudl exec` retired, not ported. Do **not** build the old
   pith authoring sugar.

*Cut/deferred from v1:* the `#Plugin` ewe function (P2 — use `#PluginObserve`), the
Tier-2 lake vocabulary (P1), Tier-3 helpers (on friction), the pure-transform
cacheable action (K1), and the `after:`/pure-ordering primitive (E5). **The entire
convergence half** (`desired`/`converge`, the loop, drift-gating, termination) is
open under ledger **V1**.

## Open questions

*(Resolved questions removed; see the ledger. What genuinely remains:)*

- ~~**Does `#SystemModel` live in mu, pudl, or a shared CUE module?**~~ **Resolved
  (ledger D1): it lives in pudl.** mu never imports it; `pudl run` compiles a model
  to a mu build. Schemas are referenced as definitions (`pudl/linux.#Package`),
  custom ones shipped in `.pudl/`/`~/.pudl` (D2–D4).
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
