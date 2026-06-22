# V1 Build Spec — `#SystemModel` convergence

> **THIS IS THE CANONICAL DOCUMENT for the V1 convergence build.**
> It is self-contained: a builder needs only this file to know *what to build*.
> Everything else in this directory is vision, rationale archive, or detail —
> see the [Document map](#document-map) at the bottom. Where a decision needs its
> *why*, this doc links to the rationale; the link is optional reading.

**Status:** design fully resolved (V1.1–V1.4, V1.6 resolved; V1.5 cut). Not yet
built. Source of truth for decisions: [`issue-ledger.md`](issue-ledger.md) V1
section. This spec is the build-facing distillation of those decisions.

---

## 1. What we're building

The **convergence loop** for `#SystemModel`, driven by `pudl run`. Observe-only
already works (inventory + drift-flag); convergence adds the half that **closes**
drift by mutating the target system, iterating to a fixed point.

Convergence is the ACUTE loop: **A**ccumulate (observe) → **C**onfigure → **U**nify
(drift) → **T**ransform (drift→actions) → **E**xecute (apply), repeated until
`observed == desired`.

**Most single-iteration primitives already ship** (see [§8 Grounding](#8-grounding--what-already-ships)).
The V1 work is **orchestration + policy**, not new execution primitives.

---

## 2. Prerequisite (build this first)

**Observe-only `pudl run` must land before the convergence loop.** `cmd/run.go`
**does not exist yet** — today there is `pudl exec` (raw pith VM) and `pudl memory
cycle` (one-shot `mu build`). The convergence loop *wraps* the observe-only single
pass, so:

1. **First:** observe-only `pudl run <model>` — `populate → relations → checks →
   report`, delegating execution to `mu build` (orchestration pattern already
   shipped: `pudl memory cycle` shells `exec.Command("mu","build",…)`,
   `pudl/cmd/memory.go:177-182`). This *replaces* `pudl exec`.
2. **Then:** the convergence arms below.

The loop-owner question is **settled: pudl-driven.** `pudl run` orchestrates the
loop; `mu build` executes each phase; mu stays the dumb executor. (Same split the
observe-only design already adopted — convergence does not reopen it once rollback
is cut.)

---

## 3. CLI contract

| Invocation | Behaviour |
|------------|-----------|
| `pudl run <model>` | **observe-only** (default): `populate → drift → checks → report`. **No mutation.** Stops at observation fixed point. |
| `pudl run <model> --converge` | Opt into the convergence loop. Converges **all** drifted resources. |
| `pudl run <model> --converge --only a,b` | Converge **only** the named definitions. Drift still computed whole-model; Transform filters emitted actions to the selected set. |
| `pudl run <model> --converge --dry-run` | Print the **plan** (the `mu.json` `export-actions` emits) and **execute nothing**. Single pass. |
| `pudl run <model> --converge --max-iters N` | Override the loop cap (default 5). |

**Rules:**
- **`--converge` is the gate.** Production mutation never happens without it.
  Observe-only is the safe default; a convergence-*capable* model (one declaring
  `desired` + `converge`) still behaves observe-only under a bare `pudl run`.
- **`--only` and `--dry-run` both require `--converge`** (else error). One rule:
  convergence flags need the convergence gate. Prevents accidental firing by
  naming a resource.
- **`--only` selects on definition name** (the unit drift / `export-actions`
  already key on).
- **`--dry-run` is inherently single-pass** — iterations 2+ depend on execution
  actually changing live state, which dry-run does not do. It shows "what
  iteration 1 would hand mu." Respects `--only`.

---

## 4. The loop

```
populate                       # initial observe (the observe-only pass already does this)
loop:
  drift                        # Unify: drift.Check vs desired
  if drift == ∅:   → mark "converged", break       # fixed-point test at TOP
  if iters >= cap: → mark "failed" (cap_exhausted), break   # safety stop
  converge → execute           # Transform (export-actions) → Execute (mu build)
  if execute errored: → mark "failed" (execute_error), break  # see §6 partial state
  populate                     # re-observe at BOTTOM
```

**Why re-observe each iteration** (not apply-once): it (a) *verifies* the live
system actually reached desired — `mu` reporting an action ran ≠ the world
changed — and (b) handles dependency chains (create parent → child now appliable).

**Fixed point:** `drift == ∅` (every definition clean, no `Differences`).
**Termination guarantee:** the hard max-iter **cap** — bounded, always halts.
Default 5 (set-difference consumers converge in 1–2; dependency chains 2–3).

---

## 5. Build units

Build order: prerequisite (§2) → V1.6 (schema+wiring) → V1.1 (loop) → V1.2
(termination) → V1.4 (failure reporting). V1.3 is satisfied by the V1.1 flag work.

### V1.6 — `converge` field path (`#PluginPlan` only)
**Build:**
1. Write the `#PluginPlan` CUE def (`plugin: string`, `input: {...}`). Today it's
   a README sketch only.
2. Wire a model's `converge: #PluginPlan{plugin,input}` arm to the
   `export-actions` invocation inside the loop's Transform step. Today
   `export-actions` runs standalone on drift reports; V1.6 connects the model
   field → that call.
3. Schema for V1: `converge?: #PluginPlan` (narrowed from
   `#EweTarget | #PluginPlan`).

**Decisions baked in:** all 5 worked examples use `#PluginPlan`; the
`export-actions → MuConfig → mu build` path ships (`ActionSpec`/`MuConfig` already
match mu's plugin protocol, `pudl/internal/mubridge/export.go:141`). **ewe-converge
(`#EweTarget` mutate) is deferred** — zero consumers. **ewe-*populate* is
untouched** — pulling external state (e.g. GitLab) stays a first-class observe
path (`populate?: #EweTarget | #PluginObserve`).

**Done when:** a model with a `converge: #PluginPlan` arm produces a valid mu.json
via the export-actions path.

### V1.1 — the loop in `pudl run`
**Build:** accept the convergence arms; sequence the five phases (§4); thread
state across iterations; bounded outer loop; the CLI flags from §3.

**Decisions baked in:** pudl-driven (per-phase `mu build` invocations, pudl owns
the loop — drift+converge are pudl's brain, can't collapse into one mu target);
re-observe at bottom, fixed-point test at top, initial populate before the loop.

**Done when:** `pudl run m --converge` runs the loop happy-path on a convergent
model and reaches `converged`; `--only` / `--dry-run` behave per §3.

### V1.2 — termination / fixed point
**Build:** the `drift == ∅ → converged` test; the `iters >= cap → failed` stop;
`--max-iters` flag (default 5).

**Decisions baked in:** the cap is the halting proof. **The monotonic-drift-shrink
guard is DEFERRED, not built** — no consumer oscillates within a loop (all
set-difference convergent); the cap already bounds any future oscillator. Argued
out and recorded:
[`../dialectics/v1-2-loop-termination.ndjson`](../dialectics/v1-2-loop-termination.ndjson)
(grounded semantics: cap+drift==∅ justified, guard defeated, robust through a full
steelman). **Revisit-trigger:** first real oscillating consumer.

**Done when:** loop stops at drift==∅ (→`converged`) or at the cap (→`failed`),
and never spins unbounded.

### V1.3 — drift-gating
**Satisfied by V1.1.** The gate *is* the `--converge` flag: explicit opt-in, **no
severity-threshold magic**. No separate work.

### V1.4 — convergence-failure reporting
**Build:** write `failed` on the two failure modes; report content.

**Two failure modes** (both → `failed` + a reason):
1. `cap_exhausted` — hit `--max-iters`, residual drift ≠ ∅.
2. `execute_error` — `mu build` returned nonzero during a converge iteration.

**Mandatory partial-state warning.** On `execute_error`, with rollback cut (§6),
the loop **stops, marks `failed`, leaves the half-applied state**. The report
**MUST** state the live system may be in a partial state with no rollback. This is
non-negotiable — silent partial-apply would be the real failure.

**Report carries:** mode, iteration count, residual drift set, and (for
`execute_error`) the failing action + the partial-state warning. This is the
**convergence analog of V2** (per-target status / partial-failure, observe-only).

**Done when:** both failure modes produce `failed` + a report distinguishable from
ordinary drift, and `execute_error` always emits the partial-state warning.

---

## 6. Cut & deferred (do NOT build)

| Item | Disposition | Revisit-trigger |
|------|-------------|-----------------|
| **V1.5 — partial-apply / rollback** | ❌ **CUT** (owner decision) | Out of scope, period. Execute is best-effort; failures are *reported* (V1.4), never *undone*. This is the acknowledged production-mutation risk V1 accepts. |
| **Monotonic-drift-shrink guard** (V1.2) | ⏸ deferred | First model that oscillates within a loop |
| **ewe-converge** (`#EweTarget` mutate, V1.6) | ⏸ deferred | First model needing custom mutation logic a plugin `apply` op can't express |

---

## 7. Status vocabulary

The catalog status enum already has every value needed — **no schema migration.**
`UpdateStatus` (`pudl/internal/database/catalog_status.go:18`) is a blind set with
validity-check only (no FSM transition guard), so the loop cycles statuses freely.

| status | meaning | written by |
|--------|---------|-----------|
| `drifted` | world ≠ desired, **untouched** (observe signal) | `drift check` (ships) |
| `converging` | mid-loop (export-actions ran) | `export-actions` (ships) |
| `converged` | loop reached drift==∅ | **V1.2 (new write)** |
| `failed` | loop ran, couldn't converge | **V1.4 (new write)** |

`converged` and `failed` are valid in the enum (`catalog_status.go:21`) but
**nothing writes them today** — they are the pre-existing terminal vocabulary V1.2
and V1.4 fill in.

---

## 8. Grounding — what already ships

Verified against source; the loop **reuses** these, does not rebuild them:

| ACUTE phase | Ships as | Location |
|-------------|----------|----------|
| Accumulate (observe) | `mu observe` / populate path | (specced) |
| Unify (drift) | `pudl drift check` — one-shot, idempotent, re-enterable | `pudl/internal/drift/checker.go:117` |
| Transform (drift→actions) | `pudl export-actions` — emits mu.json, marks `converging` | `pudl/cmd/export_actions.go:139,153` |
| Execute | `mu build` runs the actions | — |
| orchestration pattern | pudl shells `mu build` | `pudl/cmd/memory.go:177` |
| action format | `ActionSpec`/`MuConfig` match mu plugin protocol | `pudl/internal/mubridge/export.go:80,141` |

**Loop-readiness confirmed:** drift check / export-actions / UpdateStatus are all
idempotent and re-enterable across iterations. V1.1 stays "wiring existing
one-shots into a cycle" — no growth.

---

## 9. Example consumers

The convergence models V1 must serve (under [`examples/`](examples/)):

| # | Model | converge arm |
|---|-------|--------------|
| 1 | [Remote server](examples/01-remote-server/) | `#PluginPlan` |
| 3 | [TLS certs](examples/03-tls-certs/) | `#PluginPlan` |
| 4 | [DNS zone](examples/04-dns-zone/) | `#PluginPlan` (textbook set-difference) |
| 2 | [k8s policy](examples/02-k8s-policy/) | optional `#PluginPlan` upgrade |
| 5 | [repo governance](examples/05-repo-governance/) | optional `#PluginPlan` upgrade |

All converge arms are flagged `# V1-OPEN` in their `model.cue` — design resolved,
code pending. None use ewe-converge.

---

## Document map

Where everything for this effort lives, and which file owns what:

| File | Role |
|------|------|
| **`V1-BUILD-SPEC.md`** (this) | **THE build doc — what to build for V1 convergence.** Start here. |
| [`README.md`](README.md) | Vision / the `#SystemModel` concept, observe-only + convergence overview. The front door. |
| [`issue-ledger.md`](issue-ledger.md) | Decision rationale archive — every resolved issue (observe-only E1–E6…, and V1.1–V1.6) with the *why*. Source of truth for decisions; this spec distills its V1 section. |
| [`examples/`](examples/) | Five worked `#SystemModel` consumers (structured: per-model `model.cue` + README). |
| `ewe-*-spec.md` | Observe-only ewe detail specs (arg-resolution, secrets, body-kind, http-pagination). Relevant when building the populate path. |
| [`../dialectics/`](../dialectics/)`*.ndjson` | Recorded dlktk arguments (`v1-2-loop-termination`, `e5-pure-ordering`). The auditable "why" behind the hard calls. |
| [`archive/`](archive/) | **Historical** — the adversarial reviews, `ewe-extensions.md`, and the flat `examples.md` that drove the design. Superseded by the above; kept for trail. |
