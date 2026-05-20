# mu

A language-agnostic build coordinator. mu knows nothing about programming languages, compilers, or toolchains. External plugins emit action subgraphs via a simple protocol, and mu orchestrates them as a unified DAG of content-addressed actions.

The name means "emptiness" in Japanese. The build system has no built-in semantics. Plugins fill it with meaning.

## How It Works

```
                    ┌──────────────┐
  mu.cue ─────────►│  Config      │──── validated config ────► Coordinator
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

**1. Create `mu.cue`:**

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
plugins: [
    {name: "go", script: "plugins/go/plugin.bb"},
]
targets: [{
    target:    "//cmd/hello"
    toolchain: "go"
    sources: ["go.mod", "go.sum", "cmd/hello/main.go"]
    config: {output: "hello", pkg: "./cmd/hello"}
}]
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

Builds all toolchains declared in `mu.cue` from scratch. Downloads, extracts, verifies, and registers each toolchain as content-addressed artifacts.

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
  --json      Output as JSON (array of ObserveResult)
  --ndjson    Output current.records as flat NDJSON (one record per line)
```

Reports the current observed state of each target by sending observe requests to their plugins. Plugins return structured data describing what they see — mu does not make convergence decisions. The observed state is designed for ingestion into pudl's catalog, where it is compared against desired state to determine drift.

Kit targets (shell targets with deps) aggregate their dependencies' observed state.

#### Piping to pudl

The `--json` output is the canonical format for ingestion into pudl:

```bash
mu observe --json //home/odroid | pudl ingest-observe
```

`pudl ingest-observe` reads the JSON array, iterates each target's `current.records`, and stores each record as an individual observe entry. Records with a `_schema` field (e.g. `"linux.host"`) are routed to the corresponding pudl schema (e.g. `pudl/linux.#Host`). Records without `_schema` are stored as `pudl/mu.#ObserveResult`.

Ingest can also run **inside the build graph** as a pudl target that depends on another target's declared outputs — e.g. ingesting a `terraform` target's state in the same `mu build` invocation. See [Cross-target artifacts](#cross-target-artifacts) and [Example: terraform → pudl ingest](#example-terraform--pudl-ingest).

#### Output formats

`--json` preserves target context and is the format pudl expects:

```json
[
  {"target": "//home/odroid", "current": {"records": [
    {"_schema": "linux.host", "hostname": "renge", "kernel": "5.10.0", ...},
    {"_schema": "linux.package", "host": "renge", "name": "acl", ...}
  ]}}
]
```

`--ndjson` flattens `current.records` into one JSON line per record (useful for ad-hoc piping to `jq`, etc., but loses target context):

```
{"_schema":"linux.host","hostname":"renge","kernel":"5.10.0",...}
{"_schema":"linux.package","host":"renge","name":"acl",...}
```

## Targets

Targets are declared in `mu.cue` and describe what to build:

```cue
{
    target:    "//cmd/server"
    toolchain: "go"
    sources: ["go.mod", "go.sum", "cmd/server/*.go"]
    config: {output: "server", pkg: "./cmd/server"}
}
```

Source paths support glob patterns (`*`, `?`, `[...]`). Globs are expanded at config load time relative to the project root, so `cmd/server/*.go` matches all `.go` files in that directory. Literal (non-glob) paths pass through as-is. Recursive `**` patterns are not currently supported.

### Cross-target artifacts

A target can consume artifacts produced by its dependencies. The producing plugin declares what it produces via `declared_outputs` (a map from artifact-type name to a project-relative file path); the consuming plugin sees those entries under `deps[].artifacts` in its plan request and can declare them as inputs.

Producer (`plan` response):

```json
{
  "actions": [
    {"id": "show", "command": ["sh", "-c", "terraform show -json > state.json"],
     "inputs": {}, "outputs": ["infra/vpc/state.json"], "depends_on": ["apply"]}
  ],
  "declared_outputs": {"terraform_state": "infra/vpc/state.json"}
}
```

Consumer (`plan` request, received from mu):

```json
{
  "method": "plan",
  "target": {"name": "//pudl/ingest-vpc", "toolchain": "pudl", ...},
  "deps": [
    {"target": "//infra/vpc",
     "artifacts": {"terraform_state": "infra/vpc/state.json"}}
  ]
}
```

Consumer (`plan` response — declare the path as an input):

```json
{
  "actions": [
    {"id": "ingest", "command": ["pudl", "ingest-terraform", "infra/vpc/state.json"],
     "inputs": {"state": "infra/vpc/state.json"}, "outputs": ["ingested.db"]}
  ],
  "declared_outputs": {"catalog": "ingested.db"}
}
```

When mu resolves the consumer's actions, it detects that `"infra/vpc/state.json"` is a path produced by `//infra/vpc:show` and:

1. Adds an implicit `DependsOn` edge from `//pudl/ingest-vpc:ingest` to `//infra/vpc:show` so the DAG runs the producer first.
2. Stores a zero-digest placeholder for that input (the file doesn't exist at plan time).
3. Runs the producer's action, which materializes `infra/vpc/state.json` on disk.
4. Runs the consumer's action in the same project root — the file is there, so the command works.

Paths in `declared_outputs` are project-relative. Plugins are free to ignore `deps[].artifacts` entirely if they don't consume upstream outputs.

### Example: terraform → pudl ingest

Build infrastructure with the `terraform` plugin and feed its state into pudl in a single build:

```json
{
  "targets": [
    {
      "target": "//infra/vpc",
      "toolchain": "terraform",
      "sources": ["infra/vpc/*.tf"],
      "config": {"dir": "infra/vpc", "auto_approve": true}
    },
    {
      "target": "//pudl/vpc-catalog",
      "toolchain": "pudl",
      "deps": ["//infra/vpc"],
      "config": {"from": "terraform_state"}
    }
  ]
}
```

The `terraform` plugin emits three action types (`init`, `plan`, `apply`) plus a `show` action that writes `state.json` and `outputs.json` via `terraform show -json` and `terraform output -json`. The `show` action declares:

```json
{
  "declared_outputs": {
    "terraform_state":   "infra/vpc/state.json",
    "terraform_outputs": "infra/vpc/outputs.json"
  }
}
```

A pudl target that depends on `//infra/vpc` receives `{"terraform_state": "infra/vpc/state.json", "terraform_outputs": "infra/vpc/outputs.json"}` in `deps[0].artifacts` and can declare either file as an input. `mu build //pudl/vpc-catalog` will run terraform first and pudl second.

Set `"emit_state": false` in the terraform target config to suppress the `show` action when downstream pudl ingestion isn't wanted. With `"auto_approve": false`, `show` runs after `plan` instead of `apply` (state reflects existing infrastructure rather than newly-applied changes).

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

A plugin is a directory containing a `mu.cue` with a `plugin` key and at least one build target. The `mu.cue` declares how to build the plugin, what files to include, and how to run it:

```cue
package mu

plugin: {
    entrypoint: "plugin.bb"
    toolchain:  "bb"
    files: ["plugin.bb", "helper.sh"]
    guide: "GUIDE.md"
}
targets: [{
    target:    "build"
    toolchain: "shell"
    sources: ["plugin.bb", "helper.sh"]
    config: {
        command: ["true"]
        impure: false
    }
}]
```

**Plugin manifest fields** (`plugin` key):

| Field | Required | Description |
|-------|----------|-------------|
| `entrypoint` | yes | Relative path to the executable within the plugin directory |
| `toolchain` | no | Runtime toolchain needed to execute the plugin (e.g. `"bb"` for Babashka). Omit for compiled binaries. If omitted, inferred from file extension (`.bb` → `bb`) |
| `files` | no | Files to include in the CAS bundle. If omitted, all non-hidden files are included |
| `guide` | no | Relative path to a guide file (e.g. `GUIDE.md`). Bundled automatically; surfaced by `mu guide plugin <name>` |

**Build targets**: Every plugin declares its own build targets in its `mu.cue`. For interpreted plugins (Babashka scripts), the build target can be a no-op (`true`). For compiled plugins (Go, Rust), the build target compiles the binary. mu does not dictate how plugins are built — the plugin author is in control.

When `mu build` runs a plugin's build target, the plugin directory is automatically bundled as a deterministic tar and stored in CAS. The bundle is extracted to `~/.mu/plugins/<name>/` for execution.

### Referencing Plugins

Plugins are declared in the consuming project's `mu.cue` `plugins` array. There are four ways to reference a plugin:

**Plugin directory** (preferred) — point `script` at a directory containing a plugin `mu.cue`:

```cue
{name: "go", script: "plugins/go"}
```

**Single file** (legacy) — a single script file, hashed and stored in CAS:

```cue
{name: "go", script: "plugins/go/plugin.bb"}
```

**Remote file** — fetched by URL with SHA-256 verification, stored in CAS:

```cue
{name: "go", url: "https://example.com/go-plugin.bb", sha256: "abc123..."}
```

**CAS digest** — reference a previously built+published plugin by content hash:

```cue
{name: "go", digest: "sha256:abc123..."}
```

**Command** — run an arbitrary executable directly (not stored in CAS):

```cue
{name: "go", command: ["./my-plugin"]}
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
# List plugins declared in mu.cue
mu plugin list

# List all plugins stored in CAS (across all projects)
mu plugin list --cached

# Start plugins and show their capabilities
mu plugin list --discover

# Show capabilities, schemas, digest, and path for one plugin
mu plugin info <name>
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
← {"method": "plan",
   "target": {"name": "//cmd/server", "toolchain": "go",
              "sources": ["main.go"], "config": {"output": "server"}},
   "deps": [
     {"target": "//lib/crypto",
      "artifacts": {"go_library": "lib/crypto/libcrypto.a"}}
   ],
   "toolchain_artifacts": {"sdk": "sha256:..."}}
→ {"actions": [{"id": "compile", "command": ["go", "build", "-o", "server", "."],
   "inputs": {"src": "main.go"}, "outputs": ["server"], "env": {}}],
   "declared_outputs": {"executable": "server"}}
```

The coordinator resolves file paths to content digests, merges subgraphs from all targets into a unified DAG, checks the cache, and executes uncached actions in parallel.

`deps[].artifacts` is a map from artifact-type name → project-relative path, populated from each dep's `declared_outputs`. A plugin that needs a dep's output declares the path as one of its action `inputs` — mu wires an implicit DependsOn edge to the producing action (see [Cross-target artifacts](#cross-target-artifacts)). `toolchain_artifacts` carries the active toolchain's content-addressed artifacts (scratch-built) so the plugin can reference them in commands.

**`observe`** *(optional)* — reports current state of a resource for drift detection:

```json
← {"method": "observe", "target": {...}, "secrets": {"SSH_PASS": "resolved-value"}}
→ {"current": {"records": [
    {"_schema": "linux.host", "hostname": "renge", "kernel": "5.10.0", ...},
    {"_schema": "linux.package", "host": "renge", "name": "acl", ...}
  ]}}
```

Observe requests include resolved secrets from the target's `sealed_inputs` (see [Sealed Inputs](#sealed-inputs)). The plugin reports the current state; convergence decisions are made downstream by pudl, not by the plugin.

**Observe response conventions:**
- `current.records` should be an array of resource instances when the plugin observes multiple resources (e.g. packages, services, users on a host).
- Each record should include a `_schema` field with a `package.resource_type` value (e.g. `"linux.package"`) so pudl can route it to the correct schema (e.g. `pudl/linux.#Package`).
- If a plugin observes a single resource, `current` can be a flat map without `records`.

**`resolve_secret`** *(optional)* — resolves a secret reference to its value:

```json
← {"method": "resolve_secret", "secret_ref": "deploy/registry-password"}
→ {"value": "s3cr3t"}
```

Plugins that provide secrets must declare `"resolve_secret"` in their `capabilities` array during discover. See [Sealed Inputs](#sealed-inputs) below.

**`advise`** *(optional)* — lifecycle observer, called after build phases complete:

```json
← {"method": "advise", "phase": "after-build",
   "manifest": {"targets": [...], "actions": [...], "summary": {...}},
   "advise_context": {"project_root": "/path", "targets": ["//cmd/server"],
                       "duration_s": 12.3, "git_sha": "abc123",
                       "git_branch": "main", "git_dirty": false},
   "advise_config": {"webhook_url": "http://..."},
   "secrets": {"hmac_secret": "resolved-value"}}
→ {"ok": true}
```

Advice is non-fatal — errors are logged but never fail the build. Plugins declare `"advise"` in `capabilities` and `advise_phases` (e.g. `["after-build"]`) during discover. Advice config and sealed inputs are declared in `mu.cue`:

```cue
advice: [{
    plugin: "void"
    phases: ["after-build"]
    config: {webhook_url: "http://void:8080/webhook/ns/repo/mu-build"}
    sealed_inputs: {hmac_secret: "pass:void/webhook-hmac"}
}]
```

**Timeouts:** `discover` 10 seconds, `plan` 5 minutes, `observe` 5 minutes, `resolve_secret` 30 seconds, `advise` 30 seconds.

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

To package it as a plugin, create a directory with the script and a `mu.cue`:

```
my-plugin/
  mu.cue        # plugin: {entrypoint: "plugin.sh", ...}, targets: [...]
  plugin.sh     # the script above
```

### Bundled Plugins

| Plugin | Description |
|--------|-------------|
| `aws` | AWS resource observer (EC2, VPC, subnets) via the AWS CLI |
| `cowsay` | Demo text transformation |
| `docker` | Docker image builder |
| `file` | File convergence (write, copy, symlink, delete) and sealed-output capture |
| `go` | Builds Go binaries (cross-compile, tags, ldflags, race) |
| `host` | Remote host observer over SSH (OS, packages, services, mounts, network) |
| `k8s` | Kubernetes resource convergence, drift detection, and Secret capture into sealed outputs |
| `keypair-gen` | Generates ed25519/ECDSA keypairs into sealed outputs (PRIVATE + PUBLIC) |
| `lint` | Linter wrapper (observe + fix) |
| `pass` | Bidirectional secret provider backed by [pass](https://passwordstore.org) |
| `remote-exec` | Run a command on a remote host via SSH; optional `check` guard, `sudo`, and sealed-output file fetch |
| `remote-file` | Converge a file on a remote host via SSH (bytes, mode, owner) with observe support |
| `scratch` | Toolchain bootstrapping from scratch |
| `sops` | Bidirectional secret provider backed by [SOPS](https://github.com/getsops/sops) |
| `terraform` | Infrastructure provisioning, drift detection, and sensitive-output capture |
| `void` | Build result webhook reporter (advice plugin for [void](https://github.com/chazu/void) integration) |
| `zig` | Zig language toolchain |

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

Targets can declare `sealed_inputs` directly in `mu.cue` for use during observation (e.g., SSH credentials, API keys):

```cue
{
    target:    "//home/server"
    toolchain: "host"
    config: {host: "192.168.1.104", user: "root"}
    sealed_inputs: {
        SSH_PASS: "pass:servers/root-password"
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

A secret-provider plugin declares the `resolve_secret` and/or `store_secret` capabilities and handles the corresponding methods. The bundled `pass` plugin (`plugins/pass/`) provides a bidirectional reference implementation backed by [password-store](https://passwordstore.org); `sops` (`plugins/sops/`) is a second backend over [SOPS](https://github.com/getsops/sops)-encrypted files. See `mu guide secret-providers` for the authoring walkthrough.

Register the plugin in `mu.cue` and reference secrets using the provider's name as the scheme prefix (e.g., `"pass:deploy/token"` or `"sops:secrets/prod.yaml#db.password"`).

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

## Inline Programs (pith VM)

mu embeds [pith](https://github.com/chazu/pith), a concatenative
(stack-based) virtual machine. Programs are JSON arrays of words
stored in CUE — no compiled plugins needed for simple logic.

### Three Integration Points

Targets gain three optional fields, each holding a `pith.#Program`:

**`plan`** — inline planning. The coordinator interprets the program
instead of dispatching to a plugin. `action/emit` collects actions
into the DAG:

```cue
import "github.com/chazu/pith"

targets: [{
    target: "//infra/dns"
    plan: pith.#Program & [
        "target/config",
        "dup", "'record_type", "get", "'A", "eq",
        [
            "dup", "'host", "get", "swap", "'ip", "get",
            {"type": "dns/create-a"}, "merge",
            "action/emit",
        ],
        [
            "dup", "'host", "get", "swap", "'target", "get",
            {"type": "dns/create-cname"}, "merge",
            "action/emit",
        ],
        "if",
    ]
}]
```

**`transform`** — inter-target data reshaping. Runs after dependencies
complete, before own actions. The stack result is automatically
available to subsequent actions via `target/output` under the
`_result` key:

```cue
targets: [{
    target: "//deploy/config"
    depends: ["//infra/vpc", "//infra/db"]
    transform: pith.#Program & [
        "'//infra/vpc", "target/output", "'vpc_id", "get",
        "'//infra/db", "target/output", "'endpoint", "get",
        {"vpc_id": null, "db_endpoint": null},
        "swap", "'db_endpoint", "swap", "set",
        "swap", "'vpc_id", "swap", "set",
    ]
    plan: pith.#Program & [
        // reads transform result:
        "'//deploy/config", "target/output", "'_result", "get",
        {"id": "write-config", "type": "file/write"}, "merge",
        "action/emit",
    ]
}]
```

**`body`** on actions — replaces shell commands. The executor
interprets the program instead of running a subprocess:

```cue
// In a plugin's plan response or inline plan:
{
    id: "fetch-data"
    body: pith.#Program & [
        "'https://api.example.com/data", "http/get",
        "'items", "get",
        ["'status", "get", "'active", "eq"], "filter",
        "format/json",
    ]
}
```

### Phase-Scoped Vocabularies

Each execution phase registers a different set of driver words.
Words unavailable in a phase produce "unknown word" errors:

| Word | Plan | Transform | Execute |
|------|:----:|:---------:|:-------:|
| `action/emit` | yes | | |
| `target/config` | yes | yes | yes |
| `target/output` | | yes | yes |
| `http/get`, `http/post` | | | yes |
| `exec/run`, `exec/shell` | | | yes |
| `cas/store`, `cas/fetch` | | | yes |
| `format/json`, `format/compact` | | | yes |

### Driver Words

**DAG construction (plan phase only):**

```
action/emit     ( spec -- )         Emit ActionSpec into DAG
target/config   ( -- config )       Current target config
```

**Cross-target (transform + execute):**

```
target/output   ( name -- data )    Read dependency outputs
```

**HTTP (execute only):**

```
http/get     ( url -- response )          GET, parse JSON response
http/post    ( url body -- response )     POST JSON, parse response
```

**Process execution (execute only):**

```
exec/run     ( [args] -- stdout )    Run command, parse output
exec/shell   ( cmd -- stdout )       Run shell command, parse output
```

**Content-addressed store (execute only):**

```
cas/store    ( data -- digest )      Store data, return digest
cas/fetch    ( digest -- data )      Fetch data by digest
```

**Formatting (execute only):**

```
format/json      ( value -- string )    Pretty-printed JSON
format/compact   ( value -- string )    Minified JSON
```

### Transform Output Passing

When a transform program completes, its stack result is automatically
stored and made available to the target's subsequent actions. Access
it via `target/output` with your own target name — the result appears
under the `_result` key in the output map.

This eliminates the need to declare file-based outputs for transforms.
The data flows in-memory from the transform to downstream actions.

### When to Use Inline Programs vs Plugins

| Use Case | Inline (pith) | Plugin Binary |
|----------|:---:|:---:|
| API call + transform | yes | |
| Conditional action selection | yes | |
| Data reshaping between targets | yes | |
| Config templating | yes | |
| Go/Rust compilation | | yes |
| Docker build | | yes |
| Terraform apply | | yes |
| Streaming I/O | | yes |
| Persistent state across actions | | yes |

Rule of thumb: if the logic is "call API, transform data, emit
result" — pith program. If it needs a toolchain, long-running
process, or complex I/O — plugin binary.

### Coexistence with Plugins

Pith and the NDJSON plugin protocol coexist. A target can use both:
`toolchain: "go"` dispatches to the Go plugin for compilation, while
a `transform` pith program reshapes output for downstream targets.
Targets with `plan` fields skip plugin dispatch entirely.

### Caching

Pith actions cache identically to shell actions. The cache key is
`hash(canonical(body) + input_digests)`. Programs are deterministic
— same program + same inputs = same outputs. Actions with side effects
(e.g. `http/get`) should be marked `impure: true` to skip caching.

### CUE Validation

Import `pith.#Program` to validate programs at config load time:

```cue
import "github.com/chazu/pith"

plan?: pith.#Program
transform?: pith.#Program
```

Unknown words, malformed ops, and type errors are caught during CUE
evaluation — before the coordinator starts.

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

mu natively reads CUE (`mu.cue`). The root config is required; per-package `mu.cue` files in subdirectories are auto-discovered and merged.

For other formats (TOML, YAML, custom DSLs), declare an external preprocessor at the root that converts `mu.<ext>` files to JSON before they're loaded:

```cue
preprocessor: {
    extension: "toml"
    command: ["yj", "-tj"]
}
```

mu then discovers `mu.<ext>` files in subdirectories, pipes them through the preprocessor, and merges the JSON output.

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
├── sandbox/         Hermetic execution (copy, Seatbelt, namespace isolation)
└── builtin/         Built-in fetch command with SHA-256 verification
plugins/             Bundled plugins (aws, cowsay, docker, file, go, host, k8s, keypair-gen, lint, pass, remote-exec, remote-file, scratch, sops, terraform, void, zig)
examples/            Example projects
```

## Current Status (v0.1.0)

The build coordinator is functional end-to-end:

- [x] Content-addressed store with OCI layout (local + remote)
- [x] DAG construction with topological sort and cycle detection
- [x] Parallel executor with configurable worker pool
- [x] Sandbox execution environments (copy, macOS Seatbelt, Linux namespaces)
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
- [x] Advice protocol for build lifecycle observers (`advise` method, non-fatal)
- [x] Hermetic sandbox isolation (Linux namespaces + macOS Seatbelt, auto-detected)

## Roadmap

### Core

- [ ] **Tiered cache composition** — Chain local + OCI backends with read-repair and write-through policies
- [ ] **`mu clean`** — Prune stale artifacts from the local CAS
- [ ] **CLI polish** — Color output, `--verbose` for all commands, consistent `--json` across subcommands

### Build intelligence

- [ ] **GOCACHEPROG bridge** — Fine-grained Go build cache integration with mu's CAS. See [`docs/brainstorms/2026-02-28-go-toolchain-plugin-design.md`](docs/brainstorms/2026-02-28-go-toolchain-plugin-design.md)
- [ ] **Incremental compilation support** — Bridge language-specific caches (Go, Rust) with mu's CAS
- [x] **OS-level sandboxing** — Linux: user namespaces + pivot_root + PID/network isolation. macOS: sandbox-exec with deny-default SBPL profiles

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
