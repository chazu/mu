# Mu improvement selection — round 2, 2026-10-06

This round starts at `0ab0866`, after the seven improvements documented in
[the first review](2026-10-06-improvement-selection.md). Those fixes are already
present and are not counted again. The starting working tree was clean, and
`mise exec -- go test ./...` passed before editing.

Selection requires a demonstrated failure or cost, a clear execution contract,
a bounded implementation, and an acceptance test that distinguishes the old
behavior from the new behavior. Confidence percentages are engineering judgments,
not statistical probabilities. “Reject” means not excellent enough to implement
in this round, rather than a permanent objection to the subject.

## Initial 30 ideas

1. Make plugin deadlines cover blocked stdin writes and response reads.
2. Stabilize implicit dependency ordering in exact-plan fingerprints.
3. Replay cache fan-out and read-repair from disk to bound memory.
4. Preserve output permissions and restore files through verified atomic replacement.
5. Make OCI action manifests deterministic across repeated publication.
6. Verify fetched artifact digests before accepting restored outputs.
7. Bound plugin shutdown and make concurrent close safe.
8. Make queued plugin requests respect their own deadlines.
9. Add archive expansion and entry-count limits.
10. Select cache-only plugin versions by explicit digest.
11. Publish legacy single-file plugins atomically.
12. Add Linux/macOS CI with native sandbox acceptance.
13. Record inherited environment values in action identity.
14. Make cache keys portable across checkout directories.
15. Reject symlink escapes in action working directories.
16. Validate full digests separately from display prefixes.
17. Retry HTTP downloads only for transient failures.
18. Bound remotely supplied manifest and configuration JSON.
19. Add structured progress events for long builds.
20. Explain cache misses through stored identity components.
21. Prioritize actions using measured critical paths.
22. Resume interrupted builds from durable execution receipts.
23. Add remote execution workers.
24. Reuse persistent compiler workers.
25. Compile plugin configuration schemas once per build.
26. Preserve discovery-cache entries across concurrent writers.
27. Add cancellation checkpoints during large artifact transfers.
28. Make multi-output publication transactional.
29. Add focused fuzzing for archive and protocol parsers.
30. Add plugin provenance signatures and signer policy.

## Systematic evaluation

| # | Decision | Critical evaluation |
|---|---|---|
| 1 | **Keep** | `Process.send` writes stdin synchronously before selecting on context cancellation. A non-reading plugin and a request exceeding pipe capacity can hang forever. The response scanner can also outlive a cancelled exchange. Own the entire exchange and its cleanup, with a real subprocess regression. Cost: a cancelled active exchange terminates that plugin. |
| 2 | **Keep** | `Resolve` appends implicit edges from a map; plan JSON preserves slice order. Identical builds can therefore have different approval fingerprints. Sort only the synthesized edges and test actual plans across independent processes. Cost: regenerate plans whose prior order was arbitrary. |
| 3 | **Keep** | Tiered write-through and ordinary blob read-repair use `io.ReadAll`. Wrapper memory grows with the largest artifact despite streaming backends. A private tempfile provides replay without an artifact-sized allocation; explicit ownership and error cleanup make this a tractable change now. Cost: disk space and extra I/O. Archive budgets remain separate. |
| 4 | **Keep** | Executor copies use `os.Create`, losing executable bits; receipts record no modes. Cached relative outputs restore relative to Mu's process rather than the action work directory. Reads can truncate a previous output before failing. Capture permission metadata, stage and verify before rename, and deliberately rebuild legacy receipts. Cost: one-time legacy misses and changed inode identity. |
| 5 | **Keep** | `PutActionResult` ranges over the outputs map when building manifest layers. Identical results can produce different OCI manifest digests, moving the action tag and leaving equivalent manifests behind. Sort layer names. Cost: a one-time manifest-order normalization; no public schema change. |
| 6 | Consolidate into 3 and 4 | A direct remote GET explicitly does not verify the digest. Checking only a repaired store's returned hash misses direct restoration. Verify the staged bytes at the actual consumption points; a separate global fetch wrapper would add another layer and still need publication control. |
| 7 | Consolidate into 1 | `Close` can wait forever for a plugin ignoring EOF and is not safe for simultaneous calls to `cmd.Wait`. Shutdown and exchange cancellation share the same pipe/process ownership and should be implemented together. |
| 8 | Consolidate into 1 | A mutex wait ignores the queued caller's context. Replace acquisition with a cancellable gate; a queued timeout must not kill the active exchange. A separate request queue would add scheduling policy without a demonstrated need. |
| 9 | Reject/defer | Expanded-byte and entry budgets are useful, but arbitrary fixed thresholds can reject legitimate compiler/toolchain archives. Inventory representative sizes and choose one shared extraction policy. Existing `mu-rlo` retains this follow-up after memory replay is delivered. |
| 10 | Reject/defer | Cache-only commands do select by directory order, but changing this is a CLI contract decision: explicit digest, lock-backed selection, or ambiguity error. Project resolution already selects exact content. Track in existing `mu-ywp`. |
| 11 | Reject/defer | Legacy single-file extraction deserves full-digest atomic publication, but it shares cache discovery/migration behavior with idea 10. Keep both in `mu-ywp`; do not make another incompatible directory format in isolation. |
| 12 | Reject/defer | Cross-platform CI would improve qualification, but native sandbox runners, Babashka provisioning, and real PUDL tests need explicit scope. Local tests and Linux cross-compilation cannot establish that acceptance. Existing `mu-3pu` tracks it. |
| 13 | Reject/defer | Nil environments inherit untracked parent values, but hashing all of `os.Environ` could incorporate secret values and invalidate on unrelated noise. A deliberate allowlist or a clean-environment migration is needed. File a focused design issue instead of silently changing compatibility. |
| 14 | Reject | Removing absolute work directories from keys before defining project-relative execution identity could create false hits for commands that observe their location. Portability needs a relocatability contract, not textual prefix stripping. |
| 15 | Reject/defer | The lexical work-directory check does not confine symlinks. A check followed by pathname-based `exec.Cmd.Dir` remains vulnerable to swaps; a real solution needs descriptor-backed execution or sandbox enforcement and platform qualification. File this explicitly; do not claim `EvalSymlinks` fixes the race. |
| 16 | Reject/defer | Full digests and inspection prefixes are different concepts, but `ParseDigest` currently serves fixtures and multiple external paths. Tightening it globally can break valid prefix workflows. Inventory callers and introduce separate full-identity validation before choosing supported algorithms. |
| 17 | Reject | Retry classification could avoid repeated 404s and permission failures, but the fetch path already has cancellation, checksums, and atomic publication. Lower impact than hangs, incorrect artifacts, and unstable fingerprints; not enough user-visible evidence to expand this round. |
| 18 | Reject/defer | Remote manifest JSON is unbounded and deserves budgets. A shared metadata limit needs separate per-kind sizing and explicit boundary errors; arbitrary constants are insufficient. Track independently from potentially huge artifact payloads. |
| 19 | Reject | Progress events require a stable public event schema and stdout/stderr contract, plus agreement with PUDL consumers. Useful observability, but no demonstrated operator bottleneck justifies a new protocol here. |
| 20 | Reject | Explaining misses requires retaining identity components, prior snapshots, retention policy, and treatment of sensitive metadata. Correct identities and deterministic receipts come first; no concrete debugging workflow was supplied. |
| 21 | Reject | Critical-path scheduling needs representative timing data; historical durations vary with cache warmth and host speed. A new heuristic without a measured workload could worsen fairness and total runtime. |
| 22 | Reject | Pure actions already resume through cache hits. Impure actions need durable receipt semantics and post-crash uncertainty handling; replaying them from a generic checkpoint risks repeating side effects. |
| 23 | Reject | Distributed execution adds platform identity, trust, secret transport, and worker admission contracts. Local restoration and cancellation defects should be resolved before spreading those contracts across machines. |
| 24 | Reject | Persistent compiler workers change isolation and lifecycle and need amortization measurements. Plugin-owned worker experiments are a better starting point than enlarging the coordinator protocol. |
| 25 | Reject | Schema compilation repeats, but no profile shows it dominates planning. A cache creates identity/lifetime concerns without evidence of a meaningful speedup. |
| 26 | Reject | Unique discovery-cache staging already prevents file corruption. Lost entries are rediscovered; cross-process merge/locking adds complexity to a best-effort cache without evidence that lost entries are costly. |
| 27 | Consolidate into 3 and 4 | Check context between reads and before publication. This can bound work for ordinary streaming sources, but cannot interrupt an arbitrary reader already blocked in its own `Read`. Do not promise universal I/O cancellation or add goroutines that can leak. |
| 28 | Reject | Portable atomic transactions across arbitrary output paths do not follow from individual renames. Stage all restores before publishing and document per-file atomicity; a generation-directory protocol would change plugin/output consumption contracts. |
| 29 | Reject/defer | Fuzzing is strongest against explicit budgets and invariants. This round adds real lifecycle and fault-injection regressions. Archive fuzzing should accompany the common extraction limits, and metadata fuzzing should accompany bounded decoders. |
| 30 | Reject | Digests already establish content integrity. Signatures need a signer trust, rotation, revocation, and distribution model. Adding verification without an agreed policy would add operational burden rather than a clear guarantee. |

## Concrete plans and delivered changes

### 1. Own plugin exchange cancellation — confidence 95%

**What and why.** A plugin is a serial NDJSON conversation, not independent RPCs.
Once a request is partially written or a response is abandoned, the next caller
cannot safely reuse that stream. A deadline must cover both directions of I/O
and gate acquisition. Cleanup must join the scanner before releasing ownership.

**Implementation plan, now implemented:**

1. Replace the mutex with a one-token channel and select on the caller's context,
   process closure, or token acquisition. Recheck cancellation after acquiring.
2. Run the write and one response scan in one exchange worker. On cancellation,
   close owned pipe ends, terminate the child, join the worker, and await the
   direct-child reaper. A queued caller that times out never terminates the
   active exchange.
3. Own stdout/stderr pipe read ends independently of `exec.Cmd.Wait`, allowing
   final stderr to drain and allowing reads to be interrupted even if descendants
   inherited the other end.
4. Start exactly one reaper and make `Close` idempotent with `sync.Once`. Allow
   one second for an idle plugin to honor EOF, then terminate and join owned I/O.
5. On Linux/macOS and supported BSDs, launch a dedicated process group and kill
   ordinary descendants on abort. Retain direct-child termination on other OSes.
6. Close every acquired descriptor on startup failure. Continue excluding raw
   provider response bytes from decode errors.

Core cancellation shape in `internal/plugin/process.go`:

```go
select {
case <-ctx.Done():
    p.abort()
    <-done       // exchange worker has stopped touching the scanner
    <-p.waitDone // direct child has been reaped
    return fmt.Errorf("plugin %q: %w", p.name, ctx.Err())
case result := <-done:
    // Decode one response; retain safe contextual diagnostics.
}
```

**Benefit.** A 7 MiB request to a plugin that never reads stdin now exits on its
deadline rather than hanging the build. Late responses cannot race the next
scanner. Shutdown becomes an explicit owned lifecycle.

**Downsides.** Cancelling an active call kills the shared plugin and its ordinary
Unix descendants; later calls fail rather than attempting recovery. The shutdown
grace period is fixed at one second. A custom verbose `io.Writer` must not block
forever: arbitrary user-supplied `Write` cannot be interrupted. Descendants that
intentionally leave the process group are outside the tree-cancellation guarantee.
Request JSON serialization itself is synchronous; this is an I/O deadline
contract, not a hard real-time scheduling promise.

**Acceptance.** Real helper processes cover blocked writes, queued deadlines,
active cancellation, concurrent Close, idle EOF refusal, post-close rejection,
and stopped Unix descendants. The plugin package passes under the race detector.
Issue: `mu-x3z`.

### 2. Stable implicit edges — confidence 99%

**What and why.** Exact-plan approval hashes include ordered dependency arrays.
Map iteration must not create accidental ordering differences in coordinator-
synthesized dependencies.

**Implementation plan, now implemented in `internal/coordinator/resolve.go`:**

1. Preserve the first occurrence and original order of explicit plugin edges.
2. Collect implicit producer IDs, sort them, then append unseen IDs.
3. Keep input placeholders and cross-target execution wiring unchanged.
4. Test duplicate producers and overlap with explicit edges; confirm the plugin's
   slice is not mutated. Generate twelve real JSON plans in separate processes
   and require one identical 64-character `plan_sha256`.

```go
implicit := make([]string, 0, len(extraDeps))
for producer := range extraDeps {
    implicit = append(implicit, producer)
}
sort.Strings(implicit)
for _, producer := range implicit {
    if _, exists := seen[producer]; exists { continue }
    seen[producer] = struct{}{}
    merged = append(merged, producer)
}
```

**Benefit.** Stable builds no longer fail exact-plan comparison because Go chose
another map order. The dependency set and execution behavior remain the same.

**Downsides.** Older previews with arbitrary implicit ordering may need to be
regenerated once. Explicit plugin order remains significant in the plan; plugins
still must produce deterministic responses. Sorting is O(n log n) in the number
of synthesized producer edges.

**Acceptance.** Repeated resolver calls and twelve independently generated CLI
plans preserve identity. Existing exact-plan mismatch guards still pass.
Issue: `mu-g69`.

### 3. Bounded cache replay memory — confidence 96%

**What and why.** Tiered cache replay currently requires the whole artifact in
RAM. Backends already stream; the wrapper should not force an artifact-sized
allocation just to replay one stream to several stores.

**Implementation plan, now implemented in `internal/cas/{tiered,replay,stream}.go`:**

1. Keep the original-reader fast path for local-only writes.
2. For write-through, spool once to a private `0600` tempfile while computing
   SHA-256, then rewind it for the authoritative write and each remote write.
3. For blob read-repair, spool the hit, check source close and its expected digest
   before repairing any lower tier. Check each destination's returned digest.
4. Return a file-owning reader and remove its tempfile on idempotent Close.
   Error paths dispose of it immediately; Put always disposes of its replay file.
5. Check cancellation between reads/transfers and at publication checkpoints;
   wrap OCI buffering reads too. Preserve observer events and best-effort remote
   failures, while surfacing caller cancellation.

```go
digest, err := ComputeDigest(io.TeeReader(ContextReader(ctx, r), file))
if err == nil { _, err = file.Seek(0, io.SeekStart) }
// Replay the same private file, rewinding before each store.Put.
```

**Benefit.** Replay memory becomes bounded by copy/hash buffers rather than blob
size. Ordinary read-repair also rejects corrupt remote bytes before advertising
a repair. Ownership is explicit and testable.

**Downsides.** Temp disk use grows with artifact size; disk exhaustion now causes
an explicit operation error. Spooling adds disk I/O and can duplicate backend
spooling, so this is a memory improvement, not a measured throughput claim.
Get callers must Close readers, including abandoned reads. Abrupt process death
can leave tempfiles. A context wrapper cannot interrupt an arbitrary underlying
reader already blocked in `Read`; network readers must honor their own contexts.
No extraction expansion budget is included.

**Acceptance.** Tests prove both fan-out destinations see an on-disk private
replay, the file lives until caller Close, repeated Close is safe, cancellation
cleans partial spools, and corrupt blobs never repair a lower tier. Existing
local-original-reader and cache-policy/observer tests pass.
Issue: `mu-fdq`; archive work remains `mu-rlo`.

### 4. Faithful, verified output restoration — confidence 96%

**What and why.** A cached compiled program must remain executable. A failed
cache fetch must not destroy an existing artifact. Relative output paths belong
to the action's work directory, just as they do on the cold execution path.

**Implementation plan, now implemented in `internal/cas/cas.go` and
`internal/dag/executor.go`:**

1. Add receipt `version: 2` and an `output_modes` map containing permission bits
   only. Keep the existing outputs digest map and OCI envelope. Capture bytes
   and mode from the same open regular file after execution.
2. Preserve mode while copying bare `MU_OUT` and sandbox outputs to WorkDir.
3. Require supported metadata for cache restores with outputs. Legacy receipts
   deliberately miss and rebuild; guessing executable status from content or
   filename would be incorrect.
4. Resolve every relative destination against WorkDir. Open cached bytes, write
   a private sibling tempfile, hash them, reject mismatches, apply `0777` mode
   bits, and check both close operations.
5. Stage all output blobs before any rename. On read/close/hash/mode failure,
   delete the staging files and retain existing destination bytes and modes.
6. Rename staged files to their destinations. Individual replacements are atomic;
   do not claim a transaction across multiple paths.

Receipt shape:

```go
const ActionResultVersion = 2

type ActionResult struct {
    Version     int               `json:"version,omitempty"`
    OutputModes map[string]uint32 `json:"output_modes,omitempty"`
    Outputs     map[string]Digest `json:"outputs"`
    ExitCode    int               `json:"exit_code"`
}
```

Publication shape:

```go
staging, err := stageFile(ctx, destination, reader, os.FileMode(mode), digest)
closeErr := reader.Close()
// Reject either error and remove staging before replacing anything.
// Once every blob is staged:
err = os.Rename(staging, destination)
```

**Benefit.** Cold staged copies and warm restores preserve `0751` executables.
Deleted relative outputs return to the correct directory. Corrupt or interrupted
cache reads leave usable old outputs intact rather than truncated.

**Downsides.** Legacy actions with outputs rebuild once. Renaming replaces inode
identity and a destination symlink, rather than modifying its target; hard-link
aliases retain the old inode. Only ordinary permission bits are preserved, not
ownership, ACLs, extended attributes, or setuid/setgid/sticky bits. Every restore
hashes the bytes and needs space for old plus staged files. A later rename failure
can leave a partially published set; process/power failure is not a portable
multi-file transaction or an fsync durability guarantee. The existing v1 OCI
media type remains the envelope; receipt metadata is an additive JSON extension,
and older clients can still decode the outputs map but do not gain mode handling.

**Acceptance.** A cold relative executable and its deleted warm restore retain
`0751`; fault-injection tests cover read errors, close errors, digest mismatch,
legacy metadata, invalid special bits, cancellation, and a corrupt second output.
Metadata round-trips through local OCI layout and the in-memory Registry adapter.
Issue: `mu-dky`.

### 5. Canonical OCI manifest layers — confidence 99%

**What and why.** OCI manifests are content-addressed too. Sorting keys when
marshalling the config blob is insufficient when the layers array is assembled
from a Go map. The array needs an explicit canonical order.

**Implementation plan, now implemented in `internal/cas/oci/oci.go`:** collect
output names, sort them, then append their descriptors in that order. Retain
layer names, sizes, content digests, annotations, media types, and action tag.

```go
names := make([]string, 0, len(result.Outputs))
for name := range result.Outputs { names = append(names, name) }
sort.Strings(names)
for _, name := range names {
    digest := result.Outputs[name]
    // Append the descriptor with the same name, digest, size and annotations.
}
```

**Benefit.** Repeated identical publication keeps the manifest digest and action
reference stable instead of producing equivalent orphan manifests.

**Downsides.** The first normalized publication can change a pre-existing
manifest digest whose old layer order differed. Consumers must use layer names,
not rely on the accidental old map order. This does not remove already orphaned
manifests or make arbitrary user artifact metadata deterministic.

**Acceptance.** One hundred repeated publications of a result with eight named
outputs keep exactly one action-manifest identity. Existing OCI round-trips pass.
Issue: `mu-noe`.

## Validation and delivery evidence

- Baseline whole-repository unit tests passed before edits.
- Representative regressions were copied into an isolated archive of
  `0ab0866`, without changing this checkout. All five retained ideas demonstrate
  old failures: blocked plugin writes, unstable implicit plans, in-memory replay
  and accepted corruption, changed OCI manifest identities, and lost executable
  bits. Current versions of those regressions pass.
- Full repository unit suite: `mise exec -- go test ./...` passed.
- Full repository race suite:
  `mise exec -- go test -race -p 2 -timeout 10m ./...` passed after final code changes.
- `mise exec -- go vet ./...`, native CLI build, Linux amd64 CLI
  cross-compilation, and `git diff --check` passed.
- Standalone CLI smoke: repeatable exact plan, guarded cold build, deleted
  executable restored from a warm hit with `0751` mode, and lower-tier receipt
  repair after deleting the nearer cache all passed. The restored executable ran.
- Receipt permission metadata round-trips through local OCI layout and the
  in-memory Registry adapter. No live registry push/pull qualification is claimed.
- Host: macOS arm64, Go 1.26.2. Linux cross-compilation is build evidence,
  not native Linux sandbox/process acceptance. No installation, Docker/Kubernetes
  convergence, or cross-repository PUDL acceptance is claimed.
- No repository CUE build configuration was changed; config-edit-specific CUE
  gates do not apply. CUE snippets created inside temporary CLI test fixtures
  are exercised by the real config loader.

Delivered work closes `mu-x3z`, `mu-g69`, `mu-dky`, `mu-fdq`, and `mu-noe`.
The existing `mu-rlo` is narrowed to archive extraction budgets; memory replay
is delivered separately in `mu-fdq`. Existing `mu-ywp` and `mu-3pu` retain plugin
cache selection/single-file publication and platform CI qualification.

Additional scoped follow-ups: `mu-z33` inherited environment identity;
`mu-11x` working-directory symlink confinement; `mu-qml` full digest versus
inspection-prefix validation; `mu-r6a` bounded remote metadata decoding.
These are proposals needing their own contracts and tests, not delivered features.

The installed `bd` command is the newer `br` implementation: it has no `onboard`
and requires an explicit sync mode. The session uses the existing local tracker
and `bd sync --flush-only`, with git synchronization performed explicitly.
