# Spec: the `ewe` action body kind (mu integration)

Resolves ledger **I1** (folds review findings **C2(i)** and **MAJOR-2**). Targets
the **mu** repo: `internal/dag`, `internal/coordinator`, `internal/cas`. Depends on
[ewe-arg-resolution-spec.md](ewe-arg-resolution-spec.md) (engine) and
[ewe-secrets-spec.md](ewe-secrets-spec.md) (sink-side reveal).

This is the seam where an ewe populator program meets mu's execution pipeline.
Every claim below is grounded in current mu source (verified 2026-06-20).

## The forcing problem (why this needs a design at all)

Three facts collide:

1. **ewe must run late** — at execute time, inside the sandbox, with resolved
   secrets and `MU_OUT`. It does HTTP and reveals secrets; none of that exists at
   load.
2. **mu reads config early, through CUE** — it parses `mu.cue` up front.
3. **CUE evaluates eagerly** — a live `op.#Http & {…}` in config either fails to
   load (`op` undefined) or, if `op` is stubbed, gets unified/normalized at load,
   destroying the call markers ewe needs and *running the program at the one time
   it must not run*.
4. **mu actions are plain serializable data** — emitted by plan programs as
   `map[string]any` (`coordinator.go:947`), JSON-hashed for cache keys
   (`actionkey.go:50`), sent over the plugin wire. Whatever holds the ewe program
   inside an action must survive as data, not as an AST or `cue.Value`.

Facts 3+4 force the program to reach execute time as **inert, content-addressed
text** — not inlined CUE. MAJOR-2 ("store the ewe block as raw source text is
hand-waved; you can't recover verbatim sub-node source from an evaluated
`cue.Value`") is real **only** if you try to embed the program in config and pull
it back out. We don't.

## Resolution: a populator is a program, content-addressed

The ewe populator lives in its **own `.cue` file**, content-addressed into CAS;
the action carries a **digest**, never the program text. This is the exact idiom
mu already uses for plugins (a `.bb`/Go program hashed into CAS, referenced by
digest — `PluginDef.Digest`). The populator is a program you point mu at, not
config you inline.

```cue
// models/gitlab/populate.cue  — a normal CUE file, edited and `cue vet`-checked
// against a stub `op` package. Never loaded as mu config.
import "op"

_tok: op.#Secret & { args: ["GITLAB_TOKEN"] }
_repos: op.#HttpAll & { args: [{
    url:      "https://gitlab.com/api/v4/groups/garner-health/projects"
    headers:  { "PRIVATE-TOKEN": _tok.result }
    paginate: { param: "page", until: "empty" }
}]}
_out: [ for r in _repos.result if r.default_branch != null {
    name: "gitlab.com/\(r.path_with_namespace)", default_branch: r.default_branch
    _schema: "git.repository.gitlab"
}]
write: op.#WriteFile & { args: ["\(op.#Env.result.MU_OUT)/repos.json", json.Marshal(_out)] }
```

The action references it by path; mu hashes the file at plan time and carries the
digest:

```cue
plan: [{
    id:           "fetch"
    eweSource:    "models/gitlab/populate.cue"   // project-relative path
    outputs:      ["repos.json"]
    network:      true
    impure:       true
    sealed_inputs: { GITLAB_TOKEN: "env:GITLAB_TOKEN" }
}, "action/emit"]
```

### Why this beats the alternatives (recorded)

- **Inline `ewe: { …struct… }`** (as drawn in expressiveness-sketches.md): *not
  viable* — `op.#X` is live CUE that breaks load or is unified away (fact 3). The
  pretty inline form cannot survive load and stay runnable. Corrected here.
- **Inline `ewe: "…source string…"`**: survives load (a string is inert) but is a
  blob in config — no editor support, no `cue vet`, gross. Rejected.
- **Program-as-AST in process memory** (action carries an opaque id): never
  serializes → breaks remote/subprocess execution and muddies the cache key
  (you'd hash the content anyway = a digest = this design with extra steps).
  Rejected.
- **`populate` as a non-DAG phase** (like `observe`): reimplements the execution
  harness (sandbox, secret delivery, output capture, parallelism, retries) the
  action executor already provides, and flattens the intra-populate DAG (a real
  populator is fetch → batch-fetch → write — multiple actions). The loop that
  motivates a phase lives in **pudl** (`pudl run`, ledger V1), above mu — so mu
  never needed populate to be a phase. Rejected.

Content-addressed file is the sweet spot: "out" of the action as a blob, "in" the
pipeline as a cheap digest, reusing the plugin CAS idiom wholesale.

## The three integration points (all grounded in existing mechanisms)

### Point 1 — carry the program: `eweSource` path → CAS digest → `EweRef`

- `config.Target.Plan`/plan emission already passes arbitrary keys through
  `mapToActionSpec` (`coordinator.go:944-960`, "unknown keys silently ignored so
  plan programs can evolve"). Add an `eweSource` key.
- At plan/resolve time, hash the referenced file via the existing CAS hook
  **`cas.Store.Put(ctx, io.Reader) (Digest, error)`** (the same call plugins use)
  and set a new `Action.EweRef cas.Digest` field (`dag/graph.go`, beside `Body`).
- The path is resolved relative to `projectRoot` and confined to it (reuse the
  `resolve.go:55-58` project-root containment check).

```go
// dag/graph.go
type Action struct {
    // …existing…
    Body   []any        // pith VM program
    EweRef cas.Digest   // NEW: content digest of the ewe populator program
}
```

### Point 2 — execute: third dispatch arm + per-execute registry

`executeAction` dispatches Command vs pith `Body` today (`executor.go:239`). Add a
third arm: `EweRef` set → restore the program (`Store.Get(ctx, a.EweRef)`) and run
it through a **fresh per-execute `ewe.Registry`** — the ewe peer of
`pithvm.RegisterExecDrivers`:

```go
func (e *Executor) executeEwe(ctx context.Context, a *Action, env map[string]string) (int, error) {
    src, err := readBlob(ctx, e.Store, a.EweRef)        // CAS restore
    if err != nil { return 1, err }
    reg := newExecRegistry(e.sandboxFor(a), revealFor(a, env), env["MU_OUT"])
    _, err = ewe.NewProcessor(reg).ProcessSource(ctx, src)
    return exitFrom(err), err
}
```

The registry's functions close over **per-action** state, so it is built per
action, never global:
- **sandbox** → `#Http`/`#HttpAll`/`#HttpBatch`/`#Exec` confinement;
- **`reveal(name)`** → resolves against the action's `SealedInputs`, which the
  coordinator already resolved from `env:`/`pass:` refs and the executor already
  merges into the action env (`executor.go:527-540`). So
  `#Secret & {args:["GITLAB_TOKEN"]}` ties straight into existing sealed-input
  machinery; reveal happens only in sinks (secrets spec);
- **`MU_OUT`** → `#WriteFile` confinement, the same staging dir pith bodies write
  to (`executor.go:322-338`).

Outputs land in `$MU_OUT`, get copied to `WorkDir` (`executor.go:347-351`), hashed
and stored exactly as today — the ewe arm reuses the entire output path unchanged.

### Point 3 — cross-action data: declared inputs + `#ReadFile` (no `#Output`)

The rebuttal left `#Output` as a `/* */` comment. It is not needed — mu already
has a declared cross-action data mechanism:

- A producer action declares `outputs: ["repos.json"]`; the executor copies it to
  `WorkDir` (default `projectRoot`, shared across a target's actions —
  `resolve.go:83`) "so dependents can read them."
- A consumer action declares `inputs: {repos: "repos.json"}`. `Resolve`
  (`resolve.go:24-55`) hashes the input to a **content digest** (entering the cache
  key) **and**, when the path is produced by another action
  (`crossTargetProducers`), adds an **implicit `DependsOn` edge** to the producer.
- The consumer's ewe body reads it via `#ReadFile["repos.json"]` from its WorkDir.

So `Inputs` gives correctness (cache invalidation + DAG ordering) and WorkDir
sharing gives the bytes. Cross-action dataflow is `#ReadFile` of a **declared
input**, sequenced by the implicit dependency edge — the mu-idiomatic "dataflow
preferred" path, zero new functions. (A sugar `#Output["depId","name"]` over
`#ReadFile` can be added later if ergonomics demand; not in v1.)

This is also the canonical multi-action populator shape (E2/E5 fan-out): action 1
fetches repos → `repos.json`; action 2 declares it as input, batch-fetches
branches → `enriched.json`; action 3 writes the catalog. Each is one ewe body; the
DAG orders them; no effects-in-comprehensions, no pure-ordering primitive.

### Point 4 — cache key (honors K1 by construction)

`ComputeActionKey` already hashes `Body` as JSON (`actionkey.go:50-55`) and
sealed-input **refs, not values** (`:88-104`). The ewe arm adds one stanza:

```go
if a.EweRef != (cas.Digest{}) {
    fmt.Fprintf(h, "ewe:%s\n", a.EweRef.String())   // program DIGEST — no source, no values, no live HTTP
}
```

Hashing the program *digest* (already content-addressed) keeps the key cheap and
value-free. Populators are `impure` (ledger K1) so they skip cache anyway, but the
key stays well-defined for the day a pure ewe action exists.

## Authoring & tooling

- The populator file is a **real `.cue` file**: full editor support, formatting,
  and `cue vet` against a **stub `op` package** (definitions for `#Secret`,
  `#Http`, … with the arg/result shapes but no implementations). This answers
  MAJOR-2(b) "is it still type-checked CUE?" — yes, at author time; it is a checked
  artifact, not an opaque blob.
- A `mu lint`/`ewe vet` pass parses the file and runs ewe's comprehension-rejection
  (arg-resolution spec Change 3) and `#Secretf` placeholder checks statically,
  before execute.
- Co-location: populator files live beside the model (`models/<name>/populate.cue`).

## What this does not cover (boundaries)

- **Egress/exfil**: a populator can aim a secret at any sink it calls. That is a
  sandbox/network-policy concern (`network: true` + allowlist), not this spec —
  see the secrets spec's boundary section.
- **`#Plugin` inline** (ledger P2): invoking a plugin from inside an ewe body needs
  subprocess lifecycle; prefer the dataflow form (plugin `observe` as its own
  action, ewe consumes its output via a declared input). Tracked separately.
- **The `compute:` sugar** (target-level 1:1 auto-wrap of plan→action): ship the
  faithful `plan: [{…}, "action/emit"]` form first; add sugar only if 1:1
  dominates. Recorded in expressiveness-sketches.md.

## Tests

mu repo:
1. `eweSource` path → file hashed via `Store.Put`, `Action.EweRef` set; missing
   file → clear error; path escaping project root → rejected.
2. Execute arm: a trivial ewe program (`op.#Env` only) runs through
   `executeEwe`, writes a declared output, output copied to WorkDir + stored.
3. Per-execute registry: `#Secret` reveal ties to the action's `SealedInputs`;
   value absent from trace/log/cache key.
4. Cross-action: producer writes `a.json`; consumer declares it as input, ewe body
   `#ReadFile`s it; implicit `DependsOn` edge present; consumer cache key includes
   producer output digest.
5. Cache key: `EweRef` digest in key; sealed-input *values* excluded (regression
   over `actionkey` rules).
6. End-to-end: the corrected GitLab example 5 as a multi-action populator (fetch →
   batch → write) evaluates against a stub HTTP registry.

## Sequencing

1. `Action.EweRef` field + `mapToActionSpec` `eweSource` key + plan-time hashing
   via `Store.Put` + actionkey stanza (tests 1, 5).
2. `executeEwe` arm + `newExecRegistry` (closures over sandbox/reveal/MU_OUT) +
   `revealFor` tying into `SealedInputs` (tests 2, 3). Depends on the sink suite
   from the secrets spec.
3. Cross-action declared-input path validated for ewe bodies (test 4). No new code
   if `Inputs`/WorkDir behave as grounded; otherwise document the staging precisely.
4. Stub `op` package + `mu lint`/`ewe vet` static pass.
5. End-to-end multi-action populator (test 6) — the observe-only v1 gate.
6. Only then: `#SystemModel` schema + `pudl run` (P1/V1).
