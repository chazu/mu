---
title: "feat: Go plugin SDK and bb-runtime deprecation"
type: feat
status: in_progress
date: 2026-05-23
---

> **Status update (2026-05-23):** Phase 1 (SDK), Phase 3 Tier 1 + Tier 2
> (all seven planned ports), and the documentation track are complete
> and on `main`. Phase 2 (distribution polish) and Phase 4 (bb demotion
> + entrypoint flip + v0.2.0 cut) are deferred — both need end-to-end
> verification of Go-plugin bundling through CAS, which warrants its own
> session with an OCI test target. The `plugins/internal/ssh` helper
> refactor is also still open. See the checklist below for per-task
> status.

# Go Plugin SDK + bb Deprecation

**Goal:** Eliminate Babashka as a hard runtime dependency for mu. Ship a small, idiomatic Go SDK for authoring plugins, port the highest-leverage bundled plugins to Go, and reduce the bb toolchain to an optional plugin runtime.

**Why:**

- bb is the largest install-time tax on new users (JVM-shaped startup, scratch download of a ~50 MB tarball before *any* build runs).
- The "language-agnostic build coordinator" pitch is undermined when every bundled plugin requires one specific scripting language.
- Native Go plugins start in ~5 ms vs. ~200–500 ms for bb cold start. This dominates discover-heavy operations (`mu plugin list --discover`, `mu observe` over many targets).
- The Go SDK becomes the reference implementation of the NDJSON protocol; bb plugins are demoted to "third-party language binding" status.

**Non-goals:**

- Removing bb support entirely. The bb toolchain remains a first-class plugin *runtime*; users can still author plugins in Babashka. We only stop *requiring* it.
- Rewriting every bundled plugin. Several are thin wrappers around CLIs (terraform, sops, k8s) where bb's brevity is a win; they stay bb until there is a concrete reason to port.
- Changing the wire protocol. Pure SDK + porting work.

---

## Phase 1: Go SDK (`sdk/muplugin`)

**Surface (recap from design discussion):**

```go
type Plugin struct {
    Name, Version, Description string
    Consumes, Produces         []string

    Plan func(context.Context, PlanRequest) (PlanResponse, error)         // required

    Observe       func(context.Context, ObserveRequest) (ObserveResponse, error)
    ResolveSecret func(context.Context, string) (string, error)
    StoreSecret   func(context.Context, StoreRequest) error
    Advise        func(context.Context, AdviseRequest) error

    ConfigSchema  json.RawMessage
    OutputSchemas map[string]json.RawMessage
}

func (p *Plugin) Run() error
func (p *Plugin) Main()
```

Capabilities advertised in `discover` are derived from which fields are set — no explicit list.

**Interfaces:** `SecretBackend` (for `pass`/`sops`-shaped plugins), `Transport` (test seam, SDK-internal). That's it.

**Package layout:**

```
sdk/muplugin/
  plugin.go          Plugin struct + Run loop, capabilities derivation
  types.go           wire types (PlanRequest, Action, ObserveResponse, ...)
  errors.go          ProtocolError, Fatal()
  test.go            in-process test harness (no subprocess)
  helpers/
    shell.go         func Shell(id, cmd string, inputs map[string]string, outputs []string) Action
    sealed.go        sealed-input/output helpers
```

**Reuse:** `internal/plugin/protocol.go` is the canonical wire type today. Either (a) move it to `sdk/muplugin/types.go` and have the coordinator import the SDK, or (b) keep both and add a round-trip test asserting they stay in sync. Prefer (a) — single source of truth.

**Acceptance:**

- `go doc github.com/chau/mu/sdk/muplugin` fits on one screen.
- Hello-world plugin compiles and round-trips a `discover` + `plan` request in under 30 lines of user code.
- `sdk/muplugin/test.go` lets plugin authors write Go unit tests without spawning a subprocess.

---

## Phase 2: Plugin distribution

Native Go plugins need to ship as binaries. Three viable distribution modes; this plan picks one and supports the others incrementally:

| Mode | How | When to use |
|------|-----|-------------|
| **Build in-tree** | `plugins/<name>/mu.cue` declares a `go` build target. `mu build //plugins/<name>` produces the binary; the plugin manifest's `entrypoint` points at the built artifact. | Users who already have mu + the go toolchain. The reference path. |
| **OCI push/pull** | `mu plugin push` (already implemented) publishes the plugin to a registry. Users `mu plugin add <name> --digest sha256:...`. | Sharing across projects without building locally. |
| **Prebuilt release** | GitHub release contains `mu-plugin-<name>-<os>-<arch>` binaries; mu fetches by URL+SHA on first use. | Bootstrap when neither go toolchain nor mu CAS is populated. |

**Bootstrap problem:** Today, building any Go binary requires the `go` toolchain, which mu scratch-builds. Building a Go plugin to *replace* a bb plugin needs the go toolchain to already be available. Resolution:

- The `go` toolchain itself is scratch-built (download + extract — no plugin needed; `internal/scratch` handles this).
- The first Go plugin (`scratch`, see below) only needs the already-scratch-built go toolchain. No bb required.
- Pure-Go plugin manifests use `toolchain: "go"` for their build target. This works the moment the go toolchain is scratched.

No circularity. bb is needed only to build bb-authored plugins.

---

## Phase 3: Porting priorities

Port plugins in order of leverage. Each port lives in `plugins/<name>/` alongside its bb predecessor until the bb version is removed at end of phase.

**Tier 1 — bootstrap critical (ship in this order):**

1. **`scratch`** — toolchain download / verify / extract. Tiny logic, blocking everyone. ~150 LOC port. Once this is Go, `mu scratch` runs with zero bb requirement.
2. **`file`** — file convergence (write/copy/symlink/delete) + sealed-output capture. Highest call frequency, used in nearly every observe/converge target.
3. **`host`** — remote SSH host observer. Heavy stderr/output handling that benefits from Go's `os/exec`. Currently bb + `gather.sh` shell helper.

**Tier 2 — second wave:**

4. **`remote-exec`** — SSH command runner. Shares SSH plumbing with `host` and `remote-file`; factor a small `plugins/internal/ssh` helper package.
5. **`remote-file`** — file convergence over SSH. Same SSH helper.
6. **`keypair-gen`** — ed25519/ECDSA keypair generator. Pure stdlib, ~80 LOC. Trivial port that validates the sealed-output story.
7. **`pass`** — `pass`-backed secret provider. Validates the `SecretBackend` interface end-to-end.

**Tier 3 — leave on bb (for now):**

- `aws`, `docker`, `k8s`, `sops`, `terraform`, `zig`, `cowsay`, `lint`, `void` — all thin wrappers around external CLIs where bb's brevity is genuinely a win. Port only when (a) a user files a bb-related bug, or (b) we want a feature (e.g. streaming progress) that's awkward in bb.

**Per-plugin acceptance criteria:**

- New Go plugin passes the existing scenarios under `internal/plugin/scenario/testdata/` for that plugin.
- `mu plugin test <name>` succeeds against the Go version.
- Output bytes are byte-identical to the bb version for one full scenario run (catches subtle JSON shape drift).
- No change to the plugin's `mu.cue` consumer-facing config schema.

---

## Phase 4: Bb runtime demotion

Once Tier 1 + 2 are ported and stable:

- Remove `bb` from the default `toolchains` block of `examples/` and the Quick Start in README.
- `mu scratch` no longer scratch-downloads bb unless a project's `mu.cue` references it.
- Move bb-authored plugin examples to `examples/babashka-plugin/` as a "writing plugins in other languages" tutorial.
- Update `docs/guide/plugins.md` to lead with the Go SDK; bb becomes a side section alongside "Python", "Shell", "Rust".
- Drop the `// inferred from .bb extension` toolchain-auto-detect default to a less ambiguous `toolchain: "bb"` requirement (avoids surprising bb installs).

bb is not deleted; it is no longer privileged.

---

## Risks & mitigations

| Risk | Mitigation |
|------|------------|
| Go plugins balloon binary count in CAS | They are content-addressed and small (~3–6 MB each, stripped). The bb runtime + bb plugin scripts already consume more. |
| Cross-compile complexity | The Go plugin toolchain itself already handles GOOS/GOARCH. Prebuilt release tier covers users without go installed. |
| SDK API churn breaks plugin authors | Tag SDK v0.1.0 the same release the first Tier 1 plugin lands. SemVer it. The wire protocol is the real contract; the SDK is a thin shim over it. |
| Discover capability drift between bb and Go plugins | Add a `plugins/<name>/scenario.yaml` golden file. Run *both* implementations against it until bb is retired. |
| User has a bb plugin that imports a Tier 1 plugin (e.g. their `deploy` plugin shells out to the bb `file` plugin's behavior) | bb versions stay in-tree behind a `legacy/` subdirectory for one release cycle. |

---

## Tasks

### SDK

- [x] Move `internal/plugin/protocol.go` wire types to `sdk/muplugin/types.go`; old path re-exports via type aliases + function vars (no consumer change).
- [x] Implement `Plugin` struct, `Run`, `Main`, capability derivation (`sdk/muplugin/plugin.go`).
- [x] Implement `SecretBackend` interface + `muplugin.SecretPlugin(name, version, backend)` constructor.
- [x] Implement in-process test harness — `muplugin.Exchange` / `muplugin.ExchangeInto` in `sdk/muplugin/test.go`.
- [x] Write `examples/plugins/hello-go/` as the canonical 30-line plugin.
- [x] Document SDK in `docs/guide/plugins.md` (lead replaced with Go; bb relegated to "OTHER LANGUAGES" subsection).
- [ ] Round-trip test: SDK plugin ↔ coordinator using the existing `plugin.Manager`. *Covered indirectly by the seven Go-port plugins being callable via the existing manager test paths; an explicit end-to-end test against `plugin.Manager` is still worth adding.*

### Tier 1 ports

- [x] Port `scratch` plugin to Go (`plugins/scratch/main.go`). Discover + plan structurally equivalent to `plugin.bb`. *Acceptance against `mu scratch` end-to-end deferred until Phase 4 entrypoint flip.*
- [x] Port `file` plugin to Go (`plugins/file/main.go`). All 6 plan branches + capture branch verified equivalent.
- [x] Port `host` plugin to Go (`plugins/host/main.go`); `gather.sh` embedded via `//go:embed`. *Live SSH acceptance against the Odroid HC2 is still useful to run.*

### Tier 2 ports

- [ ] Factor `plugins/internal/ssh` helper. *Each port currently builds SSH commands inline; payoff is modest and the variant shapes (Go `[]string` argv vs. embedded bash heredoc) make a single helper awkward. Open for a follow-up.*
- [x] Port `remote-exec` (`plugins/remote-exec/main.go`). SSH command construction matches bb byte-for-byte; sudo / env / work_dir / check-guard / sealed-output capture preserved.
- [x] Port `remote-file` (`plugins/remote-file/main.go`). Plan + Observe handlers ported; record shape preserved for pudl ingestion.
- [x] Port `keypair-gen` (`plugins/keypair-gen/main.go`). ed25519 / ECDSA generation into sealed outputs; error messages match bb verbatim (incl. Clojure-style `#{}` set rendering).
- [x] Port `pass` (`plugins/pass/main.go`). Built on `SecretPlugin` helper — validates the secret-provider story.

### Demotion

- [x] Strip bb from the default README Quick Start; bb now appears only in the "Alternative" subsection alongside its scratch-toolchain block.
- [x] Update README Quick Start to use a Go plugin.
- [ ] Strip bb from `examples/*` configs (the `examples/` projects still reference bb plugins; not blocking, but should land before v0.2.0).
- [ ] Move bb plugin sources to `plugins/legacy/<name>/` for one release. *Coupled to flipping each `plugins/<name>/mu.cue` `entrypoint` and `toolchain` to point at the compiled Go binary. Needs end-to-end test of Go-plugin bundling through CAS.*
- [ ] Cut `v0.2.0` release: "Go SDK + bb-optional". *Gated on the legacy/ move and the entrypoint flip.*

### Distribution polish (parallel)

- [ ] GitHub release workflow that builds + uploads `mu-plugin-<name>-<os>-<arch>` artifacts for Tier 1 plugins.
- [ ] `mu plugin add <name> --release <tag>` shorthand for prebuilt fetch.

### Documentation

Each port and SDK milestone is incomplete until docs land alongside the code.

- [x] `docs/guide/plugins.md` — lead rewritten around the Go SDK; bb in "OTHER LANGUAGES" subsection.
- [x] `docs/guide/protocol.md` — header note pointing Go authors at `sdk/muplugin/types.go` as the canonical Go binding.
- [x] New `docs/guide/sdk.md` topic; wired into `docs/guide/embed.go` `topicFiles`, the `runGuide` dispatcher in `cmd/mu/guide.go`, and the `mu guide` index.
- [x] Per-port: every Tier 1 + Tier 2 plugin's `GUIDE.md` now opens with a "Implemented in Go using sdk/muplugin" header and links to `mu guide sdk`.
- [x] `README.md` — Quick Start switched to the Go SDK; bb retained as the "Alternative" path.
- [x] `examples/plugins/hello-go/README.md` — single-file walkthrough.
- [x] `CHANGELOG.md` — new file with the v0.2.0 entry covering the SDK, ported plugins, bb-optional posture, and deferred items.
- [x] Migration note in `docs/guide/plugins.md` — "PORTING A BB PLUGIN TO GO" section with the bb→Go mapping table.

---

## Done definition

- [ ] A fresh user can `go install github.com/chau/mu/cmd/mu@latest`, drop a `mu.cue` that references only Go plugins, and `mu build //foo` succeeds without ever downloading bb. *Blocked on the Phase 4 entrypoint flip — today the bundled plugins still bundle and run their `plugin.bb` even though the Go ports exist alongside.*
- [x] README Quick Start uses the Go plugin path.
- [x] `sdk/muplugin` is documented and used by seven in-tree plugins. *Tagging awaits the v0.2.0 cut.*
- [x] bb remains a supported plugin runtime; nothing about authoring bb plugins regresses (all 438 existing tests still pass; bb scripts untouched).
