# Handoff — V1 convergence (system models, pudl/mu/ewe)

Next session focus: **design the convergence half of `#SystemModel`** (ledger V1).
Everything else (observe-only) is specced, committed, pushed.

## Where things stand

Repo: `/Users/chazu/dev/go/mu` (also `/Users/chazu/dev/go/pudl`, `/Users/chazu/dev/go/ewe`).
Branch: **`design/system-models-issue-resolution`** (6 commits, pushed to origin).

The two adversarial reviews of the ewe-in-CUE system-models design have been worked
through end-to-end. **All CRITICAL/MAJOR findings resolved except M2 (convergence).**
Observe-only is fully specced; convergence is scoped-only and untouched.

**Read these first (do not re-derive — they hold the decisions):**
- `docs/design/system-models/issue-ledger.md` — the master tracker. Every resolved
  issue (E1–E6, S1, K1, I1, I2, P1, P2, V2, V3), the decided design points
  (D1–D4), and **the V1 section at the bottom** (the agenda for this session).
- `docs/design/system-models/README.md` — vision doc, reconciled to the ledger for
  observe-only; **convergence sections (`desired`/`converge`, fixed points, ACUTE)
  are deliberately unreconciled — V1 revises them.**
- Specs: `ewe-arg-resolution-spec.md`, `ewe-secrets-spec.md`, `ewe-body-kind-spec.md`,
  `ewe-http-pagination-spec.md` (all under `docs/design/system-models/`).
- `docs/design/system-models/examples/` — five worked examples. **Examples 1, 3, 4
  are convergence models** with `converge:` arms flagged `# V1-OPEN` — they are the
  concrete consumers V1 must serve.
- `docs/design/dialectics/e5-pure-ordering.ndjson` — a recorded dlktk dialectic
  (precedent for arguing out the hard cells).

## V1 agenda (full detail in the ledger's V1 section)

Convergence = the ACUTE loop (Accumulate→Configure→Unify→Transform→Execute, repeat
to a fixed point). **Most single-iteration primitives already ship** — `mu observe`,
`pudl drift check` (one-shot), `pudl export-actions` (one-shot, marks `"converging"`),
`mu build`, `desired` + `plan`-op plugins. The gap is **orchestration + policy +
safety**, six open points:

- **V1.1** — the loop in `pudl run` (re-observe→drift→transform→execute→repeat).
- **V1.2 — HARD** — termination / fixed-point (stop at drift=∅; guard: max iters,
  drift must monotonically shrink).
- **V1.3** — drift-gating (converge fires vs only flags; severity / opt-in).
- **V1.4** — convergence-failure reporting (applied-but-still-drifts / drift-grew /
  mid-loop action fail; distinct from drift).
- **V1.5 — HARD** — partial-apply / rollback (execute fails halfway against a live
  system). The first place the system **mutates production**.
- **V1.6** — `converge` field paths (`#PluginPlan` ≈ exists via export-actions; the
  ewe-converge `#EweTarget` path is newer).

Rough size (recorded, approximate): ~40–50% the breadth of observe-only, but
front-loaded — V1.2 and V1.5 are the project's two hardest cells.

## Do this FIRST in the V1 session (the grounding caveat)

Before designing the loop, **verify `pudl drift check` and `pudl export-actions` are
loop-ready** — i.e. re-enterable across iterations. Specifically: is the
`"converging"` def-state (`pudl/cmd/export_actions.go:151-167`) re-enterable, and is
`pudl/internal/drift/checker.go:117-123` callable in a cycle without one-shot
assumptions? If they bake in single-pass assumptions, V1.1 grows. This is a quick
read, not a deep dive, and it bounds the loop work.

## Working method (what the user expects — match it)

This project's owner runs design sessions a specific way; honor it:
- **Ground every claim in real source before asserting.** This session repeatedly
  caught errors by running throwaway tests / greps against actual mu/pudl/ewe code
  rather than trusting the docs. Do the same — the reviews punished assertion.
- **One issue at a time.** Give the rundown + tradeoffs + *your lean*, then discuss.
  Don't batch-resolve. The user decides; you record.
- **Record as you go** — update the ledger after each resolution, keep README in
  sync, commit at natural checkpoints. Branch is already off main.
- **Default to deferral/cut** when there's no concrete v1 consumer (the YAGNI
  pattern used throughout: E5, Tier-2, `#Plugin`, pure-transform cache).
- Caveman mode is active (terse). Code/commits/specs written normally.
- Commit trailers used this project (see recent commits for exact form):
  `Co-Authored-By: Claude Opus 4.8 (1M context)` + `Claude-Session:` line.

## Suggested skills

- **`dlktk-dialectic`** — use for V1.2 and V1.5 (the two HARD cells). Both have rival
  framings worth arguing out and recording, not asserting. Precedent: the E5
  dialectic (`docs/design/dialectics/e5-pure-ordering.ndjson`), which independently
  re-derived the team's decision via grounded semantics. Export each to
  `docs/design/dialectics/` and link from the ledger.
- **`grill-me`** — optionally, to stress-test the loop/termination design (V1.1/V1.2)
  before committing to it.
- The **caveman** mode is already active (terse responses).

## Key open framing the user flagged at session start

The user's original ask was "a very clear notion of the end goal of implementing the
IDEA cycle via the SystemModel concept." V1 *is* that end goal — pin what `pudl run`
converges to in each mode (observation fixed point vs convergence fixed point) and
make the convergence loop concrete. The README already records a "fork" (mu-driven
vs pudl-driven loop) under its convergence sections — that fork is a live V1
question and a good dialectic candidate.
