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
                 go.bb   pass   any.exe
```

Plugins decide what to build and how. mu executes.

### Core Primitives

- **Artifacts** — content-addressed blobs stored by SHA-256 hash
- **Actions** — commands that turn input artifacts into output artifacts, cached by a key over their inputs
- **Plugins** — external executables that emit action subgraphs via NDJSON protocol
- **Toolchains** — built from scratch as content-addressed artifacts (download, verify, extract)
- **Sealed I/O** — provider-backed values resolved at execution time, never
  stored in the cache or manifest; each action claims exactly the sealed
  names its target declares

### Design Principles

- **Plugin protocol over built-in rules.** LSP model applied to builds.
- **Content-addressed everything.** Universal caching across all languages.
- **OCI as the shared cache layer.** Remote caches are plain OCI registries; the
  local cache keeps OCI-style blobs plus one atomic file per action result.
- **Minimal and composable.** One Go binary; plugins in any language.

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

Requires Go 1.26.2+.

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
SDK derives capabilities from which handlers (Plan, ResolveSecret,
StoreSecret) are non-nil. Full SDK reference:
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
build (b)  Build one or more targets
scratch    Build toolchains from scratch
cache      Inspect the local CAS cache (ls, inspect, size, clean, push, login, logout)
targets    List targets and their dependency chains (alias t; --deps/--rdeps, --dot, --json)
plugin (p) List and inspect plugins (list, info, install)
verify     Re-hash CAS blobs and report corrupt or missing ones
guide      Quick-reference guides for mu features
version    Print the mu version
help       Show help for mu or a command (mu help <command> [<subcommand>])
```

These are the commands `mu --help` lists.

Common flags, on the commands that use them: `--config PATH`, `--json`,
`--verbose`, `--offline`. Run `mu <command> -h` for a command's flags.

Plugins are distributed through the official GitHub release catalog.
Use `mu plugin install NAME[@VERSION]` to download and verify a release
asset, bundle it into the local CAS, register its digest in `mu.cue`, and pin
the source and bundle digests in `mu.lock`. Re-run install to update.
Use `--catalog URL` or `MU_PLUGIN_CATALOG` for a catalog mirror or local test
server.

Run `mu guide` for the topic index. Each topic has its own page:
`overview`, `mu.cue`, `plugins`, `build`, `cache`,
`secrets`, `secret-gen`, `toolchains`, `shell`, `protocol`,
`secret-providers`, `sandbox`, `sdk`.

## Topics

| Topic | Where |
|-------|-------|
| Conceptual model, primitives, hermeticity | [`docs/architecture/mu-conceptual-model.md`](docs/architecture/mu-conceptual-model.md) |
| CUE config reference | [`docs/cue-conventions.md`](docs/cue-conventions.md) · `mu guide mu.cue` |
| Building targets, flags, manifests | `mu guide build` |
| Plugin protocol (NDJSON) | `mu guide protocol` |
| Writing plugins | `mu guide plugins` · `mu guide sdk` |
| Built-in shell toolchain | `mu guide shell` |
| Sealed inputs/outputs (secrets) | `mu guide secrets` · `mu guide secret-providers` |
| Secret-gen toolchain | `mu guide secret-gen` |
| Sandbox model | `mu guide sandbox` |
| Toolchains from scratch | `mu guide toolchains` |
| Cache (CAS + OCI remote) | `mu guide cache` |

## Bundled Plugins

Generated from `plugins/*/mu.cue` (runtime) and each plugin's `GUIDE.md`
summary; `mu guide plugin <name>` shows the full guide.

| Plugin | Runtime | Description |
|--------|---------|-------------|
| `docker` | bb | Docker image builder: builds Docker/OCI images from Dockerfiles |
| `doppler` | bb | Read-only secret provider over the [Doppler](https://doppler.com) CLI |
| `file` | Go | File convergence: write content, set permissions, create symlinks, or delete files |
| `go` | bb | Compiles Go packages into executables, archives, or shared libraries |
| `k8s` | bb | Applies Kubernetes manifests with kubectl; can capture cluster Secret values into a secret backend |
| `keypair-gen` | Go | Generates an ed25519 or ECDSA keypair and routes both halves through `sealed_outputs` |
| `lint` | bb | Language-agnostic linter wrapper (check; rewrites sources only with `fix: true`) |
| `pass` | Go | Read/write secret provider over [password-store](https://www.passwordstore.org) |
| `remote-exec` | Go | Runs a command on a remote host over SSH, with an optional `check` guard |
| `remote-file` | Go | Makes a file on a remote host match a local file's bytes, mode, and owner (SSH) |
| `sops` | bb | Read/write secret provider over [SOPS](https://github.com/getsops/sops)-encrypted files |
| `terraform` | bb | Terraform init, plan, and apply |

bb plugins run on the Babashka toolchain (declare it as shown above). Go
plugins run as prebuilt binaries, which are not committed; build one into
its directory before a project references it with `script: "plugins/<name>"`:

```bash
go build -trimpath -o plugins/<name>/plugin ./plugins/<name>
```

`mu plugin install <name>` builds a catalog package that needs it (Go must be installed).

## Project Status

`mu version` prints the version; each release is a tag, and
[`CHANGELOG.md`](CHANGELOG.md) records what changed in it. Open and planned
work lives in the repository's beads tracker: run `br list` (or `br ready`
for unblocked work) rather than looking for a roadmap here.

## License

TBD
