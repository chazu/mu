# Registry outage delivery — 2026-10-06

`mu-2vx` is fixed, including the other direct registry assumptions found during
the audit. Work started at `7138a34`. Final implementation at `9a36894` passed
[Linux and macOS CI](https://github.com/chazu/mu/actions/runs/37485353093), including
whole-repository unit/race/vet/build gates and native sandbox acceptance.

## Operational behavior

- Optional OCI existence/action-result lookups have their own two-second budget,
  including auth and metadata reads. Concurrent cold actions share the first
  reachability probe. An unavailable tier is quarantined for that store/command;
  a new invocation retries it. Parent cancellation does not poison the tier.
- OCI-only or OCI-first CLI cache configurations get an authoritative local disk
  tier. Rebuilt actions, plugins, and toolchain artifacts do not depend on a
  remote writer. Configured remote writes are best-effort and respect write flags.
- Shared Mu-owned HTTP clients bound connect/TLS setup to three seconds, response
  headers to five seconds, and stalled reads/writes to thirty seconds. Large
  transfers continue while making progress, without a short whole-file deadline.
  HTTP/1.1 prevents another multiplexed stream from refreshing this stream's
  progress deadline.
- Cached full-digest plugin bundles and verified single files are consulted
  before CAS/network probes. Native digest-only plugins no longer imply a
  Babashka runtime. No alternative digest/version is silently substituted.
- Toolchain receipt-cache failures can rebuild from a local archive only when
  its SHA-256 matches the configured source. Runtime binaries are verified and
  atomically snapshotted under full artifact digests; matching legacy binaries
  can migrate without fetching, and wrong cached versions are rejected.
- Planning and observation initialize only selected target plugins/toolchains.
  Unused remote definitions cannot block local work. Action-level sealed claims
  introduce their required providers after action planning; those exact resolved
  provider artifacts are retained for execution, with no planning-time value reads.
- `--no-cache` retains the CAS required for dependencies and storage while skipping
  action-result reuse. Scratch can rebuild receipts using verified archive inputs.
- Explicit push/publish/login/discovery requests retain failure semantics. Cache
  push stops at its first failed copy; discovery returns failure for unavailable
  or incomplete backends rather than an empty successful listing. Only 404/405
  unsupported tag-list endpoints retain the unversioned-row fallback.
- Registry HTTP tracing preserves the bounded transport, redacts credential
  headers, and does not pre-read response bodies.

## Explicit offline mode

```sh
mu build --offline //target
mu observe --offline //target
mu scratch --offline
mu plugin info --offline name
mu guide plugin --offline name
```

Offline mode disables configured OCI tiers, CUE registry resolution, and
Mu-managed dependency/catalog downloads. Locally available data remains usable;
missing required identities fail rather than being substituted. Catalog commands
can use local `file://` catalogs and already-resolved artifacts. Registry publish,
login, and remote-discovery operations reject offline mode; local inspection and
cache-push dry-run still work.

Configured preprocessors, external `MU_SCRATCH` commands, plugins/providers, and
actions are executable programs with their own network contracts. `--offline`
does not sandbox their networking. It also does not manufacture absent inputs
or promise an uncached toolchain can bootstrap without its source archive.

## Acceptance evidence

- The original repro runs an isolated silent HTTP registry and a disposable
  disk+OCI cache. Both installed/pre-fix Mu and source initially stalled until
  the three-second harness watchdog killed them. Fixed source completes the cold
  local action in about 2.03 seconds; warm local hits need no remote request.
- The durable CLI regression runs four dependent local actions with read/write
  remote caching against a silent registry. It succeeds after one remote probe;
  a warm rerun performs zero registry requests.
- Tests cover caller cancellation without registry poisoning, stalled headers,
  stalled bodies, a progressing transfer longer than its header/idle budgets,
  offline zero-request behavior, OCI-only local storage, missing pinned plugins,
  unused remote plugins, honest remote-discovery failures, offline CUE imports,
  extracted plugin reuse, verified toolchain snapshots, and archive recovery.
- Whole local race suite, vet, native build, Linux amd64 cross-compilation, and
  `git diff --check` passed. Both CI platforms passed the final implementation.
- PUDL's real-Mu `mise exec -- make test-kick-tires` passed against the newly built
  implementation, covering approval resume, stale-plan rejection, sealed routing,
  fail-fast preflight, Git observation, and concurrent sets. The final small
  scratch/manifest-error follow-up additionally passed affected race coverage
  and both-platform CI. No live registry publication or infrastructure convergence
  is claimed.
- No repository CUE configuration or schema was edited, so config-edit-specific
  `cue vet`/`mu validate` gates do not apply. CUE loader behavior is tested directly.

## Delivery and remaining work

The source changes and issue closure are committed and pushed. The installed Mu
is rebuilt/codesigned from the delivered checkout and verified with the outage
repro separately from source and CI qualification.

The independent P3 tasks remain: `mu-qml` central full-digest/prefix validation
and `mu-rlo` archive expansion/entry limits. This work adds the exact checks
needed for cached dependency reuse without claiming those wider tasks are done.
