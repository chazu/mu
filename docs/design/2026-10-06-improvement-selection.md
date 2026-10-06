# Mu improvement selection — 2026-10-06

Seven improvements were selected and implemented after inspecting the current
coordinator, executor, plugin process, CAS hierarchy, source resolver, catalog
installer, CLI plan projection, and tests. The starting commit was `db4bf0d`.
The existing whole-repository unit suite passed before changes.

Selection favors a demonstrated failure or unnecessary cost, an explicit
correctness contract, a bounded implementation, and a regression that separates
the old behavior from the desired behavior. A rejection below means rejection
from this implementation round; it does not mean the subject can never be useful.
Confidence estimates are engineering judgments, not statistical probabilities.

## 1. Initial list of 30 ideas

1. Make action cache keys unambiguous and include toolchain and output identity.
2. Publish extracted plugin bundles atomically.
3. Repair cached action results only after all output blobs arrive intact.
4. Confine source-file reads to the project root, including symlink resolution.
5. Give concurrent discovery-cache writers unique temporary files.
6. Stream local-only tiered-cache writes without buffering whole artifacts.
7. Spool remote cache fan-out to disk to bound memory.
8. Add extraction size and entry-count budgets.
9. Make plugin request cancellation cover blocked stdin writes.
10. Remove raw provider responses from protocol-error messages.
11. Make implicit dependency ordering deterministic.
12. Validate digest algorithms and lengths at external boundaries.
13. Preserve output executable modes through cache restoration.
14. Restore cached outputs through atomic file replacement.
15. Add machine-readable build progress events.
16. Explain cache misses by comparing action-key components.
17. Add critical-path build timing.
18. Introduce a persistent worker protocol for compiler plugins.
19. Add distributed action execution.
20. Add remote cache garbage collection.
21. Require signatures for downloaded plugin bundles.
22. Version the plugin capability contract more strictly.
23. Compile each plugin configuration schema once per build.
24. Reject unknown plugin configuration fields.
25. Replace filesystem-backed caches with SQLite.
26. Split the coordinator into smaller lifecycle owners.
27. Add continuous Linux and macOS validation.
28. Add fuzzing for archive, digest, and protocol boundaries.
29. Replace Babashka plugins with Go implementations.
30. Fold PUDL's convergence loop into Mu.

## 2. Systematic critical evaluation

| # | Decision | Evidence, benefit, and reason for keeping or rejecting |
|---|---|---|
| 1 | Keep | Newline-delimited fields collide: argv `["a\ncmd:b"]` hashes like `["a", "b"]`. Output declarations and toolchain digests are absent. Fixes false hits at the central cache boundary. Cost: one-time action-cache invalidation and fresh exact-plan approvals. |
| 2 | Keep | Extraction writes directly into a reusable directory; a later invalid entry leaves a directory that the next attempt accepts. Publication must follow complete extraction. Cost: staging space and retaining old bundles. |
| 3 | Keep | `GetActionResult` ignores output read errors and publishes the result even after missing blobs or failed transfers. A local cache result must reference complete local blobs. Cost: failed repairs remain misses locally. |
| 4 | Keep | The source resolver checks lexical prefixes then calls `os.Open`; symlinks can escape for both normal inputs and ewe programs. Root-confined reads enforce the documented boundary. Cost: external and absolute symlink targets are rejected. |
| 5 | Keep | Each cache instance has its own mutex, but all write `cache.json.tmp`. Independent instances race over that file. Unique staging makes each winner a complete snapshot. Cost: concurrent snapshots can still lose entries by design. |
| 6 | Keep | `Tiered.Put` buffers the complete reader even when `WriteThrough` is false. Passing the reader directly removes an artifact-sized allocation without a new abstraction. Cost: local-only errors now come directly from the store. |
| 7 | Reject/defer | Valuable for multi-gigabyte remote payloads, but adds temporary-disk failure, cancellation, and cleanup policy. The selected local fast path and streaming action-result repair give immediate benefit. Remote fan-out needs representative measurements; `mu-rlo`. |
| 8 | Reject/defer | Expanded-byte and entry budgets are useful, but arbitrary limits can reject legitimate large toolchains. Design shared limits across catalog, CAS bundles, and scratch archives with real size data; `mu-rlo`. |
| 9 | Reject/defer | `send` writes stdin before selecting on cancellation. Fixing that properly must also join scanner goroutines and handle child lifetimes; a partial timeout wrapper can leak goroutines. Separate lifecycle work with a non-reading helper process; `mu-x3z`. |
| 10 | Keep | An invalid JSON provider response is appended verbatim to the error, including a previously decoded-looking secret value. Removing wire bytes preserves useful plugin/method/decoder context. Cost: less raw debugging detail. |
| 11 | Reject/defer | Implicit edges come from map iteration, and exact-plan projection preserves their order. This can cause spurious approval mismatches, but the existing guard still refuses a mismatch. Lower priority than false cache hits or secret leakage; add stable-edge and repeated-process coverage separately; `mu-g69`. |
| 12 | Reject/defer | `ParseDigest` accepts arbitrary algorithm/hash strings; tightening it affects synthetic fixtures, legacy short-hash inspection, and OCI boundaries. Inventory those callers and distinguish full identities from display prefixes first. A global length check is too blunt. |
| 13 | Reject/defer | Receipts lack executable-mode metadata. This matters for fresh binary restores, but requires a versioned result format and OCI round-trip compatibility policy. A local `chmod` guess cannot recover producer intent; `mu-dky`. |
| 14 | Reject/defer | `writeFile` can truncate an existing output before a transfer finishes. Stage replacement together with mode preservation and a defined policy for multiple outputs; separate durability work rather than claiming multi-file atomicity; `mu-dky`. |
| 15 | Reject | Useful operator feedback, but requires a stable event schema, stdout/stderr rules, and PUDL consumption expectations. No measured progress bottleneck justifies a new public protocol in this round. |
| 16 | Reject | A hash cannot explain itself; retaining component history introduces persistence, retention, and secret-metadata decisions. Fix incorrect identity first, then establish a concrete debugging workflow. |
| 17 | Reject | Timing is informative, but scheduling and cache behavior make attribution nontrivial. No representative workload or performance target was supplied; telemetry alone does not prove an improvement. |
| 18 | Reject | Changes the subprocess lifecycle and isolation model and needs workload-specific amortization evidence. The existing NDJSON planner is intentionally simple; compiler workers belong in a motivated protocol extension. |
| 19 | Reject | Adds execution trust, platform identity, scheduling, transport, and secret-distribution contracts. Far too large before local execution and artifact restoration are fully qualified. |
| 20 | Reject | Local reachability cleanup exists; remote deletion requires registry capabilities and shared retention ownership. A guessed policy could delete artifacts another machine needs. |
| 21 | Reject | Digest pinning already supplies content integrity. Signature enforcement needs a signer trust/rotation/distribution policy and migration plan; no concrete trust model was identified. |
| 22 | Reject | Capability checks and protocol version checks already exist; stricter compatibility would break older plugins deliberately accepted today. Requires a versioned migration benefit beyond neatness. |
| 23 | Reject | Per-target schema compilation repeats work, but no profile shows it is material. A compiled-schema cache adds identity/lifetime concerns for an unmeasured optimization. |
| 24 | Reject | The validator explicitly permits extensions and warns on unknown fields. Rejecting them changes a live plugin contract and could break legitimate extensibility. |
| 25 | Reject | OCI layout is part of artifact distribution, not incidental storage. SQLite would add migrations and another authority while the discovery-cache race needs only unique temporary files. |
| 26 | Reject | The coordinator is large, but size alone does not establish a useful ownership split. A broad refactor increases review surface while the demonstrated defects have small local fixes. |
| 27 | Reject/defer | A missing CI matrix is real, but native sandbox acceptance, tool provisioning, and cross-project qualification need explicit runner expectations. Record the Linux/macOS validation work without claiming a passing matrix from local macOS tests; `mu-3pu`. |
| 28 | Reject/defer | Fuzzing is valuable when tied to explicit budgets and invariants. The selected regressions already demonstrate failures. Add fuzz corpora with the archive and boundary follow-up rather than indiscriminately fuzzing every parser. |
| 29 | Reject | Rewriting working plugins would undermine language neutrality and consume substantial effort without changing coordinator correctness. Existing Go and Babashka SDKs can coexist. |
| 30 | Reject | Conflates desired-state/approval ownership with execution ownership. Mu should keep executing plugin action DAGs; PUDL should keep owning observation, convergence, and approval. |

## 3. Detailed plans and delivered implementation

### Idea 1: Unambiguous action identity — confidence 97%

**Problem.** The old key feeds textual `cmd:...\n` and `env:key=value\n`
records into SHA-256. The hash is strong; the serialization is ambiguous.
Different commands or environment maps can generate exactly the same bytes.
The key also omits output paths, toolchain artifacts, source declarations, and
the distinction between a nil environment (inherit) and an empty environment
(clean). Those omissions permit identity reuse across observable changes.

**Concrete implementation.** In `internal/dag/actionkey.go`:

1. Begin with `mu.action-key/v2` to separate old and new identities.
2. Encode every tag, name, value, and argument with an eight-byte big-endian
   length followed by its bytes; preserve argv order.
3. Sort map keys and copied output/source declaration lists without mutating
   the action. Hash toolchain paths and digests, outputs, sources, and the
   inherited-environment and sandbox-selection flags along with the previously covered fields.
4. Keep sealed values out of identity; normalize missing sealed modes to their
   existing defaults. Keep scheduling IDs and retry policy out of successful
   artifact identity.
5. Copy the declared environment before runtime injection in the executor.
   Otherwise a temporary `MU_OUT` path enters the stored key, making the next
   lookup miss even when the plan is unchanged.

Core encoding:

```go
for _, part := range parts {
    var size [8]byte
    binary.BigEndian.PutUint64(size[:], uint64(len(part)))
    _, _ = h.Write(size[:])
    _, _ = h.Write([]byte(part))
}
```

**Why worthwhile.** It fixes deterministically reproducible false cache hits
and makes toolchain changes invalidate execution results. Existing plan JSON
includes the action key, so exact-plan identity benefits from the stronger key.

**Downsides and limits.** Existing action results miss once; saved plans must be
regenerated. Source *contents* still require declared input digests; hashing a
source name does not make an undeclared source hermetic. Inherited parent-env
values remain untracked; the new flag distinguishes inherit from clean but does
not repair that pre-existing compatibility behavior. Absolute working directories
still limit cross-checkout sharing. Malformed non-JSON pith bodies retain their
pre-existing handling; ordinary plugin bodies originate as JSON.

**Acceptance.** Added collision tests for argv newlines, environment newlines,
`=` in names, and sealed metadata; declarations invalidate keys; declaration
ordering remains invariant; inherited and empty environments differ; bare and empty-toolchain sandbox
execution differ. A regression verifies staging leaves the action environment
and cache lookup/storage key unchanged. Existing
secret-mode and configuration-format invariance tests still pass.

### Idea 2: Atomic plugin bundle publication — confidence 95%

**Problem.** The old extractor creates the final directory before reading the
entire tar. After an error, mere directory existence makes the next attempt a
hit. It also rejects legitimate names containing `..`, handles entry types
loosely, uses only twelve hash characters, and deletes older bundles while
another process may be using them.

**Concrete implementation.** In `internal/coordinator/pluginresolver.go`:

1. Validate the plugin cache name as one local path component.
2. Use `bundle-<full digest>`; short directories from the old extractor are not
   considered hits for the new full-digest path.
3. Create a unique sibling staging directory, open it through `os.OpenRoot`,
   and defer cleanup.
4. Accept only local regular-file and directory entries. Reject normalized
   duplicates, links, traversal, and unsupported types; use exclusive file
   creation and check both copy and close failures.
5. Close the extraction root and rename the complete tree to the final path.
   If another resolver already published the same directory, reuse its winner.
6. Retain older bundle paths. Use the resolver's actual `WorkDir` for catalog
   metadata instead of reconstructing a short-hash directory in the CLI.
7. Make plugin publishing collect only the resolved digest's bundle, preferring
   the exact full-hash directory over a legacy short prefix. Never merge cached
   version trees into one published artifact.

Publication boundary:

```go
staging, err := os.MkdirTemp(dir, ".bundle-*")
// Extract into staging and validate every entry.
if err := os.Rename(staging, extractDir); err != nil {
    if !complete() { return "", fmt.Errorf("publish plugin bundle: %w", err) }
}
```

**Why worthwhile.** Interrupted and concurrent resolution can no longer
advertise partially extracted plugins. Exact digest selection also keeps
publishing faithful when multiple versions are cached.

**Downsides and limits.** Staging needs temporary disk space; retaining old
bundles increases cache usage. Publication protects readers from incomplete
process-level extraction, not power-loss durability via filesystem `fsync`.
The user-owned cache remains a trust boundary. Legacy single-file extraction
still needs equivalent hardening. Cache-only info/list/guide still use their
existing directory-selection rules; explicit version selection is follow-up
`mu-ywp`. Project-based resolution and publishing select exact digests.

**Acceptance.** Truncated archives, symlinks, duplicate entries, and traversal
fail on repeated attempts without leaving entries. Sixteen concurrent resolvers
see complete content; an old in-use bundle survives; legitimate `name..sh`
works. Catalog install tests pass with full paths, and publishing tests prove
that unrelated and partial legacy version contents are excluded.

### Idea 3: Complete action-result repair — confidence 98%

**Problem.** A remote cache hit is replayed locally even if some output blobs
cannot be read or stored. `io.ReadAll` errors were ignored, allowing partial
content transfer followed by publication of an apparently complete local result.

**Concrete implementation.** In `internal/cas/tiered.go`:

1. Stream each referenced remote blob directly into the destination store.
2. Require a successful `Put`, successful reader close, and returned digest
   equal to the declared output digest.
3. On any failure emit an observer error and skip local result publication.
4. Publish the local result only after all output transfers succeed. Leave
   successfully copied CAS blobs in place, and preserve the original remote
   hit for the caller because read-repair remains best-effort.

```go
actual, putErr := local.Put(ctx, rc)
closeErr := rc.Close()
if putErr == nil { putErr = closeErr }
if putErr == nil && actual != expected { putErr = digestMismatchError }
if putErr != nil { complete = false; break }
// PutActionResult only when complete remains true.
```

**Why worthwhile.** A locally repaired receipt now makes a meaningful promise:
its output transfers completed and matched the advertised content identities.
Streaming also avoids an output-sized repair buffer.

**Downsides and limits.** A failed transfer leaves a local miss and may retry
later. Blobs copied before failure remain harmless unreferenced CAS content.
The source result is still returned, so a genuinely corrupt remote cache may
cause output restoration to fail and execution to rerun. This does not redesign
ordinary blob `Get` repair or remote write-through consistency.

**Acceptance.** Missing outputs, mid-stream errors, close errors, wrong digests,
and destination write failures never publish a local action result or report
successful result repair. Successful repair coverage remains green.

### Idea 4: Root-confined source reads — confidence 96%

**Problem.** Lexical path checking prevents `../` traversal but cannot prevent
an in-tree symlink from reading outside the project. Both normal input hashing
and ewe program CAS insertion use that vulnerable check/open sequence.

**Concrete implementation.** Replace both paths with `openProjectFile` in
`internal/coordinator/resolve.go`:

1. Normalize the project root to an absolute path.
2. Accept project-relative input paths and absolute paths lexically inside the
   root; convert them to a local relative path and reject escape.
3. Open through `os.OpenInRoot(root, rel)` so resolution itself stays confined.
4. Retain action/input/ewe-source context in the returned errors.

```go
rel, err := filepath.Rel(root, path)
if err != nil || !filepath.IsLocal(rel) {
    return nil, fmt.Errorf("path %q escapes project root", name)
}
return os.OpenInRoot(root, rel)
```

Go documents root-confined operations, including symlink behavior, in the
[`os.Root` reference](https://pkg.go.dev/os#Root). The project already pins a
Go version providing these APIs; no dependency or toolchain bump is needed.

**Why worthwhile.** Enforces the source boundary at the actual filesystem read,
including symlink swaps that a check followed by ordinary open cannot handle.

**Downsides and limits.** External symlink inputs and absolute symlink targets
are rejected; use relative links inside the tree or materialize inputs in the
project. This scopes source reads, not arbitrary trusted plugin commands,
work-directory execution, mounts, device files, or every sandbox operation.
Platform-native Linux qualification remains `mu-3pu`.

**Acceptance.** Relative in-tree symlinks work for both input hashing and ewe
program insertion; links to external files fail for both. Existing absolute
in-root paths, lexical traversal rejection, and ewe execution tests still pass.

### Idea 5: Independent discovery-cache staging — confidence 99%

**Problem.** An instance mutex serializes callers of that one object, but
separate processes or objects write the same fixed temporary path. Their writes
and renames interfere; one can rename or remove another's staging file.

**Concrete implementation.** In
`internal/coordinator/discovercache/cache.go`, create a unique temporary file
in the destination directory, write the complete JSON payload, check close,
rename that unique file to the cache path, and defer removal of the temporary
path. The resulting file is private by default (`0600`).

```go
tmp, err := os.CreateTemp(filepath.Dir(c.path), ".discover-cache-*.tmp")
defer os.Remove(tmp.Name())
// Check Write and Close before publishing.
err = os.Rename(tmp.Name(), c.path)
```

**Why worthwhile.** Each published snapshot now comes from one complete writer,
with no shared staging name or extra cross-process coordination mechanism.

**Downsides and limits.** This remains a best-effort cache: last writer wins and
concurrent snapshots can lose entries, which are rediscovered later. It is not
an interprocess merge, lock service, or power-loss durability guarantee.

**Acceptance.** Thirty-two independent cache instances begin writing together;
every Put succeeds, the final snapshot parses and contains a complete winning
entry, and no temporary files remain. The old same-instance concurrency test
alone did not exercise this failure.

### Idea 6: Stream local-only cache writes — confidence 99%

**Problem.** The tiered wrapper reads the entire artifact into memory before
writing layer zero, even when no fan-out will occur. That buffer provides no
benefit and scales with artifact size.

**Concrete implementation.** In `Tiered.Put`, buffer only when write-through
needs replay. Otherwise pass the original reader to layer zero and keep the
existing authoritative-write event/error semantics.

```go
if t.WriteThrough {
    buf, err = io.ReadAll(r)
    if err != nil { return Digest{}, err }
    r = bytes.NewReader(buf)
}
digest, err := t.Layers[0].Put(ctx, r)
```

**Why worthwhile.** Removes the wrapper's artifact-sized allocation on the
local-only path without changing the Store interface or adding temporary files.

**Downsides and limits.** This is a wrapper-memory improvement, not a claim that
every backend itself streams or that benchmark throughput improved. Remote
write-through and ordinary blob read-repair still buffer. Layer-zero writes
retain the existing policy that the authoritative local tier is writable.

**Acceptance.** A checking store sees the caller's original reader and receives
the blob. Existing write-through, read-repair, policy, and observer tests pass.

### Idea 10: Keep provider wire bytes out of errors — confidence 99%

**Problem.** An error decoding `{"value":"private bytes",BROKEN}` includes the
entire line in its diagnostic. Invalid provider traffic therefore bypasses the
normal rule excluding sealed values from logs.

**Concrete implementation.** In `internal/plugin/process.go`, remove raw
response interpolation from JSON decode errors; retain plugin name, request
method, and decoder error. Apply it to every method because observe and advice
responses can also carry sensitive content.

```go
return fmt.Errorf("plugin %q: unmarshal %s response: %w",
    p.name, req.Method, err)
```

**Why worthwhile.** Prevents a demonstrated plaintext leak on a failure path
without changing successful responses or protocol semantics.

**Downsides and limits.** Diagnostics provide less raw evidence. Plugins remain
responsible for keeping secrets out of their own stderr and declared error
strings; this change cannot sanitize arbitrary plugin-authored text.

**Acceptance.** A real shell subprocess emits malformed JSON containing a
synthetic private sentinel. ResolveSecret fails with plugin/method context and
without the sentinel or raw response. Existing process/provider tests pass.

## 4. Validation and delivery boundaries

- Baseline: `mise exec -- go test ./...` passed before edits.
- The new core regression tests were copied into an isolated archive of
  `db4bf0d`; all seven selected ideas expose old failures. The current checkout
  passes those tests. The archive did not replace or modify the working source.
- Whole repository: `mise exec -- go test ./...` passed.
- Whole repository race gate:
  `mise exec -- go test -race -p 2 -timeout 10m ./...` passed.
- `mise exec -- go vet ./...`, CLI build, and `git diff --check` passed.
- A standalone CLI smoke passed: repeated JSON plans have a stable digest,
  execution accepts that exact digest, a two-tier local cache reports a warm
  hit, and a deleted output is restored from that hit. It also exposed the
  runtime-environment mutation fixed as part of idea 1.
- Final environment/sandbox identity adjustments received affected DAG/CLI race coverage.
- Linux amd64 CLI cross-compilation passed; this is compilation evidence, not
  Linux runtime acceptance.
- Validation host: macOS arm64 with Go 1.26.2. No live registry publishing,
  Docker/Kubernetes convergence, Linux namespace execution, installation, or
  cross-repository PUDL kick-the-tires gate is claimed.
- No CUE configuration was changed, so the config-edit-specific CUE validation
  requirement does not apply.

Work is tracked in the new repository-local `.beads` workspace. The installed
`bd` command is the newer `br` CLI, which has no `onboard` subcommand; explicit
local setup avoids mutating the unrelated home-directory tracker. The initial
implementation issue is `maggie-mz6` because an inherited prefix was active at
creation; the local configuration now pins `mu`, and all follow-ups use it.

Follow-ups: `mu-x3z` plugin cancellation; `mu-dky` artifact modes and atomic
restore; `mu-g69` stable implicit edges; `mu-rlo` memory/extraction budgets;
`mu-3pu` platform CI and Linux qualification; `mu-ywp` explicit cache-only plugin
version selection and legacy single-file publication. These are open work, not
claims of delivered features.
