# Agent Instructions

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work
bd sync               # Sync with git
```

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds

## Build config format

Project build configs are authored in CUE (`mu.cue`). See
[`docs/cue-conventions.md`](docs/cue-conventions.md) for the full reference.

- Minimum CUE version: **`v0.11.0`** (recommended `>= v0.13.0`).
- Every project root needs a `cue.mod/module.cue` pinning `language.version`.
- Scripts live under `mu/scripts/`; shared CUE fragments under `mu/config/`
  (imported as packages, not `@embed`ed).
- Use `@embed` only for small (< 1 KB) literal blobs; reference larger content
  as sibling files so the loader can hash it into the action cache key.
- Run `cue vet` and `mu validate` before committing config changes.

<!-- BEGIN union:personal:tools/mu/intro -->
# Mu — when to use it

Mu = language-agnostic, plugin-driven build coordinator. NOT `go build` / `cargo` / `make`. Knows nothing about languages, compilers, toolchains. External plugins emit action subgraphs over NDJSON; mu orchestrates them as one DAG of content-addressed actions and executes hermetically.

## Reach for mu when this repo needs

- Hermetic multi-language builds with a shared cache (Go + Zig + Docker + shell, one DAG)
- Drift-converge against live infrastructure (k8s, terraform, files) — pairs with pudl for the full ACUTE loop
- BRICK-composed systems (interface/component/kit modeling) — pudl validates, mu executes
- Sandbox guarantees (Linux namespaces / macOS Seatbelt / copy isolation)
- OCI-layout artifact distribution (push/pull builds across machines)

## Do NOT use mu for

- Single-language inner-dev-loop where the native tool is already fast (just use `go test ./...`)
- Stateless ephemeral builds with no infra side effects
- Workflows that need imperative control flow (mu plans static DAGs)

## Bootstrap pointers

```
mu --version
mu guide                 # topic index — authoritative
mu guide overview        # mental model
mu guide pudl            # how mu pairs with pudl
```

`mu guide <topic>` is the single source of truth. When these clauses disagree with `mu guide`, trust the guide.
<!-- END union:personal:tools/mu/intro -->

<!-- BEGIN union:personal:tools/mu/concepts -->
# Mu core concepts

## Five primitives

1. **Artifact** — content-addressed blob (SHA-256). Immutable. Stored in OCI layout at `~/.mu/cache/`. Distributable via OCI registry push/pull.
2. **Action** — hermetic transformation: input artifacts → output artifacts. Fields: `id`, `command`, `inputs` (name→digest), `outputs` (paths), `depends_on`, `env`, `network`, `impure`, `timeout_s`, `retries`, `sealed_inputs/outputs`. Cached by input hash if pure.
3. **Plugin** — external executable speaking NDJSON. Emits action subgraphs in response to `plan` requests.
4. **Target** — declared build unit in `mu.cue`. Has `target` (name like `//cmd/app`), `toolchain`, `sources`, `deps`, `config`, optional BRICK metadata (`kind`, `implements`), optional inline `plan`/`transform` pith programs.
5. **Toolchain** — special target with `from: "scratch"`. mu downloads, verifies SHA-256, extracts, registers files as content-addressed artifacts. Passed to downstream plugins via `toolchain_artifacts`.

## DAG model

Adjacency-list graph (`dag.Graph`). Topologically sorted. Workers execute in parallel up to `--jobs`. Each action's outputs become inputs to downstream actions; mu threads digests through the graph automatically.

## Pure vs impure

- **Pure** (default): same inputs → same outputs. Cached by input hash. Use for compilation, codegen.
- **Impure** (`impure: true`): external side effects. Never cached, always runs. Use for convergence (k8s apply, terraform apply, file writes). Forced when action has `sealed_outputs`.

## Manifest

JSON build result, schema `mu.build.manifest/v1`. Fields: `version`, `type`, `timestamp`, `duration_s`, `targets[]`, `actions[]`, `summary` (completed/cached/failed/cancelled). Each action: `id`, `cached`, `exit_code`, `outputs{name→digest}`. BRICK metadata round-trips. Consumed by `pudl import`.

## Where things live

```
~/.mu/cache/        # OCI layout, content-addressed
~/.mu/plugins/      # extracted plugins (after CAS bundle build)
<repo>/mu.cue       # root config (auto-discovered by walking up)
<repo>/**/mu.cue    # sub-configs (auto-merged)
```

Sandbox model: see `personal:tools/mu/sandbox-caching`.
<!-- END union:personal:tools/mu/concepts -->

<!-- BEGIN union:personal:tools/mu/converge-recipes -->
# Converge recipes — copy-paste ACUTE loop

## One-shot full converge (drift → apply → re-observe)

```bash
# T: export drifted resources as mu targets
pudl export-actions --drifted > /tmp/converge.json

# Safety: plan first, inspect what would happen
mu build --plan --config /tmp/converge.json //...

# E: execute convergence + emit manifest
mu build --emit-manifest --config /tmp/converge.json //... > /tmp/manifest.json

# A: ingest manifest back into pudl
pudl import /tmp/manifest.json --origin mu

# A: re-observe to confirm convergence
mu observe --json --config /tmp/converge.json //... | pudl import --origin mu
```

## Observe-only (drift check without applying)

```bash
mu observe --json --config /tmp/converge.json //... | pudl import --origin mu
pudl facts list --relation drift
```

## Partial converge (specific target)

```bash
pudl export-actions --drifted --target //k8s/myapp > /tmp/c.json
mu build --plan --config /tmp/c.json //k8s/myapp
mu build --emit-manifest --config /tmp/c.json //k8s/myapp > /tmp/m.json
pudl import /tmp/m.json --origin mu
```

## When loop stalls (target keeps drifting after apply)

```bash
pudl drift                                  # see what's still drifted
pudl facts list --relation drift            # full drift history
pudl facts show <drift-id>                  # specific drift record
mu observe --verbose --config /tmp/c.json //target  # what plugin actually sees
```

Common causes:
- Plugin reports state in a different shape than pudl expects (`_schema` mismatch)
- Definition unintentionally non-idempotent (timestamps in config)
- External actor mutating resource between observe and apply

## Plan diff between iterations

```bash
mu build --plan --json --config /tmp/c1.json //... > /tmp/plan1.json
# ... after a change ...
mu build --plan --json --config /tmp/c2.json //... > /tmp/plan2.json
diff <(jq -S . /tmp/plan1.json) <(jq -S . /tmp/plan2.json)
```

## CI-style: build + push cache + emit manifest

```bash
mu build --emit-manifest //... > manifest.json
mu cache push ghcr.io/org/mu-cache
pudl import manifest.json --origin mu --source ci
```

## Loop with observation between iterations (slow but safest)

```bash
while true; do
  pudl export-actions --drifted > /tmp/c.json
  count=$(jq '.targets | length' /tmp/c.json)
  [ "$count" = "0" ] && break
  mu build --emit-manifest --config /tmp/c.json //... > /tmp/m.json
  pudl import /tmp/m.json --origin mu
  mu observe --json --config /tmp/c.json //... | pudl import --origin mu
done
```

Stops when no drift remains.

## Safety habits

- **Always `--plan` before applying** on convergence targets (anything impure)
- **Pass `--source mu` or `--source ci`** on pudl imports so attribution is preserved
- **Inspect `mu cache size`** periodically; cache grows fast on convergence loops
- **Use `pudl observe` (the `kind=fact`) to record manual interventions** that bypass the loop, so future drift detection sees them
<!-- END union:personal:tools/mu/converge-recipes -->

<!-- BEGIN union:personal:tools/mu/cue-config -->
# mu.cue config schema

Root config, auto-discovered by walking up from cwd. Subdirectory `mu.cue` files auto-merged.

## Top-level fields

```cue
package mu

toolchains:   [...]   # scratch-built tool downloads
plugins:      [...]   # external plugin registrations
targets:      [...]   # declared build units
cache:        {...}   # OCI cache config (backends, read_repair, write_through, push)
secrets:      {...}   # write-policy (writable_refs: ["pass:bootstrap/*", ...])
advice:       [...]   # lifecycle observer plugins (advise method)
preprocessor: {...}   # transform non-JSON config (extension, command)
```

## Toolchain entry

```cue
{
  toolchain: "name"
  from:      "scratch"
  config: {
    version:      "x.y.z"
    url:          "https://..."
    sha256:       "..."
    strip_prefix: "optional/leading/dir"
  }
}
```

mu downloads, verifies, extracts, registers files as artifacts. Available to plugins as `toolchain_artifacts` in plan requests.

**Implicit contracts** (else `mu scratch` fails):
- Archive: `.tar.gz` / `.tgz` / `.tar.xz` / `.txz` / `.zip` (else treated as raw binary)
- After extract + `strip_prefix`, binary must exist at `bin/<toolchain>` or `<toolchain>`
- Binary must respond to `version` or `--version` with exit 0
- Pre-compiled only — no post-extract build step. Source-only? Use `MU_SCRATCH=<command>` to delegate.

See `mu guide toolchains` for full failure-mode table.

## Plugin entry

One of:
```cue
{ name: "go", script:  "plugins/go/plugin.bb" }   # local file
{ name: "k8s", url:    "oci://...", digest: "sha256:..." }   # remote OCI
{ name: "shell", command: ["sh", "-c", "..."] }   # inline
```

## Target entry

```cue
{
  target:    "//path/name"        // unique within project
  toolchain: "plugin-name"
  sources:   ["glob/**", "file.go"]
  deps:      ["//other:target"]
  config:    { ... }              // plugin-specific, validated against config_schema
  sealed_inputs:       { NAME: "scheme:path" }
  sealed_input_modes:  { NAME: "env" | "file" }
  sealed_outputs:      { NAME: "scheme:path" }
  sealed_output_modes: { NAME: "create" | "overwrite" | "create_if_absent" }

  // BRICK metadata (used by pudl, ignored by mu execution)
  kind:       "relationship" | "interface" | "component" | "kit"
  implements: "//interface/name"

  // Optional inline pith VM programs (replace plugin dispatch)
  plan:      "..."   // pith program emits actions
  transform: "..."   // runs after deps complete
}
```

## Config validation

mu sends `discover` to plugins at startup, captures their `config_schema` (JSON-Schema Draft-7 subset), and validates each target's `config{}` block at planning time. Errors caught before any action runs.

## Naming conventions

- Target names: `//<path>/<name>` (Bazel-style)
- Wildcard: `//...` (all targets recursively), `//pkg:...` (all in package)
- Plugin names: lowercase, hyphens (`remote-exec`, not `RemoteExec`)
<!-- END union:personal:tools/mu/cue-config -->

<!-- BEGIN union:personal:tools/mu/plugin-protocol -->
# Mu plugin protocol (NDJSON wire format)

Plugins read JSON requests from stdin (one per line) and write JSON responses to stdout (one per line). mu starts the plugin, sends `discover`, then sends `plan`/`observe`/etc. as needed. Plugin exits when stdin closes.

Errors at any point: respond with `{"error": "message"}`.

## Request envelope (unified)

```json
{
  "method": "discover|plan|observe|resolve_secret|store_secret|advise",
  "target": {...},
  "deps": [...],
  "toolchain_artifacts": {...},
  "secrets": {...},
  "secret_ref": "...",
  "secret_value": "...",
  "secret_mode": "create|overwrite|create_if_absent",
  "phase": "after-build",
  "manifest": {...},
  "advise_context": {...},
  "advise_config": {...}
}
```

## Six methods

### `discover` (required, called once at startup)

**Request:** `{"method": "discover"}`

**Response:**
```json
{
  "name": "plugin_name",
  "version": "0.1.0",
  "protocol_version": 1,
  "description": "one-line summary",
  "consumes": ["artifact:type"],
  "produces": ["artifact:type"],
  "capabilities": ["discover", "plan", "observe", "resolve_secret", "store_secret", "advise"],
  "config_schema": { ... JSON-Schema Draft-7 subset ... },
  "advise_phases": ["after-build"],
  "output_schema": {"module": "mu/plugin", "version": "v1", "definition": "#Type"}
}
```

Plugin must: declare every supported method in `capabilities`. Declare `config_schema` so mu can validate target.config before planning.

### `plan` (required, called once per target)

**Request:**
```json
{
  "method": "plan",
  "target": {
    "name": "//app",
    "toolchain": "go",
    "sources": [...],
    "config": {...},
    "sealed_inputs": {"NAME": "scheme:path"},
    "sealed_input_modes": {"NAME": "env" | "file"},
    "sealed_outputs": {"NAME": "scheme:path"}
  },
  "deps": [{"target": "//lib", "artifacts": {"binary": "lib.a"}}],
  "toolchain_artifacts": {"go": "/path/to/go"}
}
```

**Response:**
```json
{
  "actions": [{
    "id": "compile",
    "command": ["go", "build", "-o", "app"],
    "inputs": {"main.go": "main.go"},
    "outputs": ["app"],
    "depends_on": [],
    "env": {"GOOS": "linux"},
    "sealed_inputs": {"SSH_KEY": "pass:hosts/id"},
    "sealed_input_modes": {"SSH_KEY": "file"},
    "sealed_outputs": {"TOKEN": "pass:bootstrap/token"},
    "sealed_output_modes": {"TOKEN": "create_if_absent"},
    "network": false,
    "work_dir": ".",
    "impure": false,
    "timeout_s": 300,
    "retries": 3,
    "retry_backoff_ms": 100
  }],
  "declared_outputs": {"binary": "app"}
}
```

Plugin must: translate target → actions. Forward `sealed_inputs/outputs` from target onto actions that need them. Consume deps via the `artifacts` map. Populate `declared_outputs` (artifact-type → file path) so downstream targets can consume.

### `observe` (optional — drift detection)

**Request:**
```json
{
  "method": "observe",
  "target": {...},
  "secrets": {"AWS_SECRET_KEY": "<resolved>"},
  "toolchain_artifacts": {...}
}
```

**Response:**
```json
{
  "current": {
    "records": [
      {"_schema": "aws.ec2.instance", "instance_id": "i-abc", "state": "running"}
    ]
  }
}
```

Plugin must: query real system. Tag each record with `_schema` so pudl routes it correctly. Declare `"observe"` in capabilities.

### `resolve_secret` (optional — secret provider read side)

**Request:** `{"method": "resolve_secret", "secret_ref": "deploy/token"}`

**Response:** `{"value": "secret-bytes"}` or `{"error": "not found"}`

Plugin must: never log the value. Honor any ref grammar variants (e.g. `pass:` first-line vs `pass:raw:` full content). Document grammar in GUIDE.md.

### `store_secret` (optional — secret provider write side)

**Request:**
```json
{
  "method": "store_secret",
  "secret_ref": "deploy/token",
  "secret_value": "bytes",
  "secret_mode": "create" | "overwrite" | "create_if_absent"
}
```

**Response:** `{}` or `{"error": "ref exists"}`

Plugin must: implement all three modes. `create_if_absent` no-ops silently if ref exists.

### `advise` (optional — lifecycle observer)

**Request:**
```json
{
  "method": "advise",
  "phase": "after-build",
  "manifest": {full manifest},
  "advise_context": {
    "project_root": "...",
    "targets": ["//app"],
    "duration_s": 12.3,
    "git_sha": "abc123",
    "git_branch": "main",
    "git_dirty": false
  },
  "advise_config": {...},
  "secrets": {resolved sealed_inputs}
}
```

**Response:** `{"ok": true}` or `{"error": "..."}`

Plugin must: declare `advise_phases` in discover. Errors are non-fatal (logged, build proceeds). 30s timeout.

## Action shape — full field reference

| field | type | notes |
|-------|------|-------|
| `id` | string | unique within subgraph |
| `command` | []string | argv |
| `inputs` | map | name → path or `{action:other-id}` ref |
| `outputs` | []string | declared output file paths |
| `depends_on` | []string | intra-subgraph action IDs |
| `env` | map | full environment (no inheritance) |
| `sealed_inputs/outputs` | map | secret refs (never cached) |
| `sealed_input_modes` | map | `env` or `file` |
| `sealed_output_modes` | map | `create` / `overwrite` / `create_if_absent` |
| `network` | bool | grants net access in sandbox |
| `work_dir` | string | relative to project root |
| `impure` | bool | skip cache; forced true if sealed_outputs present |
| `timeout_s` | int | per-attempt; 0 = none |
| `retries` | int | extra attempts; only when network: true |
| `retry_backoff_ms` | int | between attempts |

## Design contracts

1. **Determinism** — plan response = pure function of request
2. **No post-construction DAG mutation** — plugins are planners + sensors, not orchestrators
3. **Secret hygiene** — values never in cache/manifest/logs; refs+modes are cache-key metadata
4. **Cross-target wiring** — producer's `declared_outputs` ↔ consumer's `deps[].artifacts`
5. **`sealed_outputs` forces `impure: true`** — store_secret side effect must always fire
<!-- END union:personal:tools/mu/plugin-protocol -->

<!-- BEGIN union:personal:tools/mu/pudl-integration -->
# Mu ↔ pudl integration

Mu = **Execution** layer. Pudl = **Knowledge** layer (Intention + Definition + Application). This clause covers: IDEA/ACUTE model, BRICK classification, and the 3 JSON docs that flow between tools.

## IDEA — four layers of knowledge

| Layer | Holds | Pudl implementation |
|-------|-------|---------------------|
| **I**ntention | schemas, constraints, policies | `~/.pudl/schema/` (git-tracked CUE repo) |
| **D**efinition | desired-state values | `~/.pudl/schema/definitions/*.cue` |
| **E**xecution | tool-computed results | mu manifests imported as `pudl/mu.#Manifest` |
| **A**pplication | actual live state | raw imported data, bitemporally versioned |

Mu owns **E**. Pudl owns **I**, **D**, **A**.

## ACUTE — five-phase pipeline

| Phase | Action | Owner |
|-------|--------|-------|
| **A**ccumulate | import actual state from live systems | pudl (`pudl import`, `mu observe \| pudl import`) |
| **C**onfigure | normalize imported data, resolve naming | pudl |
| **U**nify | compare desired vs actual, detect drift | pudl (CUE unification) |
| **T**ransform | export drifted resources as convergence targets | pudl (`pudl export-actions --drifted`) |
| **E**xecute | run convergence actions, report results | mu (`mu build --emit-manifest`) |

Loop closes when manifest re-ingested → re-observe → next iteration.

## The loop

```
   ┌──────────────────────────────────────────────────────────┐
   ▼                                                          │
[pudl: Application layer]                                     │
       │ U: pudl unifies with Definition layer                │
       ▼                                                      │
[pudl: drift detected]                                        │
       │ T: pudl export-actions --drifted                     │
       ▼                                                      │
[mu.json — desired targets]                                   │
       │ E: mu build --emit-manifest                          │
       ▼                                                      │
[manifest.json — Execution layer]                             │
       │ A: pudl import manifest + mu observe                 │
       ▼                                                      │
[pudl: refreshed Application layer] ──────────────────────────┘
```

## BRICK — composable infrastructure blocks

Classification system for infra/code blocks. Pudl validates BRICK constraints via CUE unification; mu executes resulting actions. **Mu does NOT enforce BRICK — pudl does.**

### Four kinds

| kind | what it is |
|------|-----------|
| `relationship` | typed link between two blocks (depends-on, exposes, owns) |
| `interface` | contract — fields + constraints other blocks must satisfy |
| `component` | concrete implementation claiming to satisfy an interface |
| `kit` | curated bundle of interfaces + components + relationships |

Components carry `implements: "//interface/name"`. Pudl validates via CUE unification at planning time.

### Five registers

| Register | pudl side | mu side |
|----------|-----------|---------|
| Building block | CUE definition | Target in mu.cue |
| Role | CUE schema type (interface, component, kit) | Toolchain name |
| Implementation | (delegates to mu) | Plugin |
| Configuration | CUE values, constraints | `config{}` map on target |
| Kit | CUE package / workspace | mu.cue file + plugins/ dir |

### When BRICK matters

Use BRICK kinds when:
- Multiple components could satisfy same contract (swap implementations without rewriting consumers)
- Want pudl to surface contract violations as drift

Skip when: one-off resources with no contract; pure build targets (Go binaries, Docker images) — leave `kind` unset.

## Three JSON docs

### 1. `mu.json` (pudl → mu) — desired state

```
pudl export-actions --drifted > /tmp/converge.json
```

Pudl maps CUE schema prefixes to mu toolchain names:

| CUE prefix | mu toolchain |
|------------|--------------|
| `file.*`, `config.*` | `file` |
| `k8s.*`, `kubernetes.*` | `k8s` |
| `terraform.*`, `tf.*` | `terraform` |
| `shell.*`, `exec.*` | `shell` |
| (unknown) | `generic` |

Consumed via `mu build --config <file> //...`.

### 2. Manifest (mu → pudl) — execution result

```
mu build --emit-manifest --config /tmp/converge.json //... > /tmp/manifest.json
pudl import /tmp/manifest.json --origin mu
```

Schema `mu.build.manifest/v1`. Auto-matches `pudl/mu.#Manifest`. BRICK metadata (kind, implements) round-trips.

### 3. Observe (mu → pudl, fast path) — current state

```
mu observe --json --config /tmp/converge.json //... | pudl import --origin mu
```

Plugin's `observe` returns `current.records[]` with `_schema` fields — pudl routes them.

## Design principle

Pudl emits **desired state**, not drift diffs. File plugin receives `{"path": "...", "content": "..."}` — knows nothing about CUE, drift, or pudl. **Any mu plugin works whether the target came from pudl or hand-written `mu.cue`.**

## What each tool answers

**Pudl**: what should exist, what does exist, what changed and when, where contracts violated, what needs to converge.

**Mu**: what happened in last build (manifest), what is cached vs needs rebuild, what plugins reported as current state (observe).

## When you need the full loop

- Live infrastructure that drifts (cloud, k8s, files)
- Multi-step convergence depending on observed state
- Audit trail of every change

## When you don't

- Pure builds, no live state → just `mu build`
- Read-only analysis of repo → just `pudl import`

<!-- END union:personal:tools/mu/pudl-integration -->

<!-- BEGIN union:personal:tools/mu/secrets -->
# Mu secrets — sealed_inputs & sealed_outputs

Sealed I/O routes secret values through provider plugins. Values **never** enter the cache, manifest, or logs. Refs and modes ARE part of the cache key (so they affect re-execution decisions).

## Reading secrets — `sealed_inputs`

Per-target or per-action:

```cue
sealed_inputs: {
  KUBECONFIG_TOKEN: "pass:deploy/k8s-token"
  AWS_SECRET_KEY:   "pass:aws/secret-key"
}
sealed_input_modes: {
  KUBECONFIG_TOKEN: "env"   // exported as $KUBECONFIG_TOKEN
  AWS_SECRET_KEY:   "file"  // path written to $AWS_SECRET_KEY
}
```

mu calls provider's `resolve_secret` before action runs, injects per the mode.

## Writing secrets — `sealed_outputs`

```cue
sealed_outputs: {
  ADMIN_PASS: "pass:registry/admin"
}
sealed_output_modes: {
  ADMIN_PASS: "create_if_absent"   // also: create, overwrite
}
config: {
  command: ["sh", "-c", "openssl rand -base64 24 > $MU_SEALED_OUT_DIR/ADMIN_PASS"]
}
```

Action writes to `$MU_SEALED_OUT_DIR/<NAME>`; mu routes through provider's `store_secret`. `sealed_outputs` forces `impure: true` (never cached — store side effect must fire).

## Ref scheme grammar

`<provider>:<path>` — e.g. `pass:foo/bar`, `sops:secrets.yaml#key`. Provider-specific. Some support variants (`pass:` first-line vs `pass:raw:` full content).

## Project write policy

```cue
secrets: {
  writable_refs: [
    "pass:bootstrap/*",
    "sops:secrets/generated.yaml#*"
  ]
}
```

mu rejects `sealed_outputs` whose refs don't match a glob. Prevents accidental writes outside designated paths.

## secret-gen toolchain (built-in)

Mints secrets from derivation commands without a plugin binary:

```cue
{
  target:    "//secrets/api-key"
  toolchain: "secret-gen"
  sealed_outputs: { OUT: "pass:api/key" }
  sealed_output_modes: { OUT: "create_if_absent" }
  config: {
    derivation: ["openssl", "rand", "-hex", "32"]
  }
}
```

Idempotent with `create_if_absent` — won't regenerate if already present.

## Providers (bundled)

- `pass` — password-store
- `sops` — Mozilla SOPS encrypted files
- `keypair-gen` — ed25519/ECDSA keypairs

## Authoring a new provider

Implement `resolve_secret` + `store_secret` together. Declare both in `capabilities`. See `personal:tools/mu/plugin-protocol` and `mu guide secret-providers`.

<!-- END union:personal:tools/mu/secrets -->

<!-- BEGIN union:personal:tools/mu/workflow -->
# Mu typical workflow

## From zero to first build

### 1. Write `mu.cue` at repo root

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

plugins: [{ name: "go", script: "plugins/go/plugin.bb" }]

targets: [{
  target:    "//cmd/hello"
  toolchain: "go"
  sources:   ["go.mod", "go.sum", "cmd/hello/main.go"]
  config:    { output: "hello", pkg: "./cmd/hello" }
}]
```

### 2. Bootstrap toolchains

```
mu scratch
```

Fetches URLs, verifies SHA-256, extracts, registers as artifacts. Once per machine + toolchain version.

### 3. Plan first (always, on convergence targets)

```
mu build --plan //cmd/hello
```

Shows DAG without executing. Use as safety check before any impure action.

### 4. Build

```
mu build //cmd/hello
```

Plugin starts → mu sends `discover` then `plan` for each target → DAG executes in parallel → outputs cached by input hash.

### 5. Inspect outputs

```
mu cache ls                       # list cached blobs
mu cache inspect <digest>         # blob metadata
mu graph //cmd/hello              # DAG view
mu graph //cmd/hello --reverse    # what depends on this
```

### 6. Emit manifest (when feeding pudl)

```
mu build --emit-manifest //cmd/hello > /tmp/manifest.json
```

JSON manifest with action outcomes, output digests, BRICK metadata. See `personal:tools/mu/pudl-integration`.

## Iterative tips

- Use `--no-cache` to force rebuild during plugin development
- Use `--verbose` to see plugin NDJSON I/O
- Use `--no-discover-cache` after editing a plugin's `discover` response
- Sub-`mu.cue` files in subdirectories auto-merge — split large configs
<!-- END union:personal:tools/mu/workflow -->
