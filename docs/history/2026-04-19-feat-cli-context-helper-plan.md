# Epic: Shared `cliContext` Helper for `cmd/mu`

**Status:** Proposed
**Date:** 2026-04-19
**Owner:** TBD
**Scope:** `cmd/mu/*.go` only — no behavior change to internal packages

---

## Summary

Every subcommand under `cmd/mu/` (`build`, `observe`, `scratch`, `plugin add`,
`plugin list`, `target list`, `verify`, `cache *`) re-implements the same
preamble: register `--config` / `--json` / `--verbose` flags, find the project
root, load and validate `mu.json`, build a CAS store at `~/.mu/cache`,
construct a `Coordinator` (sometimes with a `scratch.Builder`), and prefix
every error with `mu <subcmd>: ...`. Each instance is ~30–50 lines and they
have already drifted: error prefixes are inconsistent (`mu build:` vs
`mu plugin add:`), `--verbose` is registered but ignored everywhere, `--json`
is wired ad hoc, exit codes are conventional rather than enforced, and only
`plugin.go` has a `resolveProjectRoot` helper while `build.go`, `observe.go`,
`scratch.go`, and `target.go` each open-code the same logic.

This epic introduces `cmd/mu/context.go`, providing:

1. A `cliContext` struct holding `ProjectRoot`, `Config`, `Store`, `Logger`,
   and resolved shared flags.
2. A `newCLIContext(name string, fs *flag.FlagSet) *cliContext` constructor
   that registers the canonical shared flags on `fs` and returns a value
   ready to be `Resolve()`-d after `fs.Parse()`.
3. Consistent error printing (`(*cliContext).fail(format, args...) int`) and
   a small set of named exit codes.
4. A migration plan that ports one subcommand per PR with no behavior
   changes a user can detect (other than uniform error wording).

The work is purely structural. No new commands. No flag removals. Output
formats are unchanged. The user-visible delta is: error messages have a
consistent prefix and shape; `--verbose` actually does something everywhere
it appears; `--no-color` becomes available on every command.

---

## Why now

The `feat-aws-observation-plugin` and `feat-brick-ecosystem-integration`
epics will each add new subcommands or sub-subcommands (e.g. `mu observe
push`, plugin lifecycle commands). Adding them on top of the current
copy-paste preamble locks in the drift. Folding the preamble first makes
those epics smaller and forces them to inherit consistent flag handling for
free.

Cost estimate: ~6 small PRs over ~1–2 days of focused work. Net line delta:
roughly −250 LOC across `cmd/mu/`.

---

## User stories

- **As a `mu` user**, I want every subcommand to print errors in the same
  shape (`mu <subcmd>: <message>`) so that I can grep my CI logs and tell
  my colleagues "any line starting with `mu ` and a colon is an error from
  the build tool."

- **As a `mu` user**, I want `--json`, `--verbose`, `--no-color`, and
  `--config` to behave identically across `build`, `observe`, `scratch`,
  `plugin`, `target`, `verify`, and `cache`, so that I don't have to
  remember which command supports which flag, or which one accepts
  `--config=foo` vs `--config foo`.

- **As a `mu` user running in CI**, I want a single, documented set of exit
  codes (0 = success, 1 = operation failed, 2 = bad invocation) so that my
  pipeline scripts can branch reliably.

- **As a `mu` contributor adding a new subcommand**, I want a 5-line
  preamble (`ctx, code := newCLIContext("foo", fs).Resolve(); if code != 0
  { return code }`) instead of a 40-line copy-paste, so that the next
  `mu` subcommand is a half-day's work, not a day's.

- **As a `mu` contributor reviewing a PR**, I want subcommand files to be
  about *what the command does*, not *how it bootstraps*, so that diffs are
  easy to read and behavior changes are obvious.

---

## Acceptance criteria

1. **`cmd/mu/context.go` exists** and exports (package-private) the
   `cliContext` struct described in "Technical context" below.
2. **`newCLIContext(name string, fs *flag.FlagSet) *cliContext`** registers
   the canonical shared flags on `fs`. Names: `--config` (string),
   `--json` (bool), `--verbose` (bool), `--no-color` (bool). It does not
   call `fs.Parse`.
3. **`(*cliContext).Resolve(opts resolveOpts) (int, bool)`** returns
   `(exitCode, ok)`. `opts` selects which heavy resources to materialize
   (project root, config, store, coordinator) so `cache` and `verify`
   commands don't pay for config loading they don't need.
4. **`resolveProjectRoot` is removed from `plugin.go`** and folded into the
   new context. No production code in `cmd/mu/` calls
   `config.FindProjectRoot` directly any more.
5. **All seven subcommand entry points (`runBuild`, `runScratch`,
   `runObserve`, `runPluginAdd`, `runPluginList`, `runTargetList`,
   `runVerify`, `runCacheLs`, `runCacheInspect`, `runCacheSize`)** use
   `newCLIContext` for shared flag registration and use the same
   `ctx.fail(...)` for error reporting. The error prefix is exactly
   `mu <subcmd>: ` everywhere.
6. **Exit codes are constants** (`exitOK = 0`, `exitFail = 1`, `exitUsage
   = 2`) used consistently. No bare `return 1` / `return 2` in subcommand
   files for the cases the helper covers.
7. **`--verbose` actually plumbs through** to the `plugin.Manager` /
   `Coordinator` (via a `Logger` field, even if the initial implementation
   is a thin wrapper around `log.Logger`). `_ = fs.Bool("verbose", ...)`
   sentinels are gone.
8. **`--no-color` is registered globally** and respected by any helper that
   currently does ANSI output (none today; the flag exists for forward
   compatibility and is propagated via `cliContext`).
9. **Tests** — `cmd/mu/context_test.go` exercises:
   - flag registration (every shared flag is parseable on a fresh
     `FlagSet`),
   - project-root resolution from `--config` (absolute and relative paths),
     from cwd discovery, and the failure path when no `mu.json` is found,
   - error formatting (subcommand name appears, single trailing newline,
     correct exit code returned),
   - `Resolve` is idempotent and safe to call without `Parse` for
     usage-error paths,
   - `--no-cache` interaction (when the helper is asked for a store but
     the caller wants no cache, it returns `nil` cleanly).
10. **`go build ./cmd/mu/...` and `go test ./cmd/mu/...` pass** at every
    intermediate PR. Each PR migrates exactly one subcommand file.
11. **`build_test.go` and `verify_test.go`** continue to pass without
    modification, OR are updated in the same PR that touches the file
    they cover.
12. **No public package API changes.** All work is package `main`-internal
    to `cmd/mu`.

---

## Technical context

### Files surveyed

| File             | Lines | Has preamble? | Notes |
|------------------|-------|---------------|-------|
| `main.go`        | 52    | no            | Pure dispatch; untouched. |
| `build.go`       | 284   | yes (~50)     | Plan/JSON modes, `--emit-manifest`. Has `--no-cache`. |
| `scratch.go`     | 105   | yes (~40)     | Honors `MU_SCRATCH` env. Has `--no-cache`. No `--config`. |
| `observe.go`     | 164   | yes (~50)     | Has `--ndjson`. |
| `plugin.go`      | 749   | yes (~40 ×2)  | `runPluginAdd`, `runPluginList`. Owns `resolveProjectRoot` (line 583) and `buildTargets` (line 600) — both candidates to fold into `cliContext`. |
| `target.go`      | 89    | yes (~30)     | `runTargetList`. |
| `verify.go`      | 167   | partial       | Uses `cachePath()`, no project root needed. |
| `cache.go`       | 557   | partial       | Three subcommands, all use `cachePath()`. No config load. |
| `guide.go`       | n/a   | no            | Static help text. Untouched. |

The four "heavy" preambles (`build`, `scratch`, `observe`, `plugin add`,
`plugin list`, `target list`) are textually near-identical. The two "light"
ones (`verify`, `cache *`) only need `cachePath()` and `--json`. The
helper must accommodate both.

### Proposed `cliContext` shape

```go
// cmd/mu/context.go
package main

import (
    "flag"
    "io"
    "log"

    "github.com/chau/mu/internal/cas"
    "github.com/chau/mu/internal/config"
    "github.com/chau/mu/internal/coordinator"
)

const (
    exitOK    = 0
    exitFail  = 1
    exitUsage = 2
)

// resolveOpts selects which resources Resolve() materializes.
// Light commands (cache, verify) pass {NeedCache: true} and skip the rest.
type resolveOpts struct {
    NeedProjectRoot bool
    NeedConfig      bool   // implies NeedProjectRoot
    NeedStore       bool   // CAS at ~/.mu/cache
    NeedCoordinator bool   // implies NeedConfig + NeedStore
    NoCache         bool   // when NeedStore: leave Store nil
}

type cliContext struct {
    Name string // "build", "plugin add", ...

    // Shared flags (pointers, populated by newCLIContext, read after Parse).
    flagConfig  *string
    flagJSON    *bool
    flagVerbose *bool
    flagNoColor *bool

    // Resolved values (populated by Resolve()).
    ProjectRoot string
    Config      *config.ProjectConfig
    Store       cas.Store
    Coord       *coordinator.Coordinator
    Logger      *log.Logger

    // Read-only flag accessors.
    JSON    bool
    Verbose bool
    NoColor bool

    stderr io.Writer // injectable for tests
}

func newCLIContext(name string, fs *flag.FlagSet) *cliContext { /* ... */ }

func (c *cliContext) Resolve(opts resolveOpts) (int, bool) { /* ... */ }

// fail prints "mu <name>: <message>\n" to stderr and returns code.
func (c *cliContext) fail(code int, format string, args ...any) int { /* ... */ }
```

### Patterns to preserve

- **`signal.NotifyContext(context.Background(), os.Interrupt)`** stays in
  the subcommand. The context is operation-scoped, not invocation-scoped;
  hoisting it would force every helper consumer to defer `stop()`.
- **`scratch.go`'s `MU_SCRATCH` branch** stays in `scratch.go`. The helper
  produces the *raw materials* (root, cfg, store), not the build behavior.
- **`build.go`'s `--no-cache` and `--plan` flags** stay subcommand-local;
  they aren't shared.
- **`buildTargets` in `plugin.go`** is a candidate to move to
  `context.go` as `(*cliContext).Build(ctx, targets) (*coordinator.BuildResult, error)`
  but this can be deferred to a follow-up PR — it's not strictly preamble.

### Migration plan (one PR each)

1. **PR 1 — Land the helper.** Add `cmd/mu/context.go` and
   `cmd/mu/context_test.go`. No subcommand files change. CI green.
2. **PR 2 — Migrate `target list`.** Smallest preamble; safest first port.
3. **PR 3 — Migrate `verify` + `cache *`.** Light callers (no config).
   Validates the `resolveOpts` API for cache-only consumers.
4. **PR 4 — Migrate `observe`.** Has `--ndjson`, exercises JSON pathways.
5. **PR 5 — Migrate `plugin add` and `plugin list`.** Removes
   `resolveProjectRoot` from `plugin.go`. Largest delta.
6. **PR 6 — Migrate `build` and `scratch`.** These are the most-used
   commands; do them last so any subtle behavior regression is caught
   against the others as a baseline.
7. **PR 7 (optional) — Plumb `--verbose` through.** Wire `cliContext.Logger`
   into `coordinator.Coordinator` and `plugin.Manager` so the flag actually
   means something.

Each PR: ≤ 200 LOC diff, runs `go test ./...`, has a one-line "no
user-visible change except error wording" callout in the description.

### Error-format contract

Before:
```
mu build: opening config: open /tmp/x: no such file or directory
mu plugin add: <error>
mu observe: resolving home directory: <error>
```
(Inconsistent: sometimes verb-prefixed, sometimes not.)

After:
```
mu <subcmd>: <error>
```

Single line, single trailing newline, no double-prefix. The helper is the
*only* place that prints this pattern; subcommands call `ctx.fail(exitFail,
"observing %s: %v", target, err)`.

---

## Open questions

1. **Where does `~/.mu/cache` come from?** Currently each subcommand
   computes `filepath.Join(home, ".mu", "cache")` independently. Should the
   helper expose `(*cliContext).CachePath() string`, or should that move to
   a `paths` package shared with `cache.go`'s `cachePath()`? Recommendation:
   start with a method on `cliContext`, extract to a package only if
   non-`cmd/mu` callers appear.

2. **Should `Resolve` handle `--help` / usage printing?** `flag.ContinueOnError`
   already returns `flag.ErrHelp` from `Parse`; `Resolve` runs *after*
   `Parse`. The current code returns `2` on parse error. Recommendation:
   leave `--help` handling to the subcommand (it knows its own argument
   shape) but standardize the exit code to `exitUsage` via a constant.

3. **`--verbose` semantics.** Today it's a no-op. Plumbing it through
   `coordinator.Coordinator` requires changes in `internal/coordinator/`.
   Should PR 7 be in-scope for this epic, or a separate epic? Recommendation:
   ship the flag plumbing in PR 7, but stub `Logger` to a no-op writer
   until coordinator support lands. That way the *flag* is consistent
   even if its *effect* is incremental.

4. **`scratch` has no `--config`.** Current behavior: it always uses cwd
   discovery. Should we add `--config` for consistency? Recommendation:
   yes — it's free with the helper and matches user intuition. Call this
   out in PR 6's description as the one user-visible behavior delta.

5. **`emit-manifest` and `--plan` on `build`** are mutually exclusive; this
   check stays in `build.go`. Confirm reviewers are OK with the helper not
   modeling subcommand-specific flag interactions.

6. **`buildTargets` (`plugin.go:600`)** — fold into `cliContext` now or
   later? Recommendation: later. It's not preamble; it's a convenience
   wrapper that only `plugin.go` uses. Folding it requires deciding on a
   `(*cliContext).Build` API surface that `build.go` would *also* need to
   adopt to avoid divergence — bigger scope than this epic. Track as a
   follow-up.

7. **Test isolation.** `Resolve()` reads `os.Getwd()` and `os.UserHomeDir()`.
   Tests will need to either `t.Chdir` (Go 1.24+) into a tempdir with a
   fixture `mu.json`, or accept dependency injection. Recommendation:
   `t.Chdir` + a `homeDir` field on `cliContext` overrideable in tests.

---

## Out of scope

- Refactoring `internal/coordinator/`, `internal/config/`, or
  `internal/cas/`.
- Adding new subcommands.
- Changing JSON output schemas.
- Removing or renaming any existing flag.
- Wiring `--verbose` deeper than the `Logger` field (deferred to PR 7 or a
  follow-up epic).
- Color output (`--no-color` is registered for forward-compatibility only).
