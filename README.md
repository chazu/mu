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
  --jobs N          Max parallel actions (default: CPU count)
  --no-cache        Skip cache reads, always rebuild
  --plan            Show planned actions without executing
  --emit-manifest   Emit build manifest as JSON to stdout
  --json            Output as JSON
  --verbose         Show plugin I/O
```

`--plan` shows the DAG without executing, useful for debugging. `--emit-manifest` produces a structured JSON manifest documenting what was built, cache hits, and output digests — used by pudl's ACUTE loop to track convergence state.

### `mu observe`

```bash
mu observe <targets...>

Flags:
  --json    Output as JSON
```

Reports the current observed state of each target by sending observe requests to their plugins. Plugins return structured data describing what they see — mu does not make convergence decisions. The observed state is designed for ingestion into pudl's value lattice, where it is compared against desired state to determine drift.

Kit targets (shell targets with deps) aggregate their dependencies' observed state.

```bash
mu observe --json //lint
```

```json
[
  {"target": "//lint/go-vet", "current": {"exit_code": 0, "output": "", "command": "go vet ./..."}},
  {"target": "//lint/gofmt", "current": {"exit_code": 0, "output": "", "command": "gofmt -l ."}},
  {"target": "//lint", "current": {"deps": {"//lint/go-vet": {...}, "//lint/gofmt": {...}}}}
]
```

## Targets

Targets are declared in `mu.json` and describe what to build:

```json
{
  "target": "//cmd/server",
  "toolchain": "go",
  "sources": ["go.mod", "go.sum", "cmd/server/*.go"],
  "config": {"output": "server", "pkg": "./cmd/server"}
}
```

Source paths support glob patterns (`*`, `?`, `[...]`). Globs are expanded at config load time relative to the project root, so `cmd/server/*.go` matches all `.go` files in that directory. Literal (non-glob) paths pass through as-is. Recursive `**` patterns are not currently supported.

### BRICK Classification

Targets can carry optional BRICK metadata for integration with pudl:

```json
{
  "target": "//app/api",
  "toolchain": "k8s",
  "kind": "component",
  "implements": "//interface/app",
  "sources": ["deployment.yaml"],
  "config": {"namespace": "default"}
}
```

- **`kind`** — one of `"relationship"`, `"interface"`, `"component"`, `"kit"`
- **`implements`** — which interface this component satisfies (components only)
- **`deps`** — dependencies on other targets (used by kits to compose blocks)

mu passes these fields through in build manifests but does not enforce them — constraint enforcement is pudl's job via CUE schema validation.

### Interfaces and Contract Enforcement

An interface defines a contract that components must satisfy. In pudl, interfaces are CUE definitions with a `contract` field:

```cue
lint_interface: brick.#Interface & {
    name: "//interface/lint"
    kind: "interface"
    contract: {
        toolchain: "lint"
        config: { command: [...string] }
    }
}
```

Components declare which interface they implement:

```cue
lint_go_vet: brick.#Target & {
    name:       "//lint/go-vet"
    kind:       "component"
    implements: "//interface/lint"
    toolchain:  "lint"
    config: { command: ["go", "vet", "./..."] }
}
```

pudl validates this relationship via `pudl definition validate` — CUE unification checks that every field in the interface's contract is present and compatible in the component. Violations produce specific errors:

```
  FAIL  //lint/bad (implements //interface/lint)
        field "toolchain": conflicting values "lint" and "wrong"
```

mu does not perform this validation. Its role is to execute targets, not enforce contracts. The split: **pudl validates intent, mu executes it.**

## Plugins

Plugins are external executables that tell mu what to build and how. mu itself has no built-in knowledge of any language or tool — plugins provide all of it.

### Plugin Structure

A plugin is a directory containing a `mu.json` with a `plugin` key and at least one build target. The `mu.json` declares how to build the plugin, what files to include, and how to run it:

```json
{
  "plugin": {
    "entrypoint": "plugin.bb",
    "toolchain": "bb",
    "files": ["plugin.bb", "helper.sh"]
  },
  "targets": [
    {
      "target": "build",
      "toolchain": "shell",
      "sources": ["plugin.bb", "helper.sh"],
      "config": {
        "command": ["true"],
        "impure": false
      }
    }
  ]
}
```

**Plugin manifest fields** (`plugin` key):

| Field | Required | Description |
|-------|----------|-------------|
| `entrypoint` | yes | Relative path to the executable within the plugin directory |
| `toolchain` | no | Runtime toolchain needed to execute the plugin (e.g. `"bb"` for Babashka). Omit for compiled binaries. If omitted, inferred from file extension (`.bb` → `bb`) |
| `files` | no | Files to include in the CAS bundle. If omitted, all non-hidden files are included |

**Build targets**: Every plugin declares its own build targets in its `mu.json`. For interpreted plugins (Babashka scripts), the build target can be a no-op (`true`). For compiled plugins (Go, Rust), the build target compiles the binary. mu does not dictate how plugins are built — the plugin author is in control.

When `mu build` runs a plugin's build target, the plugin directory is automatically bundled as a deterministic tar and stored in CAS. The bundle is extracted to `~/.mu/plugins/<name>/` for execution.

### Referencing Plugins

Plugins are declared in the consuming project's `mu.json` `plugins` array. There are four ways to reference a plugin:

**Plugin directory** (preferred) — point `script` at a directory containing a plugin `mu.json`:

```json
{"name": "go", "script": "plugins/go"}
```

**Single file** (legacy) — a single script file, hashed and stored in CAS:

```json
{"name": "go", "script": "plugins/go/plugin.bb"}
```

**Remote file** — fetched by URL with SHA-256 verification, stored in CAS:

```json
{"name": "go", "url": "https://example.com/go-plugin.bb", "sha256": "abc123..."}
```

**Command** — run an arbitrary executable directly (not stored in CAS):

```json
{"name": "go", "command": ["./my-plugin"]}
```

### Building Plugins

Plugin build targets appear in `mu target list` like any other target. Build them individually or use wildcard patterns:

```bash
# Build all plugins
mu build //plugins/...

# Build a single plugin
mu build //plugins/go/build

# Build everything under a prefix (one level)
mu build //plugins/*
```

Building a plugin target bundles the plugin directory into CAS automatically.

### Inspecting Plugins

```bash
# List plugins declared in mu.json
mu plugin list

# List all plugins stored in CAS (across all projects)
mu plugin list --cached

# Start plugins and show their capabilities
mu plugin list --discover
```

```
PLUGIN               DIGEST
go                   sha256:ea33df5f454a
cowsay               sha256:ff96f94da42e
docker               sha256:433d180dbe2e
```

### Plugin Protocol

Plugins communicate over NDJSON (newline-delimited JSON) on stdin/stdout. Any language works — Babashka, Go, Python, Rust, a shell script.

A plugin implements these methods:

**`discover`** (required) — returns plugin metadata:

```json
← {"method": "discover"}
→ {"name": "go", "version": "0.1.0", "protocol_version": 1,
   "consumes": ["go_source"], "produces": ["executable", "go_library"],
   "capabilities": ["discover", "plan", "observe"]}
```

**`plan`** (required) — given a target, returns an action subgraph:

```json
← {"method": "plan", "target": {"name": "//cmd/server", "toolchain": "go",
   "sources": ["main.go"], "config": {"output": "server"}}}
→ {"actions": [{"id": "compile", "command": ["go", "build", "-o", "server", "."],
   "inputs": {"src": "main.go"}, "outputs": ["server"], "env": {}}],
   "declared_outputs": {"executable": "server"}}
```

The coordinator resolves file paths to content digests, merges subgraphs from all targets into a unified DAG, checks the cache, and executes uncached actions in parallel.

**`observe`** *(optional)* — reports current state of a resource for drift detection:

```json
← {"method": "observe", "target": {...}, "secrets": {"API_KEY": "resolved-value"}}
→ {"current": {"replicas": 3, "image": "nginx:1.25"}}
```

Observe requests include resolved secrets from the target's `sealed_inputs` (see [Sealed Inputs](#sealed-inputs)). The plugin reports the current state; convergence decisions are made downstream by pudl, not by the plugin.

**`resolve_secret`** *(optional)* — resolves a secret reference to its value:

```json
← {"method": "resolve_secret", "secret_ref": "deploy/registry-password"}
→ {"value": "s3cr3t"}
```

Plugins that provide secrets must declare `"resolve_secret"` in their `capabilities` array during discover. See [Sealed Inputs](#sealed-inputs) below.

**Timeouts:** `discover` 10 seconds, `plan` 5 minutes, `observe` 5 minutes, `resolve_secret` 30 seconds.

### Writing a Plugin

A minimal plugin in Bash:

```bash
#!/usr/bin/env bash
while IFS= read -r line; do
  method=$(echo "$line" | jq -r '.method')
  case "$method" in
    discover)
      echo '{"name":"my-plugin","version":"0.1.0","protocol_version":1,"consumes":[],"produces":["text_output"],"capabilities":["discover","plan"]}'
      ;;
    plan)
      echo '{"actions":[{"id":"run","command":["echo","hello"],"inputs":{},"outputs":["out.txt"],"env":{}}],"declared_outputs":{"text_output":"out.txt"}}'
      ;;
  esac
done
```

Plugins read JSON lines from stdin, dispatch on `method`, and write JSON responses to stdout. That's the entire contract.

To package it as a plugin, create a directory with the script and a `mu.json`:

```
my-plugin/
  mu.json       # {"plugin": {"entrypoint": "plugin.sh"}, "targets": [...]}
  plugin.sh     # the script above
```

### Bundled Plugins

| Plugin | Description |
|--------|-------------|
| `go` | Builds Go binaries (cross-compile, tags, ldflags, race) |
| `cowsay` | Demo text transformation |
| `docker` | Docker image builder |
| `file` | File convergence (write, copy, symlink, delete) |
| `k8s` | Kubernetes resource convergence and drift detection |
| `zig` | Zig language toolchain |
| `terraform` | Infrastructure provisioning |
| `scratch` | Toolchain bootstrapping from scratch |
| `lint` | Linter wrapper (observe + fix) |
| `pass` | Secret provider backed by [pass](https://passwordstore.org) |

## Sealed Inputs

Sealed inputs are secret references that are resolved at runtime and never stored in CAS, cache keys, or logs. They are used in two contexts:

### In build actions

A build plugin's `plan` response can include `sealed_inputs` on any action:

```json
{
  "actions": [{
    "id": "deploy",
    "command": ["kubectl", "apply", "-f", "deployment.yaml"],
    "sealed_inputs": {
      "REGISTRY_PASSWORD": "pass:deploy/registry",
      "API_TOKEN": "vault:secrets/api-token"
    }
  }]
}
```

Each value is a reference in the format `scheme:path`, where the scheme maps to a registered plugin name. The coordinator resolves secrets after planning and injects them into the action's environment at execution time.

### In observe targets

Targets can declare `sealed_inputs` directly in `mu.json` for use during observation (e.g., SSH credentials, API keys):

```json
{
  "target": "//home/server",
  "toolchain": "host",
  "config": {"host": "192.168.1.104", "user": "root"},
  "sealed_inputs": {
    "SSH_PASS": "pass:servers/root-password"
  }
}
```

The coordinator resolves these before calling the plugin's `observe` method and passes them as a `secrets` map in the observe request. The plugin uses them to authenticate but never persists them.

### Secret resolution flow

1. Parse each reference's `scheme:path` to identify the secret-provider plugin
2. Call that plugin's `resolve_secret` method with the path
3. Store the resolved values in memory (never on disk)
4. Inject into action environments (build) or observe requests (observe)

### Writing a secret-provider plugin

A secret-provider plugin declares the `resolve_secret` capability and handles the method. The bundled `pass` plugin (`plugins/pass/`) provides an example backed by [password-store](https://passwordstore.org).

Register the plugin in `mu.json` and reference secrets using the provider's name as the scheme prefix (e.g., `"pass:deploy/token"`).

### Security properties

- **Never in CAS.** Secret values are never content-addressed or stored as artifacts.
- **Never in cache keys.** Changing a secret does not invalidate the cache. Actions with sealed inputs can still be cached based on their non-secret inputs.
- **Never on disk.** Resolved values exist only in process memory and the spawned action's environment.
- **Never logged.** Secret values are excluded from `--verbose` output and `--emit-manifest` JSON.
- **Resolved late.** Secrets are resolved after planning but before execution — the value window is as short as possible.

## Caching

All artifacts are stored by their SHA-256 content hash in OCI layout (same format locally and remotely).

**Local cache:** `~/.mu/cache/` (OCI layout directory)

**OCI remote cache:** Push/pull blobs and action results to any OCI-compliant registry.

An action's cache key is derived from:
- The command
- Sorted input digests
- Environment variables
- Network flag

Sealed inputs (secrets) are deliberately excluded from the cache key. If the key matches, the action is skipped and outputs are restored from cache.

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
- [x] Sealed inputs for secret injection (`resolve_secret` protocol, excluded from CAS/cache/logs)

## Roadmap

### Core

- [ ] **Tiered cache composition** — Chain local + OCI backends with read-repair and write-through policies
- [ ] **`mu clean`** — Prune stale artifacts from the local CAS
- [ ] **CLI polish** — Color output, `--verbose` for all commands, consistent `--json` across subcommands

### Build intelligence

- [ ] **GOCACHEPROG bridge** — Fine-grained Go build cache integration with mu's CAS. See [`docs/brainstorms/2026-02-28-go-toolchain-plugin-design.md`](docs/brainstorms/2026-02-28-go-toolchain-plugin-design.md)
- [ ] **Incremental compilation support** — Bridge language-specific caches (Go, Rust) with mu's CAS
- [ ] **OS-level sandboxing** — Linux: user namespaces + overlayfs. macOS: sandbox-exec profiles

### Plugin ecosystem

Proposed plugins and the mu/pudl ownership split are documented in [`docs/brainstorms/2026-03-25-plugin-ideas.md`](docs/brainstorms/2026-03-25-plugin-ideas.md).

- [ ] **Secrets plugins** — `pass` and `op` (1Password) plugins using the `resolve_secret` protocol for secret injection and drift detection
- [ ] **Policy plugin** — OPA/conftest for runtime policy enforcement via observe
- [ ] **Container image plugins** — `buildpack` and `ko` as alternatives to the Docker plugin
- [ ] **Developer standards plugins** — `structure`, `docs`, `convention` for project layout and documentation enforcement

### Infrastructure

- [ ] **Remote execution** — Distribute actions to worker pools
- [ ] **Protocol extensions** — Streaming progress, async planning, format negotiation

### Architecture references

- [Conceptual model](docs/architecture/mu-conceptual-model.md) — mu's primitives, hermeticity model, and execution flow
- [BRICK ecosystem](docs/architecture/brick-ecosystem.md) — how mu and pudl work together (BRICK/IDEA/ACUTE frameworks)
- [BRICK project guide](docs/architecture/brick-project-guide.md) — practical guide to structuring a BRICK project
- [BRICK integration plan](docs/plans/2026-03-24-feat-brick-ecosystem-integration-plan.md) — implementation plan for convergence and observation

## License

TBD
