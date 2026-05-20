# 30 Improvement Ideas for the mu Build Coordinator

**Date:** 2026-04-19
**Status:** Brainstorm / scout output
**Scope:** Improvements to the mu coordinator, executor, plugin manager, CAS,
and CLI — not new plugins. Grounded in the current tree at `cmd/`, `internal/`,
`plugins/*/plugin.bb`, and `docs/`.

---

## Context

mu today (~14k LOC of Go, per `wc -l`) is a working but minimal coordinator:

- `internal/coordinator/coordinator.go` runs Plan → Execute with a single
  pass; plugins are spawned per invocation and torn down after planning.
- `internal/dag/executor.go` implements a channel-based parallel worker pool
  with per-action CAS lookup and restore.
- `internal/dag/actionkey.go` hashes command + inputs + env + network + workdir
  into a SHA-256 cache key.
- `internal/plugin/manager.go` + `process.go` speak NDJSON over stdin/stdout;
  capabilities are `discover|plan|observe|resolve_secret` with fixed timeouts
  (10s/5m/5m/30s).
- `internal/config/validate.go` is about 120 lines of ad-hoc checks.
- `internal/sandbox/sandbox.go` is still the "copy sandbox": a tempdir with
  `bin/work/out/tmp`, no namespace/overlay isolation.
- `cmd/mu/{build,cache,plugin,observe,target,verify,scratch}.go` each parse
  flags independently — no shared CLI framework.
- README explicitly lists open roadmap items: tiered cache, `mu clean`, color
  output, GOCACHEPROG bridge, OS-level sandboxing, remote execution, streaming
  progress, async planning.

The ideas below target friction, correctness gaps, and leverage points I saw
while reading the code and bundled plugins.

---

## All 30 Ideas

### Caching, CAS, and cache keys

1. **`mu clean` / GC command.** Already flagged in README roadmap. Walk
   `~/.mu/cache` OCI layout and `~/.mu/plugins`, drop blobs not referenced by
   any tagged action result, with `--dry-run` and `--older-than`. Hooks into
   existing `cmd/mu/cache.go` structure.

2. **Tiered cache composition.** README roadmap. `internal/cas/oci` already has
   local; wrap it with a `cas.Tiered{Layers: []cas.Store}` that does
   read-through + write-through + optional read-repair. `CacheConfig` in
   `internal/config/types.go` already has the schema.

3. **Action cache negative results.** Today a failed action produces no
   `ActionResult`, so re-running always re-executes. Optionally store
   exit-code-only results (no outputs) so deterministic failures short-circuit
   with `--cache-failures`. `internal/dag/executor.go:executeAction`.

4. **Hash `Impure` into the cache key explicitly.** `ComputeActionKey`
   (`internal/dag/actionkey.go`) omits `Impure`. Two actions identical except
   for the impure flag would collide — today the executor short-circuits
   caching for impure, but if an action flips `Impure` false, it may reuse a
   tainted result. Add `fmt.Fprintf(h, "impure:%t\n", a.Impure)`.

5. **Include sorted `DependsOn` structural edges in the key.** Currently the
   key captures input digests but not graph shape; two actions with the same
   inputs but different topology can still collide in pathological plugins.

6. **Content-address source globs at config load.** `internal/config/loader.go`
   globs on load; remembering resolved paths + mtimes lets the coordinator
   fast-skip planning entirely when nothing changed (a "plan cache").

7. **Cache `discover` responses by plugin CAS digest.** Every `mu build`
   spawns every plugin and pays discover latency. Since plugins are
   content-addressed (`mu plugin list --cached`), cache the discover JSON
   keyed by plugin digest in `~/.mu/discover-cache.json`.

### Executor and DAG

8. **Structured progress stream / live TUI.** `internal/dag/executor.go`
   writes plain text to stderr. Add a `Progress chan ActionEvent` and a
   default renderer (spinner + per-action state) for `mu build`, plus
   `--progress=json` for CI.

9. **Cancelled action reporting is lossy.** `executor.go` appends to
   `result.Cancelled` only when `inDegree[depID] > 0` — transitively
   cancelled actions already queued may be missed. Track them uniformly and
   surface the full cancelled set.

10. **Timeouts per action.** Plugin timeouts exist; action timeouts do not.
    Add `timeout_s` to `plugin.ActionSpec` and wire through to
    `exec.CommandContext` in `internal/dag/executor.go`.

11. **Retry policy on transient actions.** `ActionSpec.Network=true` actions
    (e.g. `go mod download` in `plugins/go/plugin.bb`) currently fail hard on
    DNS flakes. Add `retries`/`retry_backoff` fields.

12. **Per-action resource classes.** `Workers` is a single global count in
    `coordinator.Coordinator`. Plugins could tag actions with a class
    ("cpu", "io", "network"); executor maintains separate semaphores so a
    thundering herd of `curl` actions doesn't starve compile actions.

13. **Dependency-aware scheduling (critical-path first).** `TopoSort` +
    channel-based ready queue is FIFO. Compute longest-path weights and pop
    highest-weight ready actions first for better wall-clock on deep graphs.

14. **Deterministic action IDs from plugin output.** `prefixActions` in
    `coordinator.go` prepends `target:` but if a plugin emits duplicate IDs
    within a target, the DAG rejects with `duplicate action ID`. Auto-suffix
    or hash instead of erroring.

### Plugin protocol / plugin manager

15. **Long-lived plugin daemon / process reuse.** `Plan()` spawns and kills
    plugins every invocation (`defer mgr.Close()` in coordinator.go:105). A
    `mu serve` / plugin warm-pool would eliminate JVM-like bb startup cost on
    each build.

16. **Streaming progress from plugin actions.** Protocol roadmap item.
    Add a `status` method on the NDJSON wire: plugins can push
    `{"method":"status","action":"...","percent":42}` lines that the
    coordinator forwards to the progress channel.

17. **Plugin protocol version negotiation.** `ProtocolVersion = 1` is a
    constant (`internal/plugin/protocol.go`). Coordinator never checks it
    against `DiscoverResponse.ProtocolVersion`. Reject mismatches with a
    clear error in `internal/plugin/manager.go`.

18. **Config-schema–driven validation with proper JSON Schema.**
    `internal/coordinator/validate.go:ValidateTargetConfig` is a hand-rolled
    matcher over `map[string]any`. Swap in a real JSON Schema library so
    plugin-declared `config_schema` supports `required`, `enum`, `pattern`,
    nested objects, defaults.

19. **Capability gating at registration.** Today the manager checks
    capabilities per-call (e.g. `HasCapability("observe")`). Fail fast at
    startup if a plugin registered under a toolchain name is missing `plan`,
    or if a `resolve_secret` scheme plugin lacks the capability.

20. **Plugin stderr capture → structured logs.** `internal/plugin/process.go`
    inherits stderr. Capture it, tag each line with the plugin name, and
    surface only on `--verbose` or on error — cleaner build output.

### CLI & UX

21. **Shared `cmd/mu` CLI framework.** Each subcommand re-implements flag
    parsing, project-root discovery, store setup, and error printing. Extract
    a `cliContext` helper so `build.go`, `observe.go`, `scratch.go`,
    `plugin.go` share consistent `--config`, `--json`, `--verbose` handling.

22. **Colored + level-filtered output.** README roadmap. Introduce a tiny
    logger in `cmd/mu` (isatty check + `--no-color`).

23. **`mu build --watch`.** File-watcher on declared sources → re-plan and
    re-execute affected subgraphs. Uses the existing plan cache hint from
    idea 6.

24. **`mu why <target>` / `mu explain <action>`.** Given a target, print
    the action subgraph, inputs, cache-key inputs, and why it missed the
    cache. Most of this exists in `cache.go`'s inspect helpers — generalize
    them.

25. **`mu plugin test` harness.** Read the plugin `mu.json`, invoke it with
    canned NDJSON requests from `internal/plugin/testdata`, and validate
    responses. Huge leverage for plugin authors; easy given the protocol is
    line-oriented.

26. **`mu fmt` for `mu.json` / config.** Canonicalize key order, indent,
    and expand globs to stable literal source lists. Eliminates diff churn.

### Observability & manifests

27. **Build manifest into CAS.** `Manifest` in `coordinator/manifest.go` is
    only emitted to stdout with `--emit-manifest`. Store it in CAS tagged
    `manifest-<timestamp>` so `mu cache ls --manifests` and `pudl` ingestion
    can replay history.

28. **Remote cache push/pull explicit command.** The roadmap references
    OCI remote cache, but there is no `mu cache push <registry>` /
    `mu cache pull`. Wire `internal/cas/oci` remote layer behind explicit
    subcommands.

### Sandbox & hermeticity

29. **OS-level sandbox.** README roadmap. Replace/augment
    `internal/sandbox/sandbox.go`'s copy sandbox with Linux user
    namespaces + overlayfs and macOS `sandbox-exec` profiles. Biggest
    correctness win; largest effort.

30. **Hardlink-first source copy with fallback already in place — extend to
    outputs.** `CopySource` in `sandbox.go:120` already hardlinks; but
    `executeInSandbox` (`executor.go:270`) copies outputs via
    `io.Copy` through `os.Open`. Try `os.Link` first to save disk bandwidth
    on large binaries (Go executables on Linux are tens of MB).

---

## Top 10 — Ranked by Simplicity × Leverage

Selection criterion: high leverage per unit of effort *for mu specifically*
(i.e. addressing something mu's tight, plugin-protocol-centric architecture
makes cheap to do well). I deprioritized the biggest items (OS sandbox,
remote execution) — they are high-leverage but L-effort and already tracked
in the roadmap.

| # | Idea | Effort | Leverage | Why it matters for mu |
|---|------|--------|----------|-----------------------|
| 1 | Include `Impure` in action cache key | S | 3 | Silent cache-poisoning bug; one-line fix in `actionkey.go` |
| 2 | Plugin stderr → structured, tagged logs | S | 4 | Plugins are the UX surface; today errors are interleaved mush |
| 3 | Protocol version negotiation | S | 3 | Cheap insurance as plugin ecosystem grows (roadmap §Protocol ext) |
| 4 | `mu clean` / GC | S | 4 | Tracked roadmap item; `cache.go` already walks index |
| 5 | `mu plugin test` harness | S | 5 | Plugin authors are the leverage point; protocol is line-oriented JSON |
| 6 | Cache `discover` responses by plugin digest | S | 4 | bb plugins pay ~1s JVM startup per invocation × N plugins |
| 7 | Shared CLI context helper in `cmd/mu` | S | 4 | Every new subcommand re-invents boilerplate |
| 8 | JSON-Schema validation of target configs | M | 5 | Plugins declare `config_schema` that's mostly ignored; real errors cost hours |
| 9 | Action timeouts (plus retries for network) | M | 4 | Prevents hung builds; roadmap mentions protocol extensions |
| 10 | Tiered cache composition (disk + OCI) | M | 5 | Schema already exists in `CacheConfig`; unlocks team-wide cache hits |

### Detail for each of the top 10

1. **`Impure` in action cache key.**
   *What:* Include `a.Impure` in the SHA-256 input of `ComputeActionKey`.
   *Why (mu):* An action that flips from `impure:true` to `impure:false`
   across plugin versions can hit a stale cached result from the impure run
   if the other key components match. Low-probability but silent.
   *Effort:* S. *Leverage:* 3.
   *Sketch:* Add `fmt.Fprintf(h, "impure:%t\n", a.Impure)` in
   `internal/dag/actionkey.go:ComputeActionKey` and update
   `actionkey_test` / `executor_test.go`.

2. **Structured, tagged plugin stderr.**
   *What:* Capture each plugin process's stderr, prefix with
   `[plugin-name] `, suppress by default, show on `--verbose` or on error.
   *Why (mu):* With N bb plugins running concurrently, raw interleaved
   stderr is the most common "WTF" for plugin authors.
   *Effort:* S. *Leverage:* 4.
   *Sketch:* In `internal/plugin/process.go:StartProcess`, wire
   `cmd.Stderr` to a `bufio.Scanner` goroutine that writes through the
   manager's logger; expose `mgr.Logs(name)`.

3. **Protocol version negotiation.**
   *What:* Compare `DiscoverResponse.ProtocolVersion` against
   `plugin.ProtocolVersion` at manager start; reject incompatible plugins
   with a clear error.
   *Why (mu):* The protocol is explicitly versioned (`protocol.go:6`) but
   never enforced. As soon as v2 ships, old plugins fail mysteriously.
   *Effort:* S. *Leverage:* 3.
   *Sketch:* In `internal/plugin/manager.go:Start`, after `proc.Discover`,
   compare `resp.ProtocolVersion` with `ProtocolVersion` and return
   `fmt.Errorf("plugin %q: protocol v%d incompatible with v%d", ...)`.

4. **`mu clean` / GC.**
   *What:* New `cmd/mu/clean.go` command. Walk `index.json`, collect
   referenced blob digests, delete unreferenced blobs; plus `--plugins` to
   prune `~/.mu/plugins/*` not present in any current config.
   *Why (mu):* README has it listed; without it the local cache grows
   unbounded (toolchain extractions alone are ~200 MB each).
   *Effort:* S. *Leverage:* 4.
   *Sketch:* Follow the `readIndex` / `blobPath` helpers in
   `cmd/mu/cache.go` to enumerate reachable blobs; `os.Remove` the rest;
   add `main.go` dispatch entry.

5. **`mu plugin test` harness.**
   *What:* A subcommand that spawns a plugin, sends canned discover/plan
   JSON from a `testdata/` dir or inline, validates response shape, and
   optionally compares to a golden file.
   *Why (mu):* mu's entire value prop is the plugin protocol; plugin
   authors currently debug by running `bb plugin.bb < req.json`.
   *Effort:* S. *Leverage:* 5.
   *Sketch:* New `cmd/mu/plugin.go:runPluginTest`, reuse
   `internal/plugin/process.go` and `internal/plugin/testdata/*` golden
   files; `mu plugin test plugins/go --golden testdata/go-plan.json`.

6. **Discover response cache keyed on plugin CAS digest.**
   *What:* Persist `{plugin-digest → DiscoverResponse}` in
   `~/.mu/discover-cache.json`; on warm start, skip the `discover` round trip
   and just start the process when needed.
   *Why (mu):* Every build pays bb startup × plugin count. The go example
   already shows ~1.5s just for plugin spawn. Plugins are content-addressed
   already (`plugins/*/mu.json` digests), so this is safe.
   *Effort:* S. *Leverage:* 4.
   *Sketch:* In `internal/coordinator/pluginresolver.go` after resolving
   each plugin digest, check the cache; fallback to the current
   `mgr.Start` path when missing. Invalidate on digest change.

7. **Shared CLI context helper.**
   *What:* Extract `cliContext { ProjectRoot, Config, Store, ... }` and a
   `newCLIContext(flags)` helper. All subcommands call it.
   *Why (mu):* `cmd/mu/build.go`, `observe.go`, `scratch.go`, `plugin.go`,
   `verify.go`, `cache.go` each duplicate the same ~40 lines of
   project-root discovery + store creation + error formatting, and each
   diverges slightly (e.g. `--config` flag wording).
   *Effort:* S. *Leverage:* 4.
   *Sketch:* New `cmd/mu/context.go` with `resolveProjectRoot` (already
   defined in `plugin.go:58`) + `openStore`; refactor each command's
   first 30 lines to `ctx, err := newCLIContext(fs)`.

8. **Real JSON-Schema validation for target configs.**
   *What:* Replace `internal/coordinator/validate.go:ValidateTargetConfig`
   with a proper JSON Schema validator so plugins' `config_schema` (e.g.
   `plugins/go/plugin.bb:31-41`) supports `required`, `enum`, `default`,
   `pattern`, nested shapes.
   *Why (mu):* The protocol already advertises `config_schema` as a
   first-class field. Today an invalid `goos` like `"linx"` fails deep
   inside the Go build. Good errors here are the single biggest
   plugin-UX improvement.
   *Effort:* M. *Leverage:* 5.
   *Sketch:* Add `github.com/santhosh-tekuri/jsonschema/v5` to `go.mod`;
   compile each plugin's schema in `coordinator.Plan` after discover;
   validate each `Target.Config` before calling `mgr.Plan`.

9. **Per-action timeouts with retries for network actions.**
   *What:* Two new fields on `plugin.ActionSpec`: `timeout_s int` and
   `retries int`. Executor wraps `ctx, cancel := context.WithTimeout(...)`
   and retries transient failures when `a.Network && retries > 0`.
   *Why (mu):* Today `go mod download` / `curl` actions can hang
   indefinitely; SIGINT is the only escape. Plugins have no way to express
   "this is network-flaky, try again."
   *Effort:* M. *Leverage:* 4.
   *Sketch:* Extend `ActionSpec` in `internal/plugin/protocol.go`; copy to
   `dag.Action` in `internal/coordinator/resolve.go`; wrap
   `e.executeInSandbox`/`executeBare` in `internal/dag/executor.go:147`
   with a retry loop.

10. **Tiered cache composition (disk → OCI remote).**
    *What:* Implement `cas.Tiered` with configurable read-through,
    write-through, and read-repair, driven by the already-present
    `CacheConfig.Backends` in `internal/config/types.go`.
    *Why (mu):* It's the highest-leverage item still tracked as "roadmap"
    that is realistically M-effort: the schema exists, `internal/cas/oci`
    already supports both local and remote, and the executor only cares
    about the `cas.Store` interface.
    *Effort:* M. *Leverage:* 5.
    *Sketch:* New `internal/cas/tiered.go`: `type Tiered struct { Layers
    []cas.Store; ReadRepair, WriteThrough bool }`. `Get` walks layers, on
    miss from layer N falls through to N+1 and back-fills. `Put` fans out
    to all writable layers. Wire into `cmd/mu/build.go` around line 88
    replacing the single `oci.NewLocal(...)` with a composed store built
    from `cfg.Cache`.

---

## Notes / observations while scouting

- `internal/dag/actionkey.go` currently excludes `Impure` and the dependency
  topology from the key. Both are latent correctness issues worth
  documenting regardless of whether we act on them.
- Plugin toolchain inference (`needsScriptRuntime` in `coordinator.go:698`)
  treats "digest-only with no extension hint" as `bb` for backward compat —
  brittle as soon as a non-bb plugin is stored by digest.
- `resolveTargets` implements a second Kahn sort (after `dag/topo.go`). Two
  topological sorts in the same build is a small code-smell worth a cleanup
  pass once more features land.
- `observe_command` in shell targets (`coordinator.go:404`) runs without any
  sandbox, env scrubbing, or timeout — a consistency gap compared to
  plugin-driven observe.
- `mu.json` has `"plugins"` as an array declared in the consuming project,
  while `mu plugin list --cached` scans `~/.mu/plugins`. There is no command
  to list *what the current project needs* vs *what is already cached*
  vs *what is stale* — a `mu plugin status` would compose the existing
  primitives.
