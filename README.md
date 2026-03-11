# mu

A language-agnostic build coordinator. mu knows nothing about programming languages, compilers, or toolchains. External plugins emit action subgraphs via a simple protocol, and mu orchestrates them as a unified DAG of content-addressed actions.

The name means "emptiness" in Japanese. The build system has no built-in semantics. Plugins fill it with meaning.

## How It Works

```
                    ┌──────────────┐
  BUILD.json ──────►│              │
  mu.json ─────────►│  Config      │──── validated config ────► Coordinator
                    │  Loader      │                            │
                    └──────────────┘                            │
                                                               ▼
                                                   ┌───────────────────┐
                                                   │   DAG Executor    │
                                                   │   (goroutines)    │
                                                   └─────────┬─────────┘
                                                             │
                             ┌────────────────────────────────┼────────────────┐
                             ▼                                ▼                ▼
                   ┌─────────────────┐            ┌───────────────┐   ┌────────────┐
                   │  Plugin Manager │            │     CAS       │   │  Bootstrap │
                   │  (stdin/stdout) │            │  (tiered)     │   │  Manager   │
                   └────────┬────────┘            └───────┬───────┘   └────────────┘
                            │                             │
                   ┌────────┼────────┐           ┌────────┼────┐
                   ▼        ▼        ▼           ▼             ▼
                 go.bb  rust.bb  any.exe       disk         OCI reg
```

mu coordinates. Plugins decide what to build and how.

### Core Primitives

- **Artifacts** — content-addressed blobs stored by SHA-256 hash
- **Actions** — hermetic transformations: input artifacts → output artifacts
- **Plugins** — external executables that emit action subgraphs via NDJSON protocol
- **Toolchains** — bootstrapped as content-addressed artifacts (download, verify, extract)

### Design Principles

- **Plugin protocol over built-in rules.** The LSP model applied to builds. Each toolchain is a plugin that emits action graphs; the build system is just the executor.
- **Content-addressed everything.** Universal caching across all languages. Toolchain upgrades are hash changes. Remote cache works automatically.
- **OCI as the cache layer.** Reuses infrastructure every org already operates. Auth, replication, GC, monitoring — all solved.
- **Minimal and composable.** mu is ~7,500 lines of Go. Plugins can be written in any language.

## Installation

```bash
go install github.com/chau/mu/cmd/mu@latest
```

Or build from source:

```bash
git clone https://github.com/chau/mu.git
cd mu
go build -o mu ./cmd/mu
```

Requires Go 1.25+.

## Quick Start

**1. Create a build file** (`BUILD.json`):

```json
{
  "targets": [
    {
      "target": "//hello:greeting",
      "toolchain": "cowsay",
      "sources": ["message.txt"],
      "config": {
        "output": "greeting.txt"
      }
    }
  ]
}
```

**2. Point to your plugin** (`mu.json`):

```json
{
  "plugins": [
    {
      "name": "cowsay",
      "command": ["bb", "plugins/cowsay/plugin.bb"]
    }
  ]
}
```

**3. Build:**

```bash
mu build //hello:greeting
```

See [`examples/`](examples/) for working examples including a cowsay transformer and a toolchain bootstrapper.

## Usage

```
mu <command> [arguments]

Commands:
  bootstrap  Bootstrap toolchains (override with MU_BOOTSTRAP)
  build      Build one or more targets
  version    Print the mu version
```

### `mu bootstrap`

```bash
mu bootstrap

Flags:
  --no-cache    Skip cache reads, always re-fetch
  --verbose     Show plugin I/O
```

Bootstraps all toolchains declared in `mu.json`. Downloads, extracts, verifies, and registers each toolchain as content-addressed artifacts.

Set `MU_BOOTSTRAP` to an executable path to use an external bootstrap plugin instead of the built-in logic:

```bash
MU_BOOTSTRAP=plugins/bootstrap/plugin.bb mu bootstrap
```

### `mu build`

```bash
mu build <targets...>

Flags:
  --jobs N      Max parallel actions (default: CPU count)
  --no-cache    Skip cache reads, always rebuild
  --verbose     Show plugin I/O
```

## Plugin Protocol

Plugins are external executables that communicate over NDJSON (newline-delimited JSON) on stdin/stdout. Any language works — Babashka, Go, Python, Rust, a shell script.

A plugin implements two methods:

### `discover`

Returns plugin metadata: name, version, protocol version, and what artifact types it consumes and produces.

```json
← {"method": "discover"}
→ {"name": "go", "version": "0.1.0", "protocol_version": 1,
   "consumes": ["go_source"], "produces": ["executable", "go_library"]}
```

### `plan`

Given a target and its dependency artifacts, returns an action subgraph.

```json
← {"method": "plan", "target": {"name": "//cmd/server", "toolchain": "go",
   "sources": ["main.go"], "config": {"output": "server"}}}
→ {"actions": [{"id": "compile", "command": ["go", "build", "-o", "server", "."],
   "inputs": {"src": "main.go"}, "outputs": ["server"], "env": {}}],
   "declared_outputs": {"executable": "server"}}
```

The coordinator resolves file paths to content digests, merges subgraphs from all targets into a unified DAG, checks the cache, and executes uncached actions in parallel.

### Timeouts

- `discover`: 10 seconds
- `plan`: 5 minutes

## Caching

All artifacts are stored by their SHA-256 content hash.

**Local cache:** `~/.mu/cache/blobs/sha256/<prefix>/<hash>`

**OCI remote cache:** Push/pull blobs and action results to any OCI-compliant registry. Supports read-repair (promote from remote to local on access) and write-through.

An action's cache key is derived from:
- The command
- Sorted input digests
- Environment variables

If the key matches, the action is skipped and outputs are restored from cache.

## Toolchain Bootstrap

mu manages toolchain downloads as part of the build:

```json
{
  "target": "//tools:jq",
  "toolchain": "bootstrap",
  "config": {
    "version": "1.7.1",
    "url": "https://github.com/jqlang/jq/releases/download/jq-1.7.1/jq-macos-arm64",
    "sha256": "0bbe619e663e0de2c550be2fe0d240d076799d6f8a652b70fa04aea8a8362e8a"
  }
}
```

Downloads are verified against the declared SHA-256 hash. Extracted binaries are registered in a toolchain registry and stored as content-addressed artifacts. Downstream targets reference bootstrapped toolchains automatically.

## Config Formats

mu natively reads JSON (`BUILD.json`, `mu.json`). For other formats (CUE, TOML, YAML), declare an external preprocessor:

```json
{
  "preprocessor": {
    "command": ["cue", "export", "--out", "json"]
  }
}
```

mu pipes the file through the preprocessor and consumes the JSON output.

## Project Structure

```
cmd/mu/              CLI entry point
internal/
├── cas/             Content-addressed store interface
│   ├── disk/        Local disk backend
│   └── oci/         OCI registry backend
├── dag/             DAG construction, topological sort, parallel executor
├── plugin/          Plugin lifecycle, NDJSON protocol, process management
├── config/          Config loading, validation, preprocessor dispatch
├── coordinator/     Build orchestration pipeline
├── bootstrap/       Toolchain download, verify, extract, register
└── builtin/         Built-in fetch command with SHA-256 verification
plugins/             Example Babashka plugins
examples/            Example projects
```

## Current Status (v0.1.0)

The build coordinator is functional end-to-end:

- [x] Content-addressed store with local disk backend
- [x] OCI registry cache backend (via oras-go)
- [x] DAG construction with topological sort and cycle detection
- [x] Parallel executor with configurable worker pool
- [x] Plugin lifecycle management (discover, plan)
- [x] NDJSON wire protocol
- [x] Config loading with external preprocessor support
- [x] Toolchain bootstrap (download, verify, extract, register)
- [x] `mu build` command with cache integration
- [x] Cross-toolchain artifact composition

## Roadmap

### Near-term

- [ ] **Service manager** — Docker and host-native runtimes with healthchecks and lifecycle management
- [ ] **File watching & triggers** — Debounced rebuilds on source changes, service restarts
- [ ] **`mu dev` command** — Compose services + triggers into a unified dev experience
- [ ] **Go toolchain plugin** — First-class Go support via GOCACHEPROG bridge to mu's CAS
- [ ] **Tiered cache composition** — Chain local + OCI backends with configurable policies

### Medium-term

- [ ] **Hermetic sandboxing** — Enforce declared inputs/outputs via seccomp, landlock, or sandbox-exec (currently honor system)
- [ ] **Plugin distribution** — Install/update third-party plugins via OCI artifacts or git
- [ ] **Incremental compilation support** — Bridge language-specific caches (Go, Rust) with mu's CAS
- [ ] **`mu clean` / `mu verify`** — Cache management and integrity checking

### Long-term

- [ ] **Remote execution** — Distribute actions to worker pools
- [ ] **Build file discovery** — Walk directory trees for multi-package monorepo support
- [ ] **Protocol extensions** — Streaming progress, async planning, format negotiation

## Writing a Plugin

A minimal plugin in Bash:

```bash
#!/usr/bin/env bash
while IFS= read -r line; do
  method=$(echo "$line" | jq -r '.method')
  case "$method" in
    discover)
      echo '{"name":"my-plugin","version":"0.1.0","protocol_version":1,"consumes":[],"produces":["text_output"]}'
      ;;
    plan)
      echo '{"actions":[{"id":"run","command":["echo","hello"],"inputs":{},"outputs":["out.txt"],"env":{}}],"declared_outputs":{"text_output":"out.txt"}}'
      ;;
  esac
done
```

Plugins read JSON lines from stdin, dispatch on `method`, and write JSON responses to stdout. That's the entire contract.

## License

TBD
