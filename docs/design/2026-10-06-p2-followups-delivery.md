# P2 follow-up delivery — 2026-10-06

All five P2 issues from the two improvement reviews are implemented. Work
started at `ab26807` with a clean tree. Final code at `40198f0` passed both
Linux and macOS CI jobs, including native sandbox acceptance:
[CI run 37469092000](https://github.com/chazu/mu/actions/runs/37469092000).

## Delivered contracts

| Issue | Result |
|---|---|
| `mu-11x` | Coordinator actions carry their project root. Root-confined directory opening rejects external symlinks; bare commands enter the opened directory with `fchdir`. Relative output restoration, collection, sandbox source copying, and VM working-directory operations use the pinned root. Internal links work; a later swap cannot redirect these operations. Explicit absolute outputs and intentional bare host commands retain their host-path contracts. |
| `mu-z33` | Pure actions default to an empty environment. An explicit PATH controls executable lookup; absent PATH uses fixed `/usr/bin:/bin` lookup for the initial bare executable. Only impure bare commands may inherit the parent environment. VM and sandbox execution never imports arbitrary ambient values. Action-key v3 hashes this contract and the project root without incorporating arbitrary parent secrets. |
| `mu-r6a` | OCI manifests/action configs have 8 MiB budgets; plugin configs/indexes have 4 MiB budgets. Oversized declared descriptors fail before fetch. Actual reads, including trailing whitespace, are bounded before decoding; malformed/trailing JSON and close errors propagate with kind/limit context. Layer blobs remain outside these metadata budgets. |
| `mu-ywp` | Cache-only info/guide refuses ambiguous versions and accepts `--digest sha256:<full hash>`. Cached listing shows every identity. Project lookup retains its configured identity. Standalone extraction verifies bytes and close results, atomically publishes full-digest paths, retains older versions, and shares this implementation with discovery. Legacy short paths remain usable only when unambiguous and are superseded by full publications. |
| `mu-3pu` | A pinned Go 1.26.2 Linux/macOS matrix runs unit/race/vet/build gates. A separate static-helper gate requires native isolation and checks work writes, denied host reads/writes, read-only Linux root, and denied/allowed networking. Rooted reads, plugin publication concurrency, and symlink swaps receive both-platform coverage. |

## Migration and scope

- Action-key encoding is now v3. Plan JSON remains v2 and mode-bearing receipts
  remain v2. Existing action results miss once; regenerate saved exact-plan
  approvals. Existing content blobs remain reusable.
- Pure commands needing HOME, locale, compiler paths, or other variables must
  declare them. Secret values belong in sealed inputs rather than plain env.
- Descriptor-backed work-directory execution is qualified on Linux and macOS;
  other platforms do not silently receive pathname-based confinement.
- Bare commands retain ordinary host permissions. This change binds their
  selected cwd and relative output operations; it does not turn them into a
  filesystem sandbox. Pith's intentional direct host reads outside WorkDir
  retain their prior contract; WorkDir reads and sanctioned writes are pinned.
- Metadata budgets are independent of large artifact payloads. A synthetic
  representative action config with 20,000 outputs fits its budget. Limits
  identify the artifact kind and byte threshold in their errors.
- No repository CUE configuration changed. CUE fixture strings are exercised
  through the real loader, but config-edit-specific CUE gates do not apply.

## Validation

- Local whole-repository unit and race gates passed during implementation.
  Final VM changes passed affected package race coverage.
- Final code passed whole-repository unit/race/vet/build gates on both
  `ubuntu-24.04` and `macos-14` in the linked CI run.
- Native macOS acceptance passed locally and in CI. Native Linux acceptance
  passed in CI; cross-compilation alone is not used as runtime evidence.
- Real helper tests cover internal/external work-directory links, swaps before
  execution, swaps after directory opening, fresh output collection, cached
  output restoration, and VM file operations using pinned roots.
- Environment tests prove undeclared ambient values do not influence a pure
  action's identity or output, explicit PATH selects its executable and changes
  identity, and impure inheritance survives runtime side-channel injection.
- Cache tests cover ambiguous/exact selection, legacy aliases, retained old
  versions, concurrent publication, and interrupted/close-failed extraction.
- Metadata tests cover exact boundaries, oversized tails and deceptive sizes,
  all three remote entry points, malformed JSON, and representative large
  receipts. A five-second bounded-decoder fuzz run passed (652 executions).
- `go vet`, native CLI build, Linux amd64 cross-compilation, actionlint 1.7.7,
  and `git diff --check` passed.
- PUDL's `mise exec -- make test-kick-tires` passed using a freshly built Mu
  supplied through a temporary PATH. Coverage included approval resume,
  changed-plan rejection, sealed routing, fail-fast preflight, concurrent run
  sets, and Git observation. Subsequent VM-specific file changes are qualified
  by Go tests and both-platform CI. This is separate from Babashka plugin or
  live Docker/Kubernetes qualification.

The native gate uncovered pre-existing gaps that were fixed rather than hidden:
macOS no longer permits data reads across the entire temporary tree; Linux now
supports re-exec in test binaries, enters `/work`, translates generated runtime
paths, keeps writable work/output/temp mounts, resolves commands using declared
environment, and mounts its private read-only procfs before detaching the old
root. The kernel's [mount visibility check](https://github.com/torvalds/linux/blob/v6.8/fs/namespace.c#L5336)
informed that ordering; the native CI result establishes acceptance on the runner.
A BSD-only permission-test command was also made correct on Linux.

## Remaining work

Only the original P3 follow-ups remain open: `mu-qml` full-digest validation
versus inspection prefixes, and `mu-rlo` archive expansion/entry budgets.
There is no remaining P2 work from this review. Local installation and release
publication were not part of this source/CI delivery.
