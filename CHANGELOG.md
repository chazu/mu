# Changelog

All notable changes to mu are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) loosely.

## [v0.3.5] — 2026-08-05

### Added

- **Strict sealed action routing.** Targets may set
  `sealed_routing: "strict"` to make target-level sealed maps capability
  bounds rather than implicit grants. Every declared input must be claimed by
  at least one action, every output by exactly one, and undeclared claims or
  ref/mode changes fail during planning before provider resolution or writes.
- `TargetInfo` and the bundled file, keypair-gen, and remote-exec planners now
  carry sealed output modes through the plugin boundary.
- **Guarded exact execution.** `mu build --expect-plan-sha256 HEX` plans once,
  compares plan schema v2 before provider access, and executes that same
  in-memory graph. Mutable zero-digest command plugins are rejected.

### Changed

- **`mu build --plan --json` is plan schema version 2.** Each action now
  includes its computed action key and every execution-affecting field:
  command/body/ewe digest, inputs/outputs/dependencies, environment, sealed
  refs and modes, network/work-dir/impure/retry settings, toolchain digests,
  and sources. The top-level plan identity also commits resolved plugin content,
  version, protocol, and capabilities. This is the exact-plan surface used by
  PUDL approval/resume.
- PUDL integration documentation now describes the current `#SystemModel` and
  `pudl run-set` workflow, including mandatory approval for sealed-output sets.

### Security

- Strict sealed routing rejects unused declarations, undeclared claims,
  mismatched modes/refs, and ambiguous writers before action execution.
- Provider write-policy checks remain enforced both during planning and at the
  final write boundary.
- Execute-time providers use the exact content-addressed artifacts resolved by
  planning rather than resolving mutable definitions a second time.
- Sealed inputs resolve only when their action is actually scheduled, so cached
  or dependency-cancelled actions do not cause provider reads.

## [v0.2.2] — 2026-06-17

### Fixed

- **Pith `plan` actions now inherit the target's `sealed_inputs`.** Targets
  planned by a pith `plan` program emit bare action specs; the coordinator
  previously attached only `sealed_outputs` to them, so a pith body could never
  receive a sealed input (`secret/get` and env-mode injection both require the
  action to carry `SealedInputs`). The coordinator now fills in the target's
  `sealed_inputs`/`sealed_input_modes` on any emitted action that did not
  declare its own. `mapToActionSpec` also parses `sealed_inputs`,
  `sealed_input_modes`, and `sealed_outputs` from emitted action maps, so a pith
  plan can set per-action secrets explicitly. Regression-tested in
  `internal/coordinator/sealed_plan_test.go`.

### Added

- **`envsecret` plugin** (`plugins/envsecret/`) — a minimal read-only secret
  provider that resolves `env:NAME` to `$NAME`. Lets an existing environment
  variable flow through the sealed-input machinery without a keyring, for local
  dev/CI/demos. Guide: `mu guide plugin envsecret`.

## [v0.2.0] — 2026-06-17 "Go SDK + bb-optional"

### Added

- **`sdk/muplugin` — public Go plugin SDK.** Plugins written in Go are
  now a one-struct-literal + one-`Main()`-call affair. The SDK handles
  the NDJSON loop, capability advertisement (auto-derived from which
  optional handlers are set), error envelopes, and dispatch.
  - `Plugin{}` struct with required `Plan` field and optional
    `Observe`, `ResolveSecret`, `StoreSecret`, `Advise` handlers.
  - `SecretBackend` interface + `SecretPlugin(name, version, backend)`
    constructor for bidirectional secret-provider plugins.
  - In-process `Exchange` / `ExchangeInto` test harness — no subprocess,
    no fixtures, just function calls.
  - Canonical example: [`examples/plugins/hello-go/`](examples/plugins/hello-go/).
  - SDK guide: `mu guide sdk` (or
    [`docs/guide/sdk.md`](docs/guide/sdk.md)).

- **Go ports of seven bundled plugins** (Phase 3 Tier 1 + Tier 2 of the
  plan at `docs/plans/2026-05-23-feat-go-plugin-sdk-bb-deprecation.md`):
  - `scratch` — toolchain download / verify / extract / register.
  - `file` — local file convergence with sealed-output capture.
  - `host` — SSH-based remote host observer.
  - `keypair-gen` — ed25519/ECDSA keypair generation into sealed outputs.
  - `pass` — `pass(1)` secret provider (built on `SecretPlugin`).
  - `remote-exec` — SSH command runner with check guard, sudo, sealed outputs.
  - `remote-file` — SSH file convergence + observe.

  Each Go port lives at `plugins/<name>/main.go` and is semantically
  equivalent to its Babashka predecessor (`plugins/<name>/plugin.bb`).
  Differences are limited to JSON field ordering (Go's `encoding/json`
  alphabetizes map keys) and the `capabilities` array always including
  `"plan"` (the SDK derives this; coordinator semantics unchanged).

- `mu guide sdk` topic covering the SDK surface, capability derivation,
  the secret-backend shortcut, the in-process test harness, and a bb→Go
  porting mapping table.

- **Pith sealed inputs & outputs.** Execute-phase pith bodies can now
  read sealed inputs and write sealed outputs through a taint-tracked
  vocabulary, so an authenticated fetch-and-reshape target no longer
  has to drop to a shell command.
  - `secret/get` reads a sealed input as a tainted value (built on
    `pith.Secret` from `github.com/chazu/pith` v0.3.0); `env/get` /
    `env/get-default` read non-secret env and refuse sealed names.
  - `http/request` takes a `{url, method?, headers?, body?}` map,
    reveals secret headers/body only at the wire, and strips auth
    headers on cross-host redirects.
  - `file/write` / `file/read` are confined to `MU_SEALED_OUT_DIR` /
    `MU_OUT` / the work dir, write `0600`, and reveal a secret only at
    the syscall — the path a body uses to emit a sealed output.
  - `format/json` / `format/compact` and `cas/store` reveal real values
    into their output while tainting any string derived from a secret.
  - Secret values never enter the action cache key, traces, or error
    strings. Guide: `mu guide pith-plugins` (SECRETS IN EXECUTE
    PROGRAMS); design note: `docs/design/pith-sealed-io.md`.

### Changed

- `internal/plugin/protocol.go` wire types relocated to
  `sdk/muplugin/types.go` as the canonical home. `internal/plugin`
  retains the same exported symbols via type aliases and function
  vars — no consumer change required.
- README quick-start now leads with the Go SDK path; Babashka kept as
  the "alternative languages" example.
- `docs/guide/plugins.md` rewritten to position the Go SDK as the
  recommended authoring path; Babashka and other languages covered in
  an "OTHER LANGUAGES" subsection. New "PORTING A BB PLUGIN TO GO"
  section with a mapping table.
- `docs/guide/index.md` lists the new `sdk` topic alongside `plugins`
  and `protocol`.
- README slimmed from 1217 → ~170 lines, linking to `docs/` and
  `mu guide` for topic-specific reference.
- `cmd/mu/guide.go` shrunk from 2136 → 171 lines: long help bodies
  moved to `docs/guide/*.md` and embedded via `//go:embed`.

### Compatibility

- **Babashka plugins still work unchanged.** The bb runtime is no
  longer required for the recommended path, but every existing bb
  plugin in `plugins/` is left in place and continues to function.
  No mu.cue migration is required.
- The NDJSON wire protocol is unchanged. Existing bb / Python / Shell
  plugins do not need to be touched.
- `bb` is no longer a mandatory toolchain for the Quick Start. Projects
  that reference bb plugins still need to declare the bb toolchain in
  `mu.cue` (see README "Alternative: Babashka").

### Removed

- Implemented planning docs (7 files under `docs/plans/`) pruned after
  their corresponding features shipped: plugin-test-harness,
  action-timeouts-retries, discover-response-cache, jsonschema target
  validation, tagged plugin stderr capture, tiered cache composition,
  plugin output schemas. Equivalent functionality lives in
  `internal/plugin/scenario/`, `internal/dag/timeout_retry_test.go`,
  `internal/coordinator/discovercache/`, `internal/coordinator/validate.go`,
  `internal/plugin/process.go`, `internal/cas/tiered.go`, and
  `internal/plugin/protocol.go` respectively.
- `docs/superpowers/plans/2026-04-24-plugin-remote-discovery.md`
  pruned after `plugin push` / `plugin list --remote` shipped.

### Roadmap (deferred to a later release)

- Phase 4 of the SDK plan: move bb plugins under `plugins/legacy/`
  and flip each plugin's `mu.cue` `entrypoint` to the Go binary. The
  flip requires end-to-end testing of Go-plugin bundling through CAS.
- `mu plugin add <name> --release <tag>` — fetch prebuilt Go-plugin
  binaries from a GitHub release.
- GitHub Actions release workflow for cross-OS prebuilt plugin
  binaries.
- Shared `plugins/internal/ssh` helper extracted from `remote-exec`
  and `remote-file` (currently each plugin builds SSH commands
  inline).
