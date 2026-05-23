---
title: "feat: Go plugin SDK and bb-runtime deprecation"
type: feat
status: proposed
date: 2026-05-23
---

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

- [ ] Move `internal/plugin/protocol.go` wire types to `sdk/muplugin/types.go`; re-export from old path with a deprecation comment.
- [ ] Implement `Plugin` struct, `Run`, `Main`, capability derivation.
- [ ] Implement `SecretBackend` interface + `muplugin.SecretPlugin(backend)` constructor.
- [ ] Implement in-process test harness (`muplugin.Test(t, plugin, request)` → response).
- [ ] Write `examples/plugins/hello-go/` as the canonical 30-line plugin.
- [ ] Round-trip test: SDK plugin ↔ coordinator using the existing `plugin.Manager`.
- [ ] Document SDK in `docs/guide/plugins.md` (replace lead with Go; keep bb section).

### Tier 1 ports

- [ ] Port `scratch` plugin to Go. Acceptance: `mu scratch` works with no bb on host.
- [ ] Port `file` plugin to Go. Acceptance: scenario suite green; observe + converge byte-identical.
- [ ] Port `host` plugin to Go. Acceptance: SSH observe against the Odroid HC2 returns identical records to bb version.

### Tier 2 ports

- [ ] Factor `plugins/internal/ssh` helper.
- [ ] Port `remote-exec`.
- [ ] Port `remote-file`.
- [ ] Port `keypair-gen`.
- [ ] Port `pass`.

### Demotion

- [ ] Strip bb from default `examples/` configs.
- [ ] Update README Quick Start to use a Go plugin instead of bb.
- [ ] Move bb plugin sources to `plugins/legacy/<name>/` for one release.
- [ ] Cut `v0.2.0` release: "Go SDK + bb-optional".

### Distribution polish (parallel)

- [ ] GitHub release workflow that builds + uploads `mu-plugin-<name>-<os>-<arch>` artifacts for Tier 1 plugins.
- [ ] `mu plugin add <name> --release <tag>` shorthand for prebuilt fetch.

### Documentation

Each port and SDK milestone is incomplete until docs land alongside the code.

- [ ] `docs/guide/plugins.md` — rewrite lead section around the Go SDK; bb relegated to a "writing plugins in other languages" subsection alongside Python/Shell/Rust.
- [ ] `docs/guide/protocol.md` — link to `sdk/muplugin` types as the canonical Go representation of every wire message.
- [ ] New `docs/guide/sdk.md` topic, wired into the `mu guide` index and `runGuide` dispatcher. Covers: minimal plugin skeleton, capability auto-derivation, secret-backend interface, in-process testing.
- [ ] Per-port: each ported plugin's `GUIDE.md` must be refreshed to mention "implemented in Go using sdk/muplugin" and link to the SDK guide. The plugin's `mu.cue` keeps the `guide:` field so `mu guide plugin <name>` continues to work.
- [ ] `README.md` — Quick Start switches to a Go plugin example; bb shown only in the "alternative runtimes" section.
- [ ] `examples/plugins/hello-go/README.md` — single-file walkthrough that a new contributor can copy.
- [ ] `CHANGELOG.md` entry for v0.2.0 covering the SDK release, ported plugins, and the bb-optional posture; explicitly call out non-breaking guarantees for existing bb plugin authors.
- [ ] Migration note in `docs/guide/plugins.md`: how to port an existing bb plugin to Go (mapping table from bb idioms → SDK calls).

---

## Done definition

- A fresh user can `go install github.com/chau/mu/cmd/mu@latest`, drop a `mu.cue` that references only Go plugins, and `mu build //foo` succeeds without ever downloading bb.
- `examples/` Quick Start uses the Go plugin path.
- `sdk/muplugin` is documented, tagged, and used by at least three in-tree plugins.
- bb remains a supported plugin runtime; nothing about authoring bb plugins regresses.
