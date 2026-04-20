---
title: "feat: Tiered cache composition (disk → OCI remote)"
type: feat
status: proposed
date: 2026-04-19
---

# feat: Tiered cache composition (disk → OCI remote)

## Summary

Compose multiple `cas.Store` backends into a tiered hierarchy so that `mu build`
can transparently read from a fast local disk cache first and fall through to a
shared remote OCI registry on miss, back-filling the local tier on the way
back. Writes fan out to all writable layers. The remote tier is always optional
— if it is unreachable, the build degrades to local-only and does not fail.

This is pure wiring: `internal/config.CacheConfig.Backends` already describes a
multi-backend stack, `internal/cas/oci.OCIStore` already speaks both local OCI
layouts (`oci.NewLocal`) and remote registries (`oci.New(remote.Repository)`),
and `internal/dag.Executor.Store` depends only on the `cas.Store` interface.
The feature introduces one new type (`cas.Tiered`) and replaces the single
`oci.NewLocal` call in `cmd/mu/build.go` (~L88–L101) with a composed store
constructed from `cfg.Cache`.

## Problem Frame

Today `mu build` has exactly one cache: `~/.mu/cache` on local disk. Two
symptoms follow from this:

1. **Cold machines are fully cold.** A new developer, a fresh CI runner, or a
   containerised build starts from zero. Nothing is shared across hosts, so
   every toolchain fetch, every action, every output blob is re-derived from
   first principles on every distinct machine.
2. **The `Backends` schema is a dead letter.** `CacheConfig.Backends`,
   `ReadRepair`, and `WriteThrough` exist in `internal/config/types.go` and are
   even validated, but nothing consumes them. Users who write a
   multi-backend cache block in `mu.json` get silent no-ops.

Meanwhile the moving pieces are already in place: `OCIStore` is backend-agnostic
(any `Registry` works — local layout, `remote.Repository`, memory), and the
`cas.Store` interface is narrow and composable. We just need to glue them.

## User Experience

### Before

```jsonc
// mu.json (cache block unused — single disk cache at ~/.mu/cache)
{ "targets": [ ... ] }
```

Every machine builds from scratch; CI invalidates on every new runner.

### After

```jsonc
{
  "cache": {
    "write_through": true,
    "read_repair": true,
    "backends": [
      { "type": "disk", "path": "~/.mu/cache" },
      { "type": "oci",  "registry": "ghcr.io/acme/mu-cache", "write": true }
    ]
  },
  "targets": [ ... ]
}
```

What the user sees:

- `mu build` behaviour is unchanged when nothing is configured (single-disk
  default at `~/.mu/cache`).
- With a remote tier configured, cold builds pull action results and blobs
  from the registry instead of re-executing. Subsequent builds on the same
  machine hit local disk at full speed (read-repair has warmed it).
- When the remote registry is unreachable (offline, 5xx, auth expired), the
  build prints one structured warning and continues against local layers. It
  does not fail.
- `mu build -v` (or the build manifest) shows, per action, which layer served
  the hit: `local`, `oci:ghcr.io/acme/mu-cache`, or `miss`.
- No new CLI flags for V1 — everything is driven off `mu.json`.

## User Stories

- **As a developer new to the repo**, I want my first build to pull cached
  artifacts from the team's shared registry, so that I don't wait 20 minutes
  on toolchain/action work my teammates have already done.
- **As a CI engineer**, I want ephemeral runners to back their cache with a
  shared OCI registry, so that parallel jobs and fresh containers don't each
  re-do the same compilation.
- **As a platform operator**, I want a failed or unreachable remote cache to
  degrade gracefully to local-only, so that a registry incident doesn't break
  every build in the org.
- **As a build debugger**, I want to see which cache layer served a given
  action, so that I can tell whether my local cache or the remote one is
  responsible for a stale/corrupt hit.
- **As an existing user with no `cache` block**, I want nothing to change,
  so that upgrading `mu` is a no-op.

## Acceptance Criteria

1. **`cas.Tiered` exists.** A new `internal/cas/tiered.go` defines:
   ```go
   type Tiered struct {
       Layers       []Store
       ReadRepair   bool
       WriteThrough bool
       Observer     func(evt TieredEvent) // optional; nil-safe
   }
   ```
   and satisfies `cas.Store`.
2. **Read-through always on.** `Get` / `Has` / `GetActionResult` walk
   `Layers` in order; on a layer's miss (`nil, nil` for action result, or
   `!Has` for blob), proceed to layer N+1.
3. **Read-repair configurable.** When `ReadRepair` is true and layer N+1
   served a hit, back-fill all layers 0..N that were willing to serve the
   read (writable) with the fetched bytes / action result.
4. **Write-through configurable.**
   - When `WriteThrough` is true, `Put` / `PutActionResult` fan out to all
     writable layers; the digest from any layer is authoritative (they must
     all agree because content-addressing).
   - When `WriteThrough` is false, writes go only to layer 0 (typical
     local-only write path); read-repair still fills layers 0..N on reads.
5. **Error tolerance.** A read error from a non-final layer is logged (via
   `Observer`) and treated as a miss — traversal continues to the next
   layer. A write error to any layer other than layer 0 is logged and
   swallowed (best-effort push to remote). A failure on layer 0 propagates.
6. **Config extension.** `internal/config/types.go` keeps the existing
   `CacheConfig{Backends, ReadRepair, WriteThrough}` schema. Backwards
   compatibility:
   - No `cache` block → single local `~/.mu/cache` (current behaviour).
   - `cache.backends` with one entry → behaves like today (just that one
     layer, wrapped in a `Tiered` of length 1, which is a valid degenerate
     case).
   - `cache.backends` with multiple entries → tiered compose in declared
     order (layer 0 = fastest/nearest, last = authoritative remote).
7. **Path expansion.** `~` and `$VAR` in `backends[].path` are expanded
   before `oci.NewLocal` is called.
8. **Registry auth.** The `oci` backend resolves credentials via the Docker
   credential helper chain (oras-go default); no credentials live in
   `mu.json`. Explicit auth is out of scope for V1 (open question).
9. **Wiring.** `cmd/mu/build.go` L86–L101 is replaced with a factory that,
   given `cfg.Cache`, returns a single `cas.Store`. The `--no-cache` flag
   still produces a nil store.
10. **Observability.** Each cache hit/miss emits a `TieredEvent` with
    `{Op: "get"|"put"|"get_action", Layer: int, LayerName: string,
    Outcome: "hit"|"miss"|"error"|"repair", Digest: ...}`. The coordinator
    surfaces these through the existing `mu observe` / build-manifest path
    (reuse whatever executor already uses for per-action telemetry — see
    Open Questions).
11. **Tests.**
    - Unit test for `Tiered.Get` on miss-then-hit with read-repair fills L0.
    - Unit test for `Tiered.Put` fan-out with `WriteThrough=true`.
    - Unit test that an injected read-error on L1 does not prevent L2 from
      serving, and surfaces an event.
    - Unit test that an injected write-error on L1 does not fail a `Put`
      when L0 succeeds.
    - Integration test in `cmd/mu` that a two-layer configured build can
      complete when L1 (remote) is killed mid-build.
    - A test helper `fakeStore` with injectable failure hooks (`FailGet`,
      `FailPut`, `FailGetAction`, `FailPutAction`) lives next to the
      `Tiered` tests.
12. **No regression.** All existing `internal/cas/oci` and
    `internal/dag/executor` tests continue to pass unchanged.

## Technical Context

### Relevant Code

- `internal/cas/cas.go:62` — `cas.Store` interface. Six methods: `Has`,
  `Get`, `Put`, `Delete`, `GetActionResult`, `PutActionResult`. Narrow,
  composable.
- `internal/cas/oci/oci.go:48` — `OCIStore` is parameterised over
  `Registry`; `NewLocal` (L57) uses `oras.land/oras-go/v2/content/oci`,
  `New` (L53) accepts any `Registry` including `remote.Repository`.
- `internal/cas/oci/oci.go:238` — `GetActionResult` returns `(nil, nil)`
  on cache miss (via `errdef.ErrNotFound`), which is the contract
  `Tiered` will rely on for miss detection.
- `internal/config/types.go:87` — `CacheConfig{Backends, ReadRepair,
  WriteThrough}` already exists.
- `internal/config/types.go:94` — `CacheBackend{Type, Path, Registry,
  MaxSize, Read, Write}` already exists (note `Read`/`Write` pointers
  allow explicit opt-out per layer).
- `internal/config/validate.go:98` — `CacheConfig` is already validated
  (type ∈ {"disk","oci"}, path/registry required per type). No new
  validation needed beyond possibly rejecting empty `backends`.
- `internal/dag/executor.go:35` — `Executor.Store` is typed as
  `cas.Store`; the DAG executor does not care which backend. `Tiered`
  drops in transparently.
- `cmd/mu/build.go:86-101` — current single-store construction. The
  replacement lives here, behind a new helper in (probably)
  `internal/cas/cachefactory.go` or `cmd/mu/cachefactory.go`.

### New Types

```go
// internal/cas/tiered.go
package cas

type TieredEvent struct {
    Op        string // "get", "put", "get_action", "put_action", "has"
    Layer     int
    LayerName string
    Outcome   string // "hit", "miss", "error", "repair"
    Digest    Digest
    Err       error
}

type Tiered struct {
    Layers       []Store
    LayerNames   []string   // optional parallel slice for logging
    ReadRepair   bool
    WriteThrough bool
    Observer     func(TieredEvent)
}

// Implements cas.Store.
```

And a factory:

```go
// internal/cas/cachefactory.go (or cmd/mu/cache.go)
func BuildStore(cfg *config.CacheConfig, defaultPath string, obs func(cas.TieredEvent)) (cas.Store, error)
```

### Patterns to Follow

- Error wrapping style: `fmt.Errorf("cas/tiered: %s layer %d: %w", op, i, err)`
  matches the idiom in `internal/cas/oci/oci.go`.
- `nil`-safe observer: every emit site checks `if t.Observer != nil`.
- `io.ReadCloser` semantics for `Get`: when read-repair copies into a lower
  layer, we need to tee the stream (buffer to a temp file, then serve one
  reader to the caller while Put-ing the bytes to lower layers). Reuse the
  same "buffer to tempfile" technique already in `OCIStore.Put`
  (`internal/cas/oci/oci.go:110-151`).

### Back-fill Semantics (Read-Repair)

For `Get` returning bytes:

1. Find hit at layer H.
2. If `ReadRepair && H > 0`: tee the hit stream into a tempfile, then for
   each layer L ∈ [0, H) whose `CacheBackend.Write != false`, call
   `layer[L].Put(ctx, tempfile)`.
3. Return a reader of the tempfile to the caller (close deletes it).

For `GetActionResult`:

1. Find hit at layer H.
2. If `ReadRepair`: for each L ∈ [0, H) writable, call
   `layer[L].PutActionResult(ctx, key, result)`. This also requires that
   the output blobs referenced by the result exist at layer L. Safest
   approach: on action-result repair, also repair each output blob (it's
   cheap if already present; OCI already handles "already exists").

### Async Remote Writes (Deferred)

Per the design brief: async write to remote is an option, not a V1
commitment. Default is synchronous fan-out (writes block on all writable
layers). A `Background` or `AsyncRemote` knob can be added later without
changing the `Tiered` struct's public shape — just spawn a goroutine in
`Put` for non-zero layers when the flag is set. Kept out of V1 to avoid
lifecycle complexity (goroutine draining on build completion, error
surfacing, etc.).

### Backwards Compatibility Matrix

| `mu.json` cache block | V1 behaviour |
|---|---|
| absent | Single `oci.NewLocal(~/.mu/cache)`. Unchanged. |
| `{backends: []}` | Treated as absent (or validation error — see OQ). |
| one disk backend | Single local layer, wrapped in `Tiered` (equivalent to today). |
| one oci backend | Single remote layer. Unusual but valid. |
| disk + oci | Tiered, read-through, write-through per flags. |
| two disks | Tiered across two local paths. Legal. |

### Failure Modes and Degradation

- **Remote unreachable at startup:** `oci.New(remote.Repository)` itself is
  lazy; errors appear on first `Exists`/`Fetch`/`Push`. `Tiered` treats
  these as layer errors → miss on reads, best-effort on writes. A
  `Observer` event is emitted once per distinct `(layer, error category)`
  to avoid log spam (V1 can emit every error; dedup is an enhancement).
- **Remote becomes unreachable mid-build:** same path — subsequent reads
  treat that layer as a miss; writes are best-effort.
- **Local disk full:** error propagates from layer 0 (authoritative write
  path). The build fails, as it would today.
- **Corrupt hit at a remote layer:** out of scope for V1. Content
  addressing means the digest would not match; the caller verifies
  digests already.

## Open Questions

1. **Observability plumbing.** The design brief mentions surfacing layer
   attribution via `mu observe` or the build manifest. Need to confirm
   which of these is the actual sink in `internal/dag/executor.go` today
   — does the executor already emit per-action cache events, and if so,
   through what channel? If nothing exists, V1 can settle for `slog`
   structured logging and defer manifest integration.
2. **Credential handling for remote OCI.** V1 assumes oras-go's default
   credential resolution (Docker config / credential helpers). Do we
   need `registry_auth` fields in `CacheBackend` now, or is pushing that
   to ENV / `~/.docker/config.json` acceptable?
3. **Empty `backends` list.** Should validation reject `backends: []`, or
   treat it as "cache disabled"? Current validation loop (`validate.go`
   L99) doesn't require non-empty. Suggest rejecting in `validate.go` to
   avoid silent disable.
4. **`read=false` / `write=false` semantics per backend.** Schema already
   has the pointers; confirm that `read=false` means "skip this layer on
   Get" and `write=false` means "skip this layer on Put and on
   read-repair fill". That's the intuitive interpretation but should be
   documented in `types.go`.
5. **Action-result repair and output-blob co-residence.** If we repair an
   `ActionResult` into a lower layer, the referenced output blobs may or
   may not be present there. Do we eagerly repair every referenced blob,
   or leave them to be lazily faulted in on the next `Get`? Eager is
   simpler and avoids dangling-reference gotchas; lazy is cheaper.
   Recommendation: eager for V1.
6. **`MaxSize` enforcement.** The schema has `max_size` per backend but
   neither `OCIStore` nor the local OCI layout currently enforces it.
   Out of scope here; flag it as a separate epic.
7. **Async remote writes.** Deferred to a follow-up. Decision needed on
   whether to ship the config knob now (as a no-op) or later.

## Scope Boundaries

- **Out of scope:** eviction / GC, `MaxSize` enforcement, async remote
  writes, in-registry auth config, alternative backend types (S3, HTTP,
  nix-cache), per-target cache routing, signed cache entries, the `mu
  cache` subcommand.
- **In scope:** `cas.Tiered`, factory from `CacheConfig`, wiring into
  `cmd/mu/build.go`, graceful degradation, layer attribution events,
  tests with injectable fake remote.

## Migration / Rollout

No user action required on upgrade. Users who want the remote tier add a
`cache.backends` block to `mu.json`. The only visible behavioural change
for existing users is new `slog` lines describing cache layer hits, which
are at debug level.
