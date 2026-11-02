# Handoff — V1 convergence BUILD session

Design is **complete**. Next session: **build**. Everything below orients you;
the detail lives in the specs.

## THE document

**[`V1-BUILD-SPEC.md`](V1-BUILD-SPEC.md)** — canonical, self-contained. Read it
first. It carries the CLI contract, the loop, per-unit build work + done-when, the
status vocabulary, ships-vs-new grounding, and a doc map. Everything else is
rationale (`issue-ledger.md`), per-area specs, or worked examples.

## Where things stand

- Repo `/Users/chazu/dev/go/mu` (+ `/Users/chazu/dev/go/pudl`, `/Users/chazu/dev/go/ewe`).
- Branch **`design/system-models-issue-resolution`**, pushed (origin at `f0f14f8`
  or later). Working tree clean.
- **All V1 design resolved or cut.** Survived an adversarial source-grounded review
  (the spec was corrected where it overclaimed; every decision traces to real
  `file:line`). No open design questions remain — what's left is *build*.

What this (design) session produced, beyond the original convergence design:
- Resolved the **apply path** (plugin owns translation; pudl routes `desired` as
  sources) — build-spec §5.5.
- Resolved **ingest composition** + a one-line `ingest-manifest` status fix
  (`converged`→`converging` on exit 0) — §5/§8.
- Pinned the **instance vs definition** model (run unit vs status unit) — §2.
- Wrote two new build specs: [`ewe-populate-spec.md`](ewe-populate-spec.md),
  [`host-converge-spec.md`](host-converge-spec.md).
- Recorded the **DNS disposition** (post-V1, plugin-shaped, ewe-converge trigger).

## Build order (dependency chain — bottom-up)

1. **ewe primitives** (the bulk; ewe + mu repos). ewe has *zero* HTTP today. Build
   per the four `ewe-*-spec.md` (arg-resolution → secrets → body-kind →
   http-pagination). These unblock the ewe-populate path.
2. **ewe-populate glue** — `#EweTarget` CUE def + the pudl ingest seam (wrap output
   as `ObserveResult`, reuse `IngestObserveResults`). [`ewe-populate-spec.md`](ewe-populate-spec.md). Thin.
3. **observe-only `pudl run`** — `cmd/run.go` (does NOT exist yet): `populate →
   relations → checks → report`, delegating to `mu build`. Prerequisite host for
   the loop. Replaces `pudl exec`. Build-spec §2.
4. **`#PluginPlan` + apply path** — the CUE def, arm→Target, `desired`→sources
   rendering. §5.5 / §6 V1.6.
5. **The convergence loop** — V1.1 (loop), V1.2 (drift==∅ + cap), V1.4 (failure
   modes + partial-state warning), plus the `ingest-manifest` status fix. §6.
6. **A declarative-apply plugin to prove convergence end-to-end** — **k8s is the V1
   proof** (ships; reads desired from sources, kubectl reconciles). Optionally
   complete **`host.plan`** ([`host-converge-spec.md`](host-converge-spec.md)) for
   example 1.

## Spec map (which doc for which build unit)

| Build unit | Spec |
|------------|------|
| ewe engine / secrets / HTTP / action-body | `ewe-arg-resolution-spec.md`, `ewe-secrets-spec.md`, `ewe-http-pagination-spec.md`, `ewe-body-kind-spec.md` |
| `#EweTarget` + populate ingest | `ewe-populate-spec.md` |
| loop / termination / gating / failure / apply path / instance model | `V1-BUILD-SPEC.md` (§§2–8) |
| host convergence (example 1 plugin) | `host-converge-spec.md` |
| termination "why cap, not monotonic guard" | `../dialectics/v1-2-loop-termination.ndjson` |

## Grounding — shipped pieces the build reuses (verified this session)

- drift check (idempotent, re-enterable): `pudl/internal/drift/checker.go:101-117`;
  reads live via `GetLatestObserve(def)` (`:72`).
- catalog status (blind set, no FSM guard): `pudl/internal/database/catalog_status.go:18-34`.
- `export-actions` → `MuConfig`, marks `converging`: `pudl/cmd/export_actions.go:139,143`.
- Execute+record: `mu build --emit-manifest` (`mu/cmd/mu/build.go:112`) →
  `pudl mu ingest-manifest` (`pudl/internal/mubridge/manifest.go:50`); **already
  writes `converged`/`failed` on exit code at `:182` — narrow this (§5).**
- observe ingest + `_schema` routing: `pudl/internal/mubridge/ingest.go:36`.
- pudl shells `mu build` (orchestration precedent): `pudl/cmd/memory.go:177`.
- mu plugin protocol (apply-capable, Plan op): `mu/sdk/muplugin/plugin.go:33`;
  k8s exemplar `mu/plugins/k8s/plugin.bb:48,125`; host stub plan
  `mu/plugins/host/main.go:71`.

## Do NOT build (cut / deferred)

- **V1.5 rollback** — CUT. Execute is best-effort; failures reported, never undone.
- **Monotonic-drift-shrink guard** — deferred (cap suffices); revisit on first
  oscillating consumer.
- **ewe-converge** (`#EweTarget` mutate) — deferred; DNS is the revisit-trigger.
- **`cloudflare-dns`** — post-V1; regular plugin when wanted (see §10 + ledger).

## Working method (match it — the reviews punished assertion)

- **Ground every claim in real source before asserting.** Run greps/tests against
  actual mu/pudl/ewe code. This session caught the spec's own overclaims that way
  (e.g. `converged`/`failed` already written; the apply-shape mismatch).
- **One issue at a time** — rundown + tradeoffs + your lean, then discuss; record
  as you go (ledger + build-spec), commit at checkpoints, push when asked.
- **Default to deferral/cut** when there's no concrete V1 consumer.
- Caveman mode active (terse); code/commits/specs written normally.
- Commit trailers (see recent commits): `Co-Authored-By: Claude Opus 4.8 (1M
  context)` + `Claude-Session:` line.

## First step for the build session

`cmd/run.go` doesn't exist, and the loop wraps observe-only `pudl run`, which wraps
the ewe-populate path, which needs the ewe HTTP primitives. **So the true first
build is the ewe primitives** (step 1). If you want a faster end-to-end convergence
demo that skips ewe, start from a `#PluginObserve` populate (e.g. k8s/host observe,
which already ship) → observe-only `pudl run` → the loop → k8s converge. That path
touches no unbuilt ewe code and proves the loop against a shipping declarative
plugin.
