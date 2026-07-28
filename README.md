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
go install github.com/chazu/mu/cmd/mu@latest
```

Or from source:

```bash
git clone https://github.com/chazu/mu.git
cd mu && go build -o mu ./cmd/mu
```

Requires Go 1.25+.

## Quick Start

Write a 30-line Go plugin against the SDK (`sdk/muplugin`):

```go
package main

import (
    "context"
    "github.com/chazu/mu/sdk/muplugin"
)

func main() {
    (&muplugin.Plugin{
        Name:     "hello",
        Version:  "0.1.0",
        Produces: []string{"text"},
        Plan:     plan,
    }).Main()
}

func plan(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
    return muplugin.PlanResponse{
        Actions: []muplugin.ActionSpec{{
            ID:      "write",
            Command: []string{"sh", "-c", "echo hello > hello.txt"},
            Outputs: []string{"hello.txt"},
        }},
        Outputs: map[string]string{"text": "hello.txt"},
    }, nil
}
```

`go build -o hello-go .` and reference it from `mu.cue`:

```cue
package mu

plugins: [{name: "hello", command: ["./hello-go"]}]
targets: [{
    target:    "//hello"
    toolchain: "hello"
    sources: []
    config: {}
}]
```

```bash
mu build //hello
```

That's the whole loop — no toolchain bootstrap needed for Go plugins. The
SDK derives capabilities from which optional handlers (Observe,
ResolveSecret, StoreSecret, Advise) are non-nil. Full SDK reference:
`mu guide sdk` or [`docs/guide/sdk.md`](docs/guide/sdk.md).

### Alternative: Babashka (or any other language)

Any executable speaking the NDJSON plugin protocol works. The repo ships
a number of Babashka plugins under [`plugins/`](plugins/) using the bb
toolchain — see `examples/` for a full Go-build pipeline that uses the
`go` plugin via Babashka. To use bb-based plugins, declare the bb
toolchain so mu scratch-builds it:

```cue
toolchains: [{
    toolchain: "bb"
    from:      "scratch"
    config: {
        version: "1.12.216"
        url:     "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-macos-aarch64.tar.gz"
        sha256:  "91499b3f430038f9b40e433215256a6e5392942780dca9984d493d2bcca7055d"
    }
}]
```

Working examples in [`examples/`](examples/). CUE syntax in [`docs/cue-conventions.md`](docs/cue-conventions.md).

## Commands

```
mu build      Build targets
mu scratch    Build toolchains from scratch
mu cache      Inspect/manage CAS cache (ls, inspect, size, clean, push, login)
mu target     List and inspect targets
mu graph      Show dependency chains (ASCII/DOT/JSON)
mu plugin     Search, install, update, list, inspect, add, push, test plugins
mu observe    Drift detection (pipe --json into pudl)
mu verify     Validate CAS integrity + schema namespaces
mu guide      Quick-reference help topics
mu version
```

Shared flags: `--json`, `--verbose`, `--config PATH`.

The official source-package catalog is available through `mu plugin search`.
Use `mu plugin install NAME[@VERSION]` to download and verify a GitHub release
asset, bundle it into the local CAS, register its digest in `mu.cue`, and pin
the source and bundle digests in `mu.lock`. `mu plugin update` refreshes one or
all locked catalog plugins; `mu plugin lock --json` displays the lockfile.
Use `--catalog URL` or `MU_PLUGIN_CATALOG` for a catalog mirror or local test
server.

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
