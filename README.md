# mu

A language-agnostic build coordinator. mu knows nothing about programming languages, compilers, or toolchains. External plugins emit action subgraphs via a simple protocol, and mu orchestrates them as a unified DAG of content-addressed actions.

The name means "emptiness" in Japanese. The build system has no built-in semantics. Plugins fill it with meaning.

## How It Works

```
                    ┌──────────────┐
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
                   │  Plugin Manager │            │     CAS       │   │  Scratch   │
                   │  (stdin/stdout) │            │  (OCI store)  │   │  Builder   │
                   └────────┬────────┘            └───────────────┘   └────────────┘
                            │
                   ┌────────┼────────┐
                   ▼        ▼        ▼
                 go.bb  rust.bb  any.exe
```

mu coordinates. Plugins decide what to build and how.

### Core Primitives

- **Artifacts** — content-addressed blobs stored by SHA-256 hash
- **Actions** — hermetic transformations: input artifacts → output artifacts
- **Plugins** — external executables that emit action subgraphs via NDJSON protocol
- **Toolchains** — built from scratch as content-addressed artifacts (download, verify, extract)

### Design Principles

- **Plugin protocol over built-in rules.** The LSP model applied to builds. Each toolchain is a plugin that emits action graphs; the build system is just the executor.
- **Content-addressed everything.** Universal caching across all languages. Toolchain upgrades are hash changes. Remote cache works automatically.
- **OCI as the cache layer.** Same OCI layout locally and remotely. Reuses infrastructure every org already operates. Auth, replication, GC, monitoring — all solved.
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

**1. Create `mu.json`:**

```json
{
  "toolchains": [
    {
      "toolchain": "bb",
      "from": "scratch",
      "config": {
        "version": "1.12.216",
        "url": "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-macos-aarch64.tar.gz",
        "sha256": "91499b3f430038f9b40e433215256a6e5392942780dca9984d493d2bcca7055d"
      }
    }
  ],
  "plugins": [
    {"name": "go", "script": "plugins/go/plugin.bb"}
  ],
  "targets": [
    {
      "target": "//cmd/hello",
      "toolchain": "go",
      "sources": ["go.mod", "go.sum", "cmd/hello/main.go"],
      "config": {"output": "hello", "pkg": "./cmd/hello"}
    }
  ]
}
```

**2. Build:**

```bash
mu build //cmd/hello
```

See [`examples/`](examples/) for working examples including a Go build, a cowsay transformer, and a scratch toolchain build.

## Usage

```
mu <command> [arguments]

Commands:
  build      Build one or more targets
  scratch    Build toolchains from scratch (override with MU_SCRATCH)
  cache      Inspect CAS cache contents
  target     List and inspect targets
  plugin     List, inspect, and add plugins
  observe    Check if targets are up-to-date (drift detection)
  verify     Validate CAS blob integrity
  version    Print the mu version
```

### `mu scratch`

```bash
mu scratch

Flags:
  --no-cache    Skip cache reads, always re-fetch
  --verbose     Show plugin I/O
```

Builds all toolchains declared in `mu.json` from scratch. Downloads, extracts, verifies, and registers each toolchain as content-addressed artifacts.

Set `MU_SCRATCH` to an executable path to use an external scratch builder instead of the built-in logic:

```bash
MU_SCRATCH=plugins/scratch/plugin.bb mu scratch
```

### `mu build`

```bash
mu build <targets...>

Flags:
  --jobs N      Max parallel actions (default: CPU count)
  --no-cache    Skip cache reads, always rebuild
  --verbose     Show plugin I/O
```

## Plugins

Plugins are external executables that tell mu what to build and how. mu itself has no built-in knowledge of any language or tool — plugins provide all of it.

### Defining Plugins

Plugins are declared in `mu.json`'s `plugins` array. There are four ways to reference a plugin:

**Local script** — a `.bb` (Babashka) script vendored in the repo:

```json
{"name": "go", "script": "plugins/go/plugin.bb"}
```

**Remote script** — fetched by URL with SHA-256 verification:

```json
{"name": "go", "url": "https://example.com/go-plugin.bb", "sha256": "abc123..."}
```

**CAS digest** — reference a plugin already stored in the content-addressed cache:

```json
{"name": "go", "digest": "sha256:818f0c36b02f946611b674eac0f658de2184e759a2c389f4a6f13d0caa8652ab"}
```

**Command** — run an arbitrary executable (escape hatch, not stored in CAS):

```json
{"name": "go", "command": ["bb", "plugins/go/plugin.bb"]}
```

For script, URL, and digest plugins, mu needs a `bb` toolchain to execute the `.bb` script. Declare one in your `toolchains` array.

### Building and Distributing Plugins

mu includes build targets for all bundled plugins. Each `//plugins/<name>` target hashes the plugin script and stores it in CAS:

```bash
# Build all plugins into CAS
mu build //plugins

# Build a single plugin
mu build //plugins/go
```

List all plugins available in the cache with their digests:

```bash
mu plugin list --cached
```

```
PLUGIN               CACHED  DIGEST
go                   yes     sha256:818f0c36...
cowsay               yes     sha256:b3df9813...
docker               yes     sha256:1fd5b618...
```

Add a cached plugin to your project's `mu.json` by name:

```bash
mu plugin add go
```

This builds `//plugins/go`, extracts the output digest, and writes a `digest`-based entry into `mu.json`. If an entry with the same name already exists, it is replaced.

### Inspecting Plugins

```bash
# List plugins defined in mu.json
mu plugin list

# Start plugins and show their capabilities (requires built toolchains)
mu plugin list --discover

# JSON output
mu plugin list --cached --json
```

### Plugin Protocol

Plugins communicate over NDJSON (newline-delimited JSON) on stdin/stdout. Any language works — Babashka, Go, Python, Rust, a shell script.

A plugin implements two methods:

**`discover`** — returns plugin metadata:

```json
← {"method": "discover"}
→ {"name": "go", "version": "0.1.0", "protocol_version": 1,
   "consumes": ["go_source"], "produces": ["executable", "go_library"]}
```

**`plan`** — given a target, returns an action subgraph:

```json
← {"method": "plan", "target": {"name": "//cmd/server", "toolchain": "go",
   "sources": ["main.go"], "config": {"output": "server"}}}
→ {"actions": [{"id": "compile", "command": ["go", "build", "-o", "server", "."],
   "inputs": {"src": "main.go"}, "outputs": ["server"], "env": {}}],
   "declared_outputs": {"executable": "server"}}
```

The coordinator resolves file paths to content digests, merges subgraphs from all targets into a unified DAG, checks the cache, and executes uncached actions in parallel.

**Timeouts:** `discover` 10 seconds, `plan` 5 minutes.

### Writing a Plugin

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

### Bundled Plugins

| Plugin | Toolchain | Description |
|--------|-----------|-------------|
| `go` | Go | Builds Go binaries (cross-compile, tags, ldflags, race) |
| `cowsay` | Text | Demo text transformation |
| `docker` | Docker | Docker image builder |
| `file` | File | File operations |
| `k8s` | Kubernetes | Kubernetes resource management |
| `zig` | Zig | Zig language toolchain |
| `terraform` | Terraform | Infrastructure provisioning |
| `scratch` | Bootstrap | Toolchain bootstrapping from scratch |

## Caching

All artifacts are stored by their SHA-256 content hash in OCI layout (same format locally and remotely).

**Local cache:** `~/.mu/cache/` (OCI layout directory)

**OCI remote cache:** Push/pull blobs and action results to any OCI-compliant registry.

An action's cache key is derived from:
- The command
- Sorted input digests
- Environment variables

If the key matches, the action is skipped and outputs are restored from cache.

## Toolchains

Toolchains are built from scratch — mu downloads, verifies, extracts, and registers them as content-addressed artifacts:

```json
{
  "toolchains": [
    {
      "toolchain": "go",
      "from": "scratch",
      "config": {
        "version": "1.25.7",
        "url": "https://go.dev/dl/go1.25.7.linux-amd64.tar.gz",
        "sha256": "abc123...",
        "strip_prefix": "go"
      }
    }
  ]
}
```

Downloads are verified against the declared SHA-256 hash. Extracted binaries are registered in a toolchain registry and stored as content-addressed artifacts. Downstream plugins receive toolchain artifacts automatically in plan requests.

## Config Formats

mu natively reads JSON (`mu.json`). For other formats (CUE, TOML, YAML), declare an external preprocessor:

```json
{
  "preprocessor": {
    "extension": "star",
    "command": ["cue", "export", "--out", "json"]
  }
}
```

mu discovers `mu.<ext>` files in subdirectories, pipes them through the preprocessor, and consumes the JSON output.

## Project Structure

```
cmd/mu/              CLI entry point
internal/
├── cas/             Content-addressed store interface
│   └── oci/         OCI layout backend (local + remote)
├── dag/             DAG construction, topological sort, parallel executor
├── plugin/          Plugin lifecycle, NDJSON protocol, process management
├── config/          Config loading, validation, preprocessor dispatch
├── coordinator/     Build orchestration pipeline
├── scratch/         Toolchain download, verify, extract, register
├── sandbox/         Hermetic execution environments
└── builtin/         Built-in fetch command with SHA-256 verification
plugins/             Babashka plugins (go, cowsay, docker, file, k8s, zig, terraform, scratch)
examples/            Example projects
```

## Current Status (v0.1.0)

The build coordinator is functional end-to-end:

- [x] Content-addressed store with OCI layout (local + remote)
- [x] DAG construction with topological sort and cycle detection
- [x] Parallel executor with configurable worker pool
- [x] Sandbox execution environments (copy sandbox)
- [x] Plugin lifecycle management (discover, plan)
- [x] Script-based plugins via bootstrapped bb toolchain
- [x] NDJSON wire protocol
- [x] Config loading with external preprocessor support
- [x] Toolchain scratch builds (download, verify, extract, register)
- [x] Go toolchain plugin (build, cross-compile, tags, ldflags, race)
- [x] `mu build` command with cache integration
- [x] Cross-toolchain artifact composition
- [x] Plugin distribution via CAS digests (`mu plugin add`, `mu plugin list --cached`)

## Roadmap

### Near-term

- [ ] **GOCACHEPROG bridge** — Fine-grained Go build cache integration with mu's CAS
- [ ] **Tiered cache composition** — Chain local + OCI backends with configurable policies
- [ ] **`mu clean` / `mu verify`** — Cache management and integrity checking

### Medium-term

- [ ] **OS-level sandboxing** — Linux: user namespaces + overlayfs. macOS: sandbox-exec profiles
- [ ] **Incremental compilation support** — Bridge language-specific caches (Go, Rust) with mu's CAS

### Long-term

- [ ] **Remote execution** — Distribute actions to worker pools
- [ ] **Protocol extensions** — Streaming progress, async planning, format negotiation

## License

TBD
