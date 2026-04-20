# Epic: Discover-Response Cache Keyed on Plugin CAS Digest

Status: Design
Date: 2026-04-19
Author: scout agent (feature-design)
Related brainstorm: `docs/brainstorms/2026-04-19-mu-coordinator-improvements.md` (item 7, item 6 in "Plugin ecosystem")
Related design: Protocol version negotiation (design #3, pending)

---

## Summary

Every `mu build` today spawns every plugin process, pays the `bb` (or other
runtime) startup cost, and issues a `discover` NDJSON round-trip. The
`DiscoverResponse` JSON is a pure function of the plugin bundle's content —
plugins are already stored in CAS by digest (see
`internal/coordinator/pluginresolver.go`), so the discover response is
deterministic for a given `(digest, ProtocolVersion)` pair.

This epic adds a persistent, on-disk cache — `~/.mu/discover-cache.json` —
mapping plugin CAS digest → `DiscoverResponse`. When a build resolves a
plugin whose digest is cache-hit, the coordinator uses the cached response
to satisfy all `DiscoverInfo(...)` consumers (config-schema validation,
capability checks) **without spawning the plugin process for discovery**.
Plugin processes still get started — but only lazily, when a `Plan`,
`Observe`, or `ResolveSecret` call actually needs them. For plan-only
builds with cache hits on every plugin, the bb runtime never has to spin up.

On a typical project with two plugins (go + shell-ext), this removes ~1.5 s
of bb startup plus two discover round-trips from every warm `mu build`.

## Goals

1. Persist discover responses keyed by CAS digest + ProtocolVersion.
2. Skip `mgr.Start` for plugins whose discover is cache-hit and which are
   not needed for plan/observe/secret calls in this run.
3. Invalidate automatically on digest change (trivial: cache key *is* the
   digest).
4. Survive concurrent `mu build` invocations without corruption or lost
   updates.
5. Offer a `--no-discover-cache` escape hatch.
6. Interoperate cleanly with the (forthcoming) protocol-version negotiation
   design — cached entries are tagged with the protocol version they were
   captured at.

## Non-Goals

- Caching `plan` or `observe` responses (those depend on target + deps, not
  just the plugin). That's a separate plan-cache epic (brainstorm item 6).
- Distributed / shared caches across hosts. This is a local per-user cache.
- Caching discover for command-plugins without a CAS digest (digest is zero;
  we simply don't cache those).

## User Stories

- **As a mu user running repeated builds**, I want warm builds to skip
  plugin discover latency, so that `mu build //lib/foo` feels instant for
  small targets.
- **As a mu user debugging a plugin**, I want `--no-discover-cache` so I
  can bypass the cache when hacking on a plugin in place.
- **As a mu plugin author bumping the protocol version**, I want old
  cached entries to be ignored (not crash or mis-validate) so my plugin
  upgrade is safe.
- **As a CI operator running multiple mu builds in parallel on the same
  host**, I want reads and writes to the discover cache to be safe under
  concurrency — no corrupted JSON, no lost writes, no spurious errors.
- **As a mu user inspecting my cache**, I want the cache to be a
  human-readable JSON file I can `cat`, `rm`, or inspect with `jq`.

## User Experience

- No visible change on first build after a plugin digest changes — one
  discover round-trip happens, the result is written to the cache.
- Subsequent builds: ~1.5 s faster (no bb spawn for discover).
- New flag: `mu build --no-discover-cache` bypasses both read and write.
- New flag (optional, stretch): `mu plugin discover-cache clear` to wipe.
- Verbose mode (`mu build --verbose`) logs `discover-cache hit: <name> <digest>`
  or `discover-cache miss: <name> <digest>` per plugin.

## Acceptance Criteria

### Functional

1. Given a plugin resolved to CAS digest `D`, a prior build populated the
   cache, and no change to `D`:
   - A new build SHALL NOT spawn the plugin process for discovery alone.
   - `mgr.DiscoverInfo(name)` SHALL return a response structurally
     equal to what the live plugin would have returned.
   - `ValidateTargetConfig` and `HasCapability` SHALL behave identically
     to the non-cached path.
2. Given a plugin whose digest changed since the cached entry:
   - The stale entry SHALL be ignored (cache miss) and the new response
     SHALL be written under the new key on successful discover.
3. Given a plugin whose cached `ProtocolVersion` differs from
   `plugin.ProtocolVersion` built into the `mu` binary:
   - The cached entry SHALL be ignored (cache miss).
4. Given `--no-discover-cache`:
   - The cache SHALL NOT be read (every plugin starts and discovers live).
   - The cache SHALL NOT be written.
5. Command plugins (zero digest) SHALL NOT be cached; behaviour is
   unchanged for them.
6. If all plugins are cache-hit AND the build is `--plan`/`--dry-run` AND
   plan for every target can be served without touching a live plugin:
   - The plugin manager SHALL NOT call `StartProcess` for any plugin
     whose toolchain is not referenced in any `Plan`/`Observe`/
     `ResolveSecret` call.
7. If any plugin is cache-miss, that plugin (and only that plugin) is
   started for discover, and its response is persisted.

### Non-functional

8. A corrupt `~/.mu/discover-cache.json` SHALL NOT break builds — the
   cache is treated as empty, a warning is logged, and the file is
   rewritten on successful discover.
9. Concurrent `mu build` runs SHALL NOT corrupt the cache file. The
   cache is updated atomically (write-temp + rename) with advisory file
   locking for the read-modify-write sequence.
10. The cache file SHALL be valid JSON, pretty-printed, sorted by digest
    for stable diffs.

### Test coverage

11. Unit tests in `internal/coordinator/discover_cache_test.go` cover:
    - round-trip write/read,
    - corrupted file is treated as empty,
    - protocol-version mismatch invalidates entry,
    - missing digest returns (nil, false),
    - concurrent writers (t.Parallel + N goroutines + rename atomicity).
12. Integration test in `internal/coordinator/coordinator_test.go`:
    - first run populates cache,
    - second run does not spawn the mock plugin for discover (assert via
      a counter file the plugin increments on startup),
    - toggling `--no-discover-cache` in `CoordinatorConfig` re-spawns.
13. Test for plan-only fast path: if `--plan` and all cache-hit,
    `mgr.Start` is not called (or called with an empty set).

## Design

### Cache format and location

Location: `~/.mu/discover-cache.json` (alongside `~/.mu/cache/` and
`~/.mu/plugins/`). A single flat JSON file is chosen over per-digest files
or an OCI blob because:

- Human-inspectable (one `cat`, one `jq`).
- Trivially garbage-collected alongside `mu cache gc` (brainstorm item 2).
- Tiny (~1 KB per plugin) — no scale concern.
- An OCI `discover-<digest>` tag ties discover cache into CAS GC, which
  conflates two concerns (blob storage vs. fast metadata) and forces a
  full CAS read to hydrate — slower than a single JSON decode.

Schema (proposed, v1):

```json
{
  "version": 1,
  "entries": {
    "sha256:abcdef…": {
      "name": "go",
      "protocol_version": 1,
      "cached_at": "2026-04-19T16:20:00Z",
      "response": {
        "name": "go",
        "version": "0.1.0",
        "protocol_version": 1,
        "consumes": [...],
        "produces": [...],
        "config_schema": {...},
        "capabilities": ["discover","plan","observe"]
      }
    }
  }
}
```

- Top-level `version`: cache-schema version; if the `mu` binary reads a
  different version, it ignores the file and overwrites on next write.
- Key is the full digest string (`sha256:<hex>`).
- `protocol_version` at entry level is a convenience for quick
  invalidation without unmarshalling `response`.
- `cached_at` is informational (debugging; possible future TTL).

### New package: `internal/coordinator/discovercache`

```
internal/coordinator/discovercache/
  cache.go       // Cache type, Load/Save/Get/Put
  cache_test.go
```

Interface sketch:

```go
type Cache struct {
    path string
    mu   sync.Mutex // process-local; file lock guards cross-process
}

type Entry struct {
    Name             string                  `json:"name"`
    ProtocolVersion  int                     `json:"protocol_version"`
    CachedAt         time.Time               `json:"cached_at"`
    Response         plugin.DiscoverResponse `json:"response"`
}

func Open(path string) *Cache                         // never returns error; corrupt file = empty
func (c *Cache) Get(digest cas.Digest) (*plugin.DiscoverResponse, bool)
func (c *Cache) Put(digest cas.Digest, resp *plugin.DiscoverResponse) error
func (c *Cache) Delete(digest cas.Digest) error
```

`Get` checks `Entry.ProtocolVersion == plugin.ProtocolVersion`; mismatch →
returns `(nil, false)`.

### Lookup path in pluginresolver / coordinator

Rather than putting lookup into `pluginresolver.go` (whose job is CAS
resolution, not runtime state), wire the cache into the
coordinator's Plan step in `internal/coordinator/coordinator.go` between
steps 2 and 3:

```go
// 2. Resolve plugins → CAS.
resolvedPlugins, err := resolver.Resolve(ctx, c.Config.Plugins)
...

// 2.5. Consult discover cache.
dcache := discovercache.Open(filepath.Join(home, ".mu", "discover-cache.json"))
cachedDiscover := map[string]*plugin.DiscoverResponse{} // name -> response
needsLive := make([]ResolvedPlugin, 0, len(resolvedPlugins))
for _, rp := range resolvedPlugins {
    if c.NoDiscoverCache || rp.Digest.IsZero() {
        needsLive = append(needsLive, rp)
        continue
    }
    if resp, ok := dcache.Get(rp.Digest); ok {
        cachedDiscover[rp.Def.Name] = resp
        continue
    }
    needsLive = append(needsLive, rp)
}

// 3. Start plugin manager.
mgr := plugin.NewManager(c.ProjectRoot)
mgr.SetCachedDiscover(cachedDiscover) // see Manager changes below
for _, rp := range resolvedPlugins {
    if err := mgr.Register(rp.Def); err != nil { ... }
}

// 3a. Start only plugins needing live discover.
liveNames := make([]string, 0, len(needsLive))
for _, rp := range needsLive { liveNames = append(liveNames, rp.Def.Name) }
if err := mgr.StartSubset(ctx, liveNames); err != nil { ... }

// 3b. Persist newly-discovered responses.
for _, rp := range needsLive {
    if rp.Digest.IsZero() || c.NoDiscoverCache { continue }
    info := mgr.DiscoverInfo(rp.Def.Name)
    if info != nil {
        _ = dcache.Put(rp.Digest, info) // best-effort; log-on-error
    }
}
```

### Manager changes (`internal/plugin/manager.go`)

Two surgical additions:

1. `SetCachedDiscover(map[string]*DiscoverResponse)` populates
   `pluginEntry.discover` at Register/Start time so `DiscoverInfo` returns
   the cached copy. Entries with a pre-seeded `discover` but no
   `process` are valid: `DiscoverInfo(name)` uses `discover`, but any
   `Plan/Observe/ResolveSecret` call must lazily start the process.

2. Rename `Start(ctx)` semantics or add `StartSubset(ctx, names []string)`
   that starts only the listed plugins. `Start(ctx)` becomes equivalent
   to `StartSubset(ctx, all)` for backwards compat.

3. New internal helper `ensureStarted(ctx, name)` called at the top of
   `Plan`, `Observe`, `ResolveSecret`; if `entry.process == nil`, spawns
   it (under `m.mu` held as write-lock) and **does not** re-issue a
   discover — we already have `entry.discover` from cache.

   Correctness note: we trust the cached discover for capability checks
   (`HasCapability`) even when the process is later started, because the
   content-addressing guarantees the plugin binary is byte-identical to
   what produced the cached response.

4. `Plan`/`Observe`/`ResolveSecret` currently fail with "plugin not
   started"; that check is replaced by `ensureStarted`.

### Protocol version interaction (design #3)

The protocol-version-negotiation design (pending) will likely add a
`MinProtocolVersion`/`MaxProtocolVersion` pair or analogous handshake.
This epic anticipates that:

- The cache entry records the single `ProtocolVersion` that came back on
  discover. On read, we require it to equal `plugin.ProtocolVersion` in
  the current `mu` binary. If design #3 introduces a range, this check
  becomes "cached version ∈ mu's supported range" — single-line update.
- A `mu` binary that advances `ProtocolVersion` will cache-miss all
  entries captured at older versions, which is correct: we'd want a
  fresh discover so the plugin can renegotiate.
- Because the cache key is `digest` (not `(digest, protocol)`), a
  protocol bump just forces one re-discover per plugin; after that the
  entry is overwritten. We do not keep historical entries.

### Concurrency

Multiple `mu build` runs may race on the cache file. Strategy:

1. **Reads**: `Open()` reads the whole file into memory once per build.
   No lock needed for reads — a concurrent writer's atomic rename means
   we either see the pre-state or the post-state, never a torn file.
2. **Writes**: `Put()` performs a read-modify-write under a file lock:
   - `flock(LOCK_EX)` on `~/.mu/discover-cache.json.lock` (separate
     lockfile so we can unlink the data file without dropping the lock).
   - Re-read current contents.
   - Merge new entry.
   - Write to `~/.mu/discover-cache.json.tmp` and `os.Rename` over the
     target (atomic on POSIX; Windows fallback uses `MoveFileEx` via
     `os.Rename` which is atomic on NTFS).
   - Release lock.
3. File-lock implementation: `golang.org/x/sys/unix.Flock` behind a
   tiny `internal/coordinator/discovercache/filelock_unix.go` +
   `_windows.go` pair. (Gonum isn't needed; keep dep surface minimal.)
4. Lock is held only for the merge; the rest of the build runs lock-free.
5. On lock-acquire failure (EAGAIN after 1 s) or write failure, we log a
   warning and continue — the cache is a best-effort optimization,
   never a correctness boundary.

### Invalidation summary

| Trigger                              | Mechanism                              |
| ------------------------------------ | -------------------------------------- |
| Plugin content changes               | Digest changes → new key → implicit    |
| `mu` protocol version advances       | Entry.ProtocolVersion mismatch → skip  |
| User wants to force live discover    | `--no-discover-cache` flag             |
| Cache file corrupt                   | Treated as empty; overwritten on Put   |
| Cache schema changes                 | Top-level `version` mismatch → empty   |
| Manual wipe                          | `rm ~/.mu/discover-cache.json` (or CLI)|

### CLI surface

- `mu build --no-discover-cache` (new). Also plumbed through to
  `mu observe`, `mu verify`, `mu plugin` subcommands that spin plugins.
- Plumbed into `coordinator.Config` as `NoDiscoverCache bool`.
- Optional (stretch): `mu plugin discover-cache {clear,show}` — nice to
  have; the file is already user-editable, so not required for v1.

## Technical Context

### Relevant files

| File | Role in this epic |
|------|------|
| `internal/coordinator/pluginresolver.go` | Produces `ResolvedPlugin{Def, Digest}`. Digest is the cache key. No code change needed here. |
| `internal/coordinator/coordinator.go` | Wires cache lookup between plugin resolution and `mgr.Start`. Primary integration point (`Plan` method, ~lines 72-105). |
| `internal/plugin/manager.go` | Gains `SetCachedDiscover`, `StartSubset`, and lazy `ensureStarted` in `Plan`/`Observe`/`ResolveSecret`. |
| `internal/plugin/protocol.go` | Source of truth for `ProtocolVersion` constant and `DiscoverResponse` shape used as cache payload. |
| `internal/cas/cas.go` | `cas.Digest` type used as cache key; already has `String()` and `Parse`. |
| `cmd/mu/build.go` | Adds `--no-discover-cache` flag; passes through to `coordinator.Config`. |
| `cmd/mu/observe.go`, `cmd/mu/verify.go` | Same flag. |

### Patterns discovered

- `pluginresolver.go` already demonstrates the *right* way to manage
  content-addressed derived artifacts: `bundle-<hash>` subdirs, skip-if-
  exists, glob-clean old versions. The discover cache follows the same
  model but collapsed into one JSON file (since entries are tiny).
- `plugin.DiscoverResponse` has a `HasCapability` method that defaults
  empty-capabilities to `["discover","plan"]` — the cache preserves this
  transparently because we serialize/deserialize the exact struct.
- `coordinator.go` uses `defer mgr.Close()` immediately after `Start`
  because "Plugins are only needed for planning." The cache makes this
  window even shorter — potentially zero-duration for fully cache-hit
  plan-only builds.

### Not in scope but adjacent

- `mu cache gc` (brainstorm item 2) will eventually prune stale blobs;
  it should also sweep discover-cache entries whose digest no longer
  exists in CAS. Out of scope for this epic; add as a follow-up bullet.
- Plan cache (brainstorm item 6) composes cleanly: plan cache key
  includes digest + discover, so a cached-discover build still feeds
  the plan-cache lookup correctly.

## Risks

1. **Cached discover diverges from live reality.** Only possible if the
   plugin's behaviour depends on something outside its CAS bundle (env,
   clock, network). By contract, `discover` must be pure — document
   this in `README.md` plugin section. Caching is a forcing function
   here; misbehaving plugins will surface quickly.
2. **File-lock portability.** Windows semantics differ. Mitigation:
   fall back to "no lock, trust rename" on Windows; last-writer-wins
   is acceptable for a best-effort cache.
3. **Manager API surface grows.** `StartSubset` + `ensureStarted`
   complicate `manager.go`. Mitigation: keep the old `Start(ctx)`
   method working as a thin wrapper; tests cover both paths.
4. **Lazy start breaks the current "start-all-upfront" error behaviour.**
   Today a mis-declared plugin fails fast at `mgr.Start`. With caching,
   a broken plugin hidden behind a cache hit won't fail until the first
   `Plan` call. Acceptable: errors still surface, just later — same
   user-facing message.

## Open Questions

1. Should the flag name be `--no-discover-cache` (verbose, grep-able) or
   `--fresh-discover` (shorter)? The former is consistent with
   `--no-cache` already present on `mu build`.
2. Should we piggyback on the existing `--no-cache` flag rather than
   adding a new one? *Recommendation: no* — `--no-cache` already means
   "skip CAS action-cache reads", conflating with discover would
   surprise users. Keep them independent.
3. Should cache entries record the `mu` binary version? Helpful for
   debugging stale entries after a breaking change but not required for
   correctness (protocol version suffices).
4. Should `mu plugin discover-cache {show,clear}` ship in v1, or defer?
   *Recommendation: defer* — `rm ~/.mu/discover-cache.json` works fine.
5. Interaction with design #3 once authored: if protocol negotiation
   produces per-process state (e.g. a picked version for a range),
   should we cache the negotiated version alongside discover? *Likely
   yes* — add a `NegotiatedProtocolVersion int` field to the entry, set
   equal to `Response.ProtocolVersion` today.
6. Do we need a cache-size cap? Given a reasonable ceiling of O(100)
   plugins per user and ~1 KB each, probably not in v1; revisit if a
   user ever complains.
7. Should `mu plugin list --cached` also list discover-cache state
   (populated/empty)? *Stretch*, low effort once the cache type exists.

## Rough Implementation Sketch (not a plan — that's a separate doc)

1. Add `internal/coordinator/discovercache/{cache,filelock_unix,filelock_windows}.go` + tests.
2. Add `NoDiscoverCache bool` to `coordinator.Config`.
3. Add `SetCachedDiscover` + `StartSubset` + lazy `ensureStarted` in `manager.go`; keep `Start` as `StartSubset(ctx, all names)`.
4. Wire cache consult + persist into `Coordinator.Plan` in `coordinator.go`.
5. Add `--no-discover-cache` flag to `cmd/mu/build.go`, `observe.go`, `verify.go`, `plugin.go`.
6. Unit tests: cache round-trip, corrupt file, protocol-mismatch, concurrent writers.
7. Integration test: mock plugin that increments a counter file on startup; assert second build doesn't increment.
8. Docs: README section "Plugin discover cache" near `~/.mu/` layout.
