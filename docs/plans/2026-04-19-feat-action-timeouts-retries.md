---
title: "feat: Per-action timeouts and network retries"
type: feat
status: proposed
date: 2026-04-19
---

# feat: Per-action timeouts and network retries

## Summary

Today, an `ActionSpec` has `Network: bool` but no way to bound how long an
action runs or whether transient failures should be retried. Actions like
`go mod download`, `curl`, `apt-get update`, or any plugin invoking a remote
service can hang indefinitely when the network misbehaves — the only recovery
is SIGINT to the whole `mu` build. Similarly, a single 500 from a registry
mirror kills the whole DAG even though a second attempt would usually succeed.

This feature adds three fields to `ActionSpec` — `timeout_s`, `retries`,
`retry_backoff_ms` — wired through `dag.Action` to the executor, which wraps
`executeInSandbox` / `executeBare` in `context.WithTimeout` and a retry loop
gated on `Network && retries > 0`. Results gain an attempt-level breakdown so
users can see *what happened* instead of just "action failed".

## Why

- **Determinism for CI.** Builds must have an upper bound on wall time. Today
  a stuck network action can consume the full CI timeout.
- **Resilience for flaky networks.** Package registries, blob stores, and
  apt mirrors fail intermittently. Retrying transient failures is the
  standard practice every other build tool (Bazel, Nix, Make-based CI)
  already supports.
- **Observability.** When an action fails, users need to know *why* —
  timeout? 500? non-zero exit? retries exhausted? The current
  `ActionStatus.Err` is one line of text.

## User Stories

1. **As a plugin author**, I want to declare that `go mod download` is
   network-flaky with a 60s timeout and 3 retries, so that a hiccup on
   proxy.golang.org doesn't fail my user's build.

2. **As a mu user** running a build on a flaky hotel wifi, I want my build
   to retry network actions automatically rather than requiring me to
   re-run `mu build` manually.

3. **As a mu user**, I want a hung `curl` action to give up after 30 seconds
   with a clear "timeout" error rather than hanging my terminal until I hit
   Ctrl-C.

4. **As a CI operator**, I want per-action timeouts so a broken network
   doesn't consume my whole job budget silently.

5. **As a plugin author debugging intermittent failures**, I want the
   `ActionResult` to tell me "attempt 1: exit 1 after 2.1s; attempt 2: exit 0
   after 800ms" so I can distinguish "retry helped" from "retry was
   unnecessary".

6. **As a plugin author**, I want retries to only apply to network actions
   (`Network: true`), so that my `go test` never silently re-runs if a test
   happens to be flaky — retries should be an explicit, scoped tool.

## Acceptance Criteria

### Protocol (internal/plugin/protocol.go)

- [ ] `ActionSpec` gains three JSON-tagged fields:
  - `TimeoutS int` → `timeout_s,omitempty` (0 = no timeout, the current behavior)
  - `Retries int` → `retries,omitempty` (0 = no retry, the current behavior)
  - `RetryBackoffMs int` → `retry_backoff_ms,omitempty` (0 = retry immediately)
- [ ] Semantics documented in doc comments: retries only apply when
  `Network == true`; on a non-network action `Retries` is silently ignored
  (with a single warning logged once per build for misuse).
- [ ] `ProtocolVersion` is bumped to 2. Plugins reporting
  `protocol_version: 1` continue to work — the new fields are optional and
  default to the legacy "no timeout, no retry" behavior.

### DAG wiring (internal/coordinator/resolve.go → internal/dag/graph.go)

- [ ] `dag.Action` gains `TimeoutS`, `Retries`, `RetryBackoffMs` fields
  mirroring `ActionSpec`.
- [ ] `coordinator.Resolve` copies the three fields through, with validation:
  negative values rejected with a clear error at resolve time;
  `RetryBackoffMs > 0 && Retries == 0` warns (dead config).
- [ ] `ComputeActionKey` **does not** include the three new fields. Cache
  keys must be stable regardless of whether a plugin author tweaks the
  timeout — changing a timeout shouldn't invalidate caches.

### Executor (internal/dag/executor.go ~line 147)

- [ ] `executeAction` is refactored so that the `executeInSandbox` /
  `executeBare` branch is wrapped in a helper `runWithTimeoutAndRetry(ctx,
  a, execEnv)` that returns `(exitCode int, outcome ActionOutcome, err error)`.
- [ ] When `a.TimeoutS > 0`, each attempt uses
  `context.WithTimeout(ctx, time.Duration(a.TimeoutS)*time.Second)`; the
  per-attempt context is cancelled when the attempt returns.
- [ ] Retry loop runs at most `a.Retries + 1` attempts total, but **only**
  when `a.Network && a.Retries > 0`.
- [ ] Between attempts, the executor sleeps for `a.RetryBackoffMs`
  (respecting the parent `ctx` — sleep returns immediately if ctx is done).
  Optional v1.1: exponential backoff if `RetryBackoffMs` is negative. Not
  in scope for v1.
- [ ] Retry eligibility (see Open Questions #1 for discussion):
  - **Timeout: always retryable** (classic transient failure)
  - **Non-zero exit code: retryable** (simplest rule; network failures
    surface as many different codes — curl 6/7/28, go 1, apt 100). Plugin
    authors opt in by setting `Retries > 0`, so this is an explicit choice.
  - **Process failed to start / fork error: not retryable** (configuration
    problem, not transient)
  - **Parent ctx cancelled (SIGINT): not retryable, return ctx.Err()
    immediately**
- [ ] On final failure after all retries exhausted, the action is treated
  as failed exactly as today (transitive dependents cancelled).

### Reporting (internal/dag/executor.go ActionStatus, cas.ActionResult)

- [ ] `ActionStatus` gains:
  - `Attempts int` — number of attempts actually executed (1 if no retry)
  - `TotalElapsed time.Duration` — wall time across all attempts including
    backoff sleep
  - `Outcome ActionOutcome` — enum: `OutcomeSuccess`, `OutcomeNonZeroExit`,
    `OutcomeTimeout`, `OutcomeStartError`, `OutcomeCancelled`
  - `AttemptLog []AttemptRecord` — per-attempt breakdown for debugging:
    `{Attempt int; ExitCode int; Outcome ActionOutcome; Elapsed time.Duration}`
- [ ] `mu build -v` surfaces attempt info on retry or timeout:
  `mu: action "//foo:go_mod_download" succeeded on attempt 2/3 after 4.1s total`
- [ ] On failure:
  `mu: action "//foo:curl" failed: timed out after 30s (3 attempts, 93.2s total)`

### Cache interaction

- [ ] Timeouts and non-zero exits with `Retries > 0` follow the existing
  "failed actions are not cached" rule — failed-after-retries still not
  cached in v1.
- [ ] This feature is *explicitly orthogonal* to the "cache failures" and
  "Impure-aware cache key" ideas. Note in the design: if those land,
  the cache layer will look at `ActionResult.Outcome`, not at whether the
  action declared retries. No code coupling here beyond reusing
  `ActionResult.ExitCode`, which already exists.

### Tests (internal/dag)

- [ ] `flaky_simulator_test.go`: a deterministic flaky action — a small Go
  program (or `sh -c` script) compiled into `testdata/` that reads a counter
  file, increments it, and exits non-zero for the first N attempts before
  succeeding. Covers:
  - Retries exhausted → failure is reported, dependents cancelled.
  - Retry-then-succeed → ActionStatus.Outcome is Success,
    Attempts == N+1, AttemptLog has the right shape.
  - `Network: false` with `Retries: 3` → single attempt only; warning
    logged.
- [ ] Timeout test: a `sleep 30` action with `TimeoutS: 1` → fails with
  `OutcomeTimeout` in ~1s, not 30s.
- [ ] Timeout + retry: `Network: true, Retries: 2, TimeoutS: 1` running
  `sleep 30` — total wall time should be ~3s + 2×backoff, not 90s.
- [ ] Parent context cancellation during backoff sleep returns promptly
  with `ctx.Err()` and does not consume remaining retries.
- [ ] Cache key stability: changing `TimeoutS` / `Retries` / `RetryBackoffMs`
  alone does not change `ComputeActionKey`.

### Documentation

- [ ] `docs/plugin-protocol.md` (or equivalent) documents the three fields
  with examples.
- [ ] Plugin authors see at least one worked example in `plugins/host/` or
  `plugins/aws/` showing `timeout_s: 30, retries: 3, retry_backoff_ms: 500`
  on a real network action.

## Technical Context

### Key files

- `internal/plugin/protocol.go:87-98` — `ActionSpec` definition. Add three
  fields here and bump `ProtocolVersion`.
- `internal/coordinator/resolve.go:84-95` — where `ActionSpec` is projected
  into `dag.Action`. Add field copies + validation.
- `internal/dag/graph.go:11-36` — `dag.Action` struct; add three fields
  next to `Network`, `Impure`.
- `internal/dag/executor.go:147` — `executeAction`. Introduce
  `runWithTimeoutAndRetry` helper between the cache-miss branch (line 163)
  and the actual `executeInSandbox` / `executeBare` dispatch (lines 185-189).
- `internal/dag/executor.go:42` — `Execute` already wraps with
  `context.WithCancel`; the retry context derives from this, so SIGINT
  continues to propagate correctly.
- `internal/dag/actionkey.go:25` — `ComputeActionKey`. Must *not* include
  the new fields (document this explicitly).
- `internal/cas` — `ActionResult` already carries `ExitCode`; we add
  outcome/attempts metadata here only if we decide to serialise
  retry diagnostics into the cache (out of scope for v1; keep retry
  metadata in `ActionStatus` only).
- `internal/dag/dag_test.go:219-326` — existing ComputeActionKey tests show
  the canonical testing pattern for this package.

### Patterns to follow

- **Nil/zero defaults mean legacy behavior.** This is how `Impure`,
  `Network`, `SealedInputs` were added. Preserves backward compatibility
  with v1 plugins.
- **Validation at resolve time, not at execute time.** Matches the existing
  pattern where `Resolve` rejects path escapes and malformed work dirs
  before actions reach the executor.
- **Secrets-style separation.** Just as resolved secrets never touch
  `ComputeActionKey`, timeouts/retries must never touch the cache key.
  Both are "execution policy", not "action identity".

### Non-goals (explicit)

- Not adding an exponential-backoff policy in v1 — a constant
  `retry_backoff_ms` is simpler and covers the 80% case. Can be extended
  later (e.g. `retry_policy: "exponential"`).
- Not adding a global `--default-timeout` CLI flag. Plugins should declare
  timeouts per action; a global override can come later.
- Not caching failed-after-retries results in v1 — that's a separate idea
  with its own tradeoffs.
- Not changing SIGINT semantics; the parent context still cancels the
  whole build.

## Open Questions

1. **Retry eligibility: all non-zero exits, or an allowlist?** The
   recommendation is *all non-zero exits when Network && Retries > 0*,
   because (a) network tools report failure via wildly different exit
   codes (curl has ~30, go-mod has a few, apt has 100, git has several),
   and (b) the plugin author has already opted in by setting `Retries > 0`
   on a `Network: true` action. An allowlist (e.g. "retry only on 1, 6, 7,
   28") adds complexity with little benefit. **Decision needed from
   reviewers.**

2. **Does `TimeoutS` apply to non-network actions too?** Current design:
   yes — timeout is orthogonal to retry. A `go test` with `TimeoutS: 600`
   is useful even without retry. Retries are the network-gated feature,
   timeouts are universal.

3. **Should we emit structured events (JSON log line) for each retry
   attempt?** Useful for CI log analysis but adds a new surface. Proposal:
   defer to `mu`'s existing logging, just make the text output
   parseable.

4. **Should cache writes happen if the action succeeded *on a retry*?**
   Proposal: **yes** — the output is byte-identical to a no-retry success;
   the retry is an execution-time detail. The cache has no reason to care.
   Matches how Bazel handles `--remote_retries`.

5. **Interaction with sandbox teardown on timeout.** When
   `executeInSandbox` is killed by timeout, `sb.Cleanup()` (line 247)
   must still run. Needs a test to confirm deferred cleanup fires on
   context cancellation (it does today, via `defer`, but worth verifying
   with a timeout-kill test).

6. **`RetryBackoffMs` units.** Proposal: ms (consistent with most config
   tooling). Alternative: accept a Go duration string (`"500ms"`,
   `"2s"`) — richer, but requires parsing in plugins that don't have a
   duration library. Stick with ms for v1.

7. **Should `ActionResult.AttemptLog` persist to disk?** Not in v1. It
   lives in `ActionStatus` only, for logging. If users later want a
   durable audit log, it can be added to `cas.ActionResult` without a
   protocol change.

## Rollout

- v1: protocol bump to 2; new fields default to legacy behavior; all
  existing plugins continue to work unchanged.
- Migrate `plugins/host` or `plugins/aws` network actions in a follow-up
  PR to start using the new fields (reasonable defaults: `timeout_s: 60,
  retries: 3, retry_backoff_ms: 500` for package fetches).
