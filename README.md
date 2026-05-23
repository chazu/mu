# mu

Language-agnostic build coordinator. mu knows nothing about programming languages, compilers, or toolchains. External plugins emit action subgraphs via a simple NDJSON protocol; mu orchestrates them as a unified DAG of content-addressed actions.

The name means "emptiness" in Japanese. The build system has no built-in semantics. Plugins fill it with meaning.

## How It Works

```
  mu.cue ──► Config Loader ──► Coordinator ──► DAG Executor
                                                   │
                          ┌────────────────────────┼────────────────┐
                          ▼                        ▼                ▼
                   Plugin Manager              CAS (OCI)       Scratch Builder
                   (stdin/stdout)
                          │
                   ┌──────┼──────┐
                   ▼      ▼      ▼
                 go.bb  rust.bb  any.exe
```

Plugins decide what to build and how. mu executes.

### Core Primitives

- **Artifacts** — content-addressed blobs stored by SHA-256 hash
- **Actions** — hermetic transformations: input artifacts → output artifacts
- **Plugins** — external executables that emit action subgraphs via NDJSON protocol
- **Toolchains** — built from scratch as content-addressed artifacts (download, verify, extract)

### Design Principles

- **Plugin protocol over built-in rules.** LSP model applied to builds.
- **Content-addressed everything.** Universal caching across all languages.
- **OCI as the cache layer.** Same OCI layout locally and remotely.
- **Minimal and composable.** ~7,500 LOC of Go. Plugins in any language.

See [`docs/architecture/mu-conceptual-model.md`](docs/architecture/mu-conceptual-model.md) for the full mental model.

## Installation

```bash
go install github.com/chau/mu/cmd/mu@latest
```

Or from source:

```bash
git clone https://github.com/chau/mu.git
cd mu && go build -o mu ./cmd/mu
```

Requires Go 1.25+.

## Quick Start

Create `mu.cue`:

```cue
package mu

toolchains: [{
    toolchain: "bb"
    from:      "scratch"
    config: {
        version: "1.12.216"
        url:     "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-macos-aarch64.tar.gz"
        sha256:  "91499b3f430038f9b40e433215256a6e5392942780dca9984d493d2bcca7055d"
    }
}]
plugins: [{name: "go", script: "plugins/go"}]
targets: [{
    target:    "//cmd/hello"
    toolchain: "go"
    sources: ["go.mod", "go.sum", "cmd/hello/main.go"]
    config: {output: "hello", pkg: "./cmd/hello"}
}]
```

Build:

```bash
mu build //cmd/hello
```

Working examples in [`examples/`](examples/). CUE syntax in [`docs/cue-conventions.md`](docs/cue-conventions.md).

## Commands

```
mu build      Build targets
mu scratch    Build toolchains from scratch
mu cache      Inspect/manage CAS cache (ls, inspect, size, clean, push, login)
mu target     List and inspect targets
mu graph      Show dependency chains (ASCII/DOT/JSON)
mu plugin     List, inspect, add, push, test plugins
mu observe    Drift detection (pipe --json into pudl)
mu verify     Validate CAS integrity + schema namespaces
mu guide      Quick-reference help topics
mu version
```

Shared flags: `--json`, `--verbose`, `--config PATH`.

Run `mu guide` for the topic index. Each topic has its own page:
`overview`, `mu.cue`, `plugins`, `build`, `observe`, `pudl`, `cache`,
`secrets`, `secret-gen`, `toolchains`, `shell`, `protocol`,
`secret-providers`, `pith-plugins`, `sandbox`, `advice`.

## Topics

| Topic | Where |
|-------|-------|
| Conceptual model, primitives, hermeticity | [`docs/architecture/mu-conceptual-model.md`](docs/architecture/mu-conceptual-model.md) |
| BRICK ecosystem (mu + pudl) | [`docs/architecture/brick-ecosystem.md`](docs/architecture/brick-ecosystem.md) |
| BRICK project layout | [`docs/architecture/brick-project-guide.md`](docs/architecture/brick-project-guide.md) |
| CUE config reference | [`docs/cue-conventions.md`](docs/cue-conventions.md) · `mu guide mu.cue` |
| Plugin protocol (NDJSON) | `mu guide protocol` · [`docs/plugin-output-schemas.md`](docs/plugin-output-schemas.md) |
| Writing plugins | `mu guide plugins` |
| Inline programs (pith VM) | `mu guide pith-plugins` |
| Sealed inputs/outputs (secrets) | `mu guide secrets` · [`docs/sealed-input-delivery-modes.md`](docs/sealed-input-delivery-modes.md) · [`docs/secrets-write-policy.md`](docs/secrets-write-policy.md) |
| Secret-gen toolchain | `mu guide secret-gen` · [`docs/secret-gen-toolchain.md`](docs/secret-gen-toolchain.md) |
| Sandbox model | `mu guide sandbox` |
| Toolchains from scratch | `mu guide toolchains` |
| Cache (CAS + OCI remote) | `mu guide cache` |
| pudl integration | `mu guide pudl` |

## Bundled Plugins

| Plugin | Description |
|--------|-------------|
| `aws` | AWS resource observer (EC2, VPC, subnets) via AWS CLI |
| `cowsay` | Demo text transformation |
| `docker` | Docker image builder |
| `file` | File convergence (write/copy/symlink/delete) + sealed-output capture |
| `go` | Go binaries (cross-compile, tags, ldflags, race) |
| `host` | Remote host observer over SSH |
| `k8s` | Kubernetes convergence + drift + Secret capture |
| `keypair-gen` | ed25519/ECDSA keypair generator → sealed outputs |
| `lint` | Linter wrapper (observe + fix) |
| `pass` | Bidirectional secret provider over [pass](https://passwordstore.org) |
| `remote-exec` | Run commands over SSH with sealed-output fetch |
| `remote-file` | Converge a file on a remote host over SSH |
| `scratch` | Toolchain bootstrapping |
| `sops` | Bidirectional secret provider over [SOPS](https://github.com/getsops/sops) |
| `terraform` | Infra provisioning + drift + sensitive-output capture |
| `void` | Build webhook reporter (advice plugin) |
| `zig` | Zig toolchain |

## Project Status

v0.1.0 — coordinator functional end-to-end. CAS, DAG, parallel executor, sandbox (Linux namespaces + macOS Seatbelt), NDJSON plugins, CUE config, sealed inputs/outputs, pith VM, plugin distribution via OCI, `mu verify`, `mu graph`, `mu guide`.

### Roadmap

- Tiered cache composition (read-repair, write-through)
- CLI polish: color output, consistent `--json`
- GOCACHEPROG bridge
- 1Password / OPA / buildpack / ko plugins
- Remote execution (worker pools)
- Protocol extensions (streaming progress, async planning)

## License

TBD
