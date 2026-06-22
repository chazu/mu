# V1 Build Spec — `#SystemModel` convergence

> **THIS IS THE CANONICAL DOCUMENT for the V1 convergence build.**
> It is self-contained: a builder needs only this file to know *what to build*.
> Everything else in this directory is vision, rationale archive, or detail —
> see the [Document map](#document-map) at the bottom. Where a decision needs its
> *why*, this doc links to the rationale; the link is optional reading.

**Status:** orchestration/policy design resolved (V1.1–V1.4, V1.6; V1.5 cut). The
**apply path** (translating desired-state drift into imperative actions a mutation
plugin accepts) is **identified but not yet designed** — see
[§10 Open / not-yet-designed](#10-open--not-yet-designed). Not built. Decision
rationale: [`issue-ledger.md`](issue-ledger.md) V1 section. Scope was corrected by
an adversarial source-grounded review (2026-06-21) — see §10.

---

## 1. What we're building

The **convergence loop** for `#SystemModel`, driven by `pudl run`. Observe-only
already works (inventory + drift-flag); convergence adds the half that **closes**
drift by mutating the target system, iterating to a fixed point.

Convergence is the ACUTE loop: **A**ccumulate (observe) → **C**onfigure → **U**nify
(drift) → **T**ransform (drift→actions) → **E**xecute (apply), repeated until
`observed == desired`.

**What ships vs what's new** (be honest — an earlier draft overclaimed this):
- **Ships:** the *observe* substrate (drift check, catalog, status enum) and the
  *orchestration scaffold* (pudl shells `mu build`; `mu build --emit-manifest |
  pudl mu ingest-manifest` records execution results). See [§9 Grounding](#9-grounding--what-ships-vs-what-is-new).
- **New:** the loop itself; the **apply translation** (desired-state →
  per-toolchain imperative actions) — this is *not* a wiring task, the current
  `export-actions` path emits desired-state targets for observe/BRICK cases, not
  apply directives for arbitrary mutation plugins; a one-line status-semantics fix
  to `ingest-manifest`; and (prerequisite) the observe-only `pudl run` + the
  ewe-populate path, which is specced but unbuilt.

---

## 2. The unit of a run: a `#SystemModel` *instance*

`pudl run` operates on an **instance** of `#SystemModel`, not the definition.
This is the CUE idiom every example uses (`examples/04-dns-zone/model.cue:10`):

```cue
dnsZone: #SystemModel & { name: "dns-example-com", desired: [ ... ], converge: ... }
```

- `#SystemModel` (with `#`) = the **schema**. It has no concrete populate target or
  desired data — it cannot be run.
- `dnsZone` (no `#`) = a concrete **instance** unifying with the schema. This is
  what runs.

**Three levels — the run is at the top, status is at the bottom:**

```
#SystemModel              schema (shape: populate/desired/converge/…)
  └─ dnsZone (INSTANCE)   ← pudl run's unit; the ACUTE loop runs over ONE instance
       └─ definitions     ← the instance's `desired` entries (e.g. dns.#Record values)
            └─ status      ← catalog status is PER-DEFINITION (drifted/converging/converged/failed)
```

- **Instance = the run/orchestration unit.** One `pudl run <model>` = one
  instance's loop. The fixed point (`drift == ∅`) is over **all the instance's
  definitions**.
- **Definition = the status / `--only` unit.** Drift, `ingest-manifest`, and
  `--only` all key on definition name (`defName`,
  `pudl/internal/mubridge/manifest.go:133`).
- **`<model>` resolves to a build target** `//models/<model>` (per
  `README.md:436` and the `pudl memory cycle` precedent, which builds
  `//memory:cycle`). The instance's `name:` field is its stable display identity in
  reports. Resolving by target avoids evaluating CUE just to locate the instance.

---

## 3. CLI contract

`<model>` = a `#SystemModel` instance, resolved to the `//models/<model>` target.

| Invocation | Behaviour |
|------------|-----------|
| `pudl run <model>` | **observe-only** (default): `populate → drift → checks → report`. **No mutation.** Stops at observation fixed point. |
| `pudl run <model> --converge` | Opt into the convergence loop. Converges **all** drifted definitions in the instance. |
| `pudl run <model> --converge --only a,b` | Converge **only** the named definitions. Drift still computed instance-wide; Transform filters emitted actions to the selected set. |
| `pudl run <model> --converge --dry-run` | Print the **plan** (the translated `MuConfig`) and **execute nothing, write no status**. Single pass. |
| `pudl run <model> --converge --max-iters N` | Override the loop cap (default 5). |

**Rules:**
- **`--converge` is the gate.** Production mutation never happens without it.
  Observe-only is the safe default; a convergence-*capable* instance (one declaring
  `desired` + `converge`) still behaves observe-only under a bare `pudl run`.
- **`--only` and `--dry-run` both require `--converge`** (else error). One rule:
  convergence flags need the convergence gate. Prevents accidental firing by
  naming a resource.
- **`--only` selects on definition name** (the unit drift / `ingest-manifest`
  key on).
- **`--dry-run` is inherently single-pass** — iterations 2+ depend on execution
  actually changing live state, which dry-run does not do. It shows "what
  iteration 1 would hand mu." Respects `--only`. **Must not write catalog status**
  — build the `MuConfig` via the translation directly and print it; do **not**
  route through the `export-actions` *command*, which couples in a `markConverging`
  DB write (`pudl/cmd/export_actions.go:143-146`). (mu's own `mu build --plan`,
  `cmd/mu/build.go:26`, is the clean execute-nothing path on the mu side.)

---

## 4. The loop

```
resolve instance //models/<model>; enumerate its definitions
populate                       # initial observe (the observe-only pass already does this)
loop:
  drift                        # Unify: drift.Check vs desired, over the instance's definitions
  if drift == ∅:   → (drift writes "converged"), break        # fixed-point test at TOP
  if iters >= cap: → loop writes "failed" (cap_exhausted), break   # safety stop
  converge:  translate drift+`converge` arm → MuConfig         # Transform (apply translation, §10)
  execute:   mu build --emit-manifest | pudl mu ingest-manifest # Execute + record
             # ingest records per-action entries and returns a Failed count
  if ingest Failed > 0: → loop writes "failed" (execute_error), break  # see §6 partial state
  populate                     # re-observe at BOTTOM
```

**Why re-observe each iteration** (not apply-once): it (a) *verifies* the live
system actually reached desired — `mu` reporting an action ran ≠ the world
changed — and (b) handles dependency chains (create parent → child now appliable).
Drift compares declared keys against the **latest observe artifact**
(`pudl/internal/drift/checker.go:101-114`), so re-observe each iteration is
load-bearing, not optional.

**Fixed point:** `drift == ∅` (every definition in the instance clean, no
`Differences`).
**Termination guarantee:** the hard max-iter **cap** — bounded, always halts.
Default 5 (set-difference consumers converge in 1–2; dependency chains 2–3).

---

## 5. How the loop composes with `ingest-manifest` (the Execute arm)

This is the key mechanical decision, so it gets its own section.

A **mu build manifest** (`mu build --emit-manifest`, `mu/cmd/mu/build.go:112-113`)
is mu's after-action execution report: `{timestamp, summary{total,cached,executed,
failed}, actions:[{id,target,cached,exit_code,outputs}]}`
(`pudl/internal/mubridge/manifest.go:16-37`).

`pudl mu ingest-manifest` (`manifest.go:50`) is the **feedback channel from mu's
execution back into pudl's catalog**: it content-hash dedups, stores a per-action
catalog entry (audit + outputs), and returns a `Failed` count.

**The loop reuses `ingest-manifest` as its Execute-and-record arm** — it is *not* a
competitor:
- It records per-action results (the audit trail, outputs for downstream).
- Its returned `Failed` count **is** the loop's `execute_error` signal (V1.4).

**The one conflict, and the fix.** Today `ingest-manifest` also writes a per-def
status `converged`/`failed` from action **exit code** (`manifest.go:182-188`). But
exit-0 means *the apply command ran*, **not** *the world now matches desired* —
that is the "action ran ≠ world changed" lie, and it ships today in the loop-less
path (which has no re-verify).

**Fix (one line at `manifest.go:182`):** on exit 0, write **`converging`**
("applied, pending verification"), not `converged`. Exit≠0 stays `failed`. Then
**only the drift check ever writes `converged`** (`checker.go:101-103`, and it only
does so when `Differences == ∅`). Result: `converged` means *observed == desired,
verified* everywhere, with **zero new enum values** and **no overwrite race**. This
also fixes the loop-less path (a manual apply that exits 0 but didn't fully
converge now honestly sits at `converging`, not a false `converged`).

Healthy 1-iteration timeline:
`drifted → converging (export) → converging (applied, exit 0) → [re-observe, drift ∅] converged`.

---

## 6. Build units

Build order: prerequisite (§2/§9) → V1.6 apply translation (§10 — design it first)
→ V1.1 (loop) → V1.2 (termination) → V1.4 (failure reporting). V1.3 is satisfied by
the V1.1 flag work. The `ingest-manifest` status fix (§5) lands with V1.1.

### V1.6 — `converge` field path (`#PluginPlan` only)
**Build:**
1. Write the `#PluginPlan` CUE def (`plugin: string`, `input: {...}`). Today it's
   a README sketch only (`grep '#PluginPlan:'` over mu+pudl `.cue` → zero).
2. **The apply translation** — read the model's `converge: #PluginPlan{plugin,
   input}` arm + the drift diff, and produce a `MuConfig` whose target the named
   plugin will accept. **This is net-new** — see [§10](#10-open--not-yet-designed).
   No Go code reads a `converge` field today, and the existing `export-actions`
   path emits desired-state keys, not apply directives.
3. Schema for V1: `converge?: #PluginPlan` (narrowed from
   `#EweTarget | #PluginPlan`).

**Decisions baked in:** all 5 worked examples use `#PluginPlan`. **ewe-converge
(`#EweTarget` mutate) is deferred** — zero consumers. **ewe-*populate* is a
separate, must-have path** (pull state, e.g. GitLab) — unaffected by this cut, but
**unbuilt** (see §9/§10), not "already shipping."

**Done when:** an instance with a `converge: #PluginPlan` arm produces a `MuConfig`
that the named plugin executes to actually close drift (verified by a subsequent
drift==∅), for at least one real plugin.

### V1.1 — the loop in `pudl run`
**Build:** resolve `//models/<model>` to an instance; enumerate its definitions;
sequence the phases (§4); thread state across iterations; bounded outer loop; the
CLI flags from §3; the `ingest-manifest` status fix (§5).

**Decisions baked in:** pudl-driven (per-phase `mu build` invocations, pudl owns
the loop — drift+converge are pudl's brain, can't collapse into one mu target);
re-observe at bottom, fixed-point test at top, initial populate before the loop;
the instance is the run unit, definitions the status unit (§2).

**Done when:** `pudl run <model> --converge` runs the loop happy-path on a
convergent instance and reaches `converged`; `--only` / `--dry-run` behave per §3.

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
2. `execute_error` — `ingest-manifest` returned `Failed > 0` for the iteration's
   `mu build` (an action exited nonzero). The detector already exists (§5).

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

## 7. Cut & deferred (do NOT build)

| Item | Disposition | Revisit-trigger |
|------|-------------|-----------------|
| **V1.5 — partial-apply / rollback** | ❌ **CUT** (owner decision) | Out of scope, period. Execute is best-effort; failures are *reported* (V1.4), never *undone*. This is the acknowledged production-mutation risk V1 accepts. |
| **Monotonic-drift-shrink guard** (V1.2) | ⏸ deferred | First model that oscillates within a loop |
| **ewe-converge** (`#EweTarget` mutate, V1.6) | ⏸ deferred | First model needing custom mutation logic a plugin `apply` op can't express |

---

## 8. Status vocabulary

The catalog status enum already holds every value needed — **no schema migration.**
`UpdateStatus` (`pudl/internal/database/catalog_status.go:18-34`) is a blind set
with a validity-check only (no FSM transition guard), so the loop cycles statuses
freely. Valid set: `unknown/clean/drifted/converging/converged/failed`
(`catalog_status.go:20-21`).

| status | meaning | written by |
|--------|---------|-----------|
| `clean` / `drifted` | observed == / ≠ desired, observe signal | `drift check` (ships, `checker.go:101-117`) |
| `converging` | mid-loop: actions emitted **or** applied-but-not-yet-verified | `export-actions` (ships); `ingest-manifest` after the §5 fix |
| `converged` | drift == ∅, **verified** | **`drift check` only** (after the §5 fix; ships, semantics narrowed) |
| `failed` | `cap_exhausted` or `execute_error` | the loop (V1.2/V1.4); `ingest-manifest` on exit≠0 |

**Correction (was wrong in an earlier draft):** `converged`/`failed` are **already
written today** — by `ingest-manifest` on action exit code (`manifest.go:182-188`).
The V1 change is therefore *not* "add new writes"; it is **narrowing**
`ingest-manifest` (exit-0 → `converging`, not `converged`, §5) so the verified
`converged` is owned solely by the drift check. That removes a latent lie rather
than filling a blank.

---

## 9. Grounding — what ships vs what is new

Verified against source (mu/pudl/ewe, 2026-06-21).

**Ships (reuse as-is or with the §5 one-liner):**

| Piece | Location | Note |
|-------|----------|------|
| drift check — idempotent, re-enterable | `pudl/internal/drift/checker.go:101-117` | compares declared vs latest observe artifact; writes clean/drifted/converged |
| catalog status enum + blind `UpdateStatus` | `pudl/internal/database/catalog_status.go:18-34` | no FSM guard; all needed values valid |
| `export-actions` — emits `MuConfig`, marks `converging` | `pudl/cmd/export_actions.go:139,143-146` | **observe/BRICK shape**, see §10; emit coupled to a status write |
| Execute + record | `mu build --emit-manifest` (`mu/cmd/mu/build.go:112`) → `pudl mu ingest-manifest` (`pudl/internal/mubridge/manifest.go:50`) | the loop's Execute arm; returns `Failed` count |
| orchestration pattern (pudl shells mu) | `pudl/cmd/memory.go:177` | precedent for `pudl run` |
| mu plugin protocol (apply-capable) | `mu/sdk/muplugin/plugin.go:33,37`; `mu build` executes, `--plan` dry-runs | the execution substrate is real |

**New (must be built — NOT reuse):**

| Piece | Why it's new |
|-------|--------------|
| `pudl run` / `cmd/run.go` | does not exist (`ls cmd/run.go` → absent). Observe-only `pudl run` is itself the prerequisite. |
| **apply translation** (desired-state → imperative plugin actions) | the riskiest piece — see §10. The shipped `export-actions` emits `Target.Config = DeclaredKeys` (desired state); plugins like `remote-exec` need imperative `config.command`/`config.host` (`mu/plugins/remote-exec/main.go:66-71`). Shapes don't match. |
| `#PluginPlan` CUE def + arm reader | sketch only; no Go reads a `converge` field |
| `ingest-manifest` status narrowing | the §5 one-liner |
| **ewe-populate** (auth'd HTTP fetch → catalog) | ewe has **zero** HTTP code, zero `EweTarget`/`HttpAll`/`auth.bearer`, zero `.cue` files (verified in ewe). It is *specced* (`ewe-*-spec.md`) but unbuilt. |

---

## 10. Open / not-yet-designed

Surfaced by the 2026-06-21 adversarial review (grounded in mu/pudl/ewe source).
These are **real design gaps**, not wiring. They must be designed before or during
the build; the orchestration decisions above assume them solved.

1. **Apply translation (the core gap).** Convergence needs to turn a drift *diff*
   plus a `#PluginPlan` arm into imperative actions a mutation plugin accepts. The
   existing `export-actions` path produces *desired-state* `MuConfig` targets for
   observe/BRICK cases, and plugins' config vocabularies differ
   (`remote-exec` wants `command`/`host`, `mu/plugins/remote-exec/main.go:66-71`;
   `k8s` reads a manifest from `sources`, `mu/plugins/k8s/plugin.bb:48,125`). No
   code computes apply directives from a diff. **This is the riskiest unsolved
   piece; design it first (it is the real content of V1.6 step 2).**

2. **ewe-populate path.** Pull external state (the GitLab/DNS cases) is a must-have
   observe path but is unbuilt in ewe (no HTTP/auth/fetch). It is a **prerequisite**
   for the convergence examples, and belongs to the observe-only milestone (§2/§9).
   The `ewe-*-spec.md` files are its design.

3. **Missing example plugins.** Of the example converge plugins only `remote-exec`
   ships; `cloudflare-dns` (example 4) does **not** exist in `mu/plugins/`, and DNS
   set-difference apply (compute POST/PUT/DELETE from a diff) is unbuilt. Treat the
   examples as *target consumers*, not executable-today proofs.

Review artifact: the full findings (F1–F8, each with `file:line` evidence) are
summarized in the commit that introduced this section; re-run the review after the
apply-translation design lands.

---

## 11. Example consumers

The convergence instances V1 must serve (under [`examples/`](examples/)):

| # | Model | converge arm | plugin ships? |
|---|-------|--------------|---------------|
| 1 | [Remote server](examples/01-remote-server/) | `#PluginPlan` (`remote-exec`) | ✅ `remote-exec` |
| 3 | [TLS certs](examples/03-tls-certs/) | `#PluginPlan` | ⚠️ verify plugin |
| 4 | [DNS zone](examples/04-dns-zone/) | `#PluginPlan` (`cloudflare-dns`, textbook set-difference) | ❌ no `cloudflare-dns` plugin (§10) |
| 2 | [k8s policy](examples/02-k8s-policy/) | optional `#PluginPlan` upgrade (`k8s`) | ✅ `k8s` (apply shape: §10) |
| 5 | [repo governance](examples/05-repo-governance/) | optional `#PluginPlan` upgrade (`gitlab`) | ⚠️ verify plugin |

All converge arms are flagged `# V1-OPEN` in their `model.cue` — orchestration
design resolved, apply-translation + some plugins pending. None use ewe-converge.

---

## Document map

Where everything for this effort lives, and which file owns what:

| File | Role |
|------|------|
| **`V1-BUILD-SPEC.md`** (this) | **THE build doc — what to build for V1 convergence.** Start here. |
| [`README.md`](README.md) | Vision / the `#SystemModel` concept, observe-only + convergence overview. The front door. |
| [`issue-ledger.md`](issue-ledger.md) | Decision rationale archive — every resolved issue (observe-only E1–E6…, and V1.1–V1.6) with the *why*. Source of truth for decisions; this spec distills its V1 section. |
| [`examples/`](examples/) | Five worked `#SystemModel` consumers (structured: per-model `model.cue` + README). |
| `ewe-*-spec.md` | ewe detail specs (arg-resolution, secrets, body-kind, http-pagination) — the design for the unbuilt ewe-populate path (§10). |
| [`../dialectics/`](../dialectics/)`*.ndjson` | Recorded dlktk arguments (`v1-2-loop-termination`, `e5-pure-ordering`). The auditable "why" behind the hard calls. |
| [`archive/`](archive/) | **Historical** — the adversarial reviews, `ewe-extensions.md`, and the flat `examples.md` that drove the design. Superseded by the above; kept for trail. |
