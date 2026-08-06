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
mu version
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

JSON build result, schema `mu.build.manifest/v1`. Fields: `version`, `type`, `timestamp`, `duration_s`, `targets[]`, `actions[]`, `summary` (completed/cached/failed/cancelled). Each action: `id`, `cached`, `exit_code`, `outputs{name→digest}`. BRICK metadata round-trips. Ingest with `pudl mu ingest-manifest`.

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

## One-shot full converge (observe → apply → re-observe)

```bash
# PUDL owns planning, mu execution, receipt ingestion, and re-observation.
pudl run <model> --converge

# For cross-model values, name the exact closed set.
pudl run-set <producer> <consumer> --converge
```

## Observe-only (drift check without applying)

```bash
pudl run <model>
pudl run-set <producer> <consumer>
```

## Partial converge (specific target)

```bash
pudl run <model> --converge --only myapp
```

## When loop stalls (target keeps drifting after apply)

```bash
pudl run <model> --json                     # fresh observed verdict + binding issues
pudl run report [run-id] --json             # durable phase/report evidence
pudl run-set report [run-set-id]            # exact-set member and binding evidence
mu observe --verbose //target                # direct plugin view when mu.cue owns target
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
pudl mu ingest-manifest --path manifest.json
```

## Iteration and approval

```bash
pudl run <model> --converge --max-iters 5
pudl run-set <models...> --converge --require-approval
pudl run-set resume <run-set-id>   # or reject
```

PUDL re-observes between applies and stops when clean or the cap is reached.
Any mutating run-set with a sealed output requires exact-plan approval even
without `--require-approval`.

## Safety habits

- **Use `--converge --dry-run`** to inspect a single-model mutation plan
- **Use exact run-set approval** when a human must authorize the whole set
- **Inspect `mu cache size`** periodically; cache grows fast on convergence loops
- **Use `pudl facts observe`** to record manual interventions that bypass the loop
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
  sealed_routing:      "strict" // optional: exact action claims, no inheritance

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
  "output_schema": {"module": "mu/plugin", "version": "v1", "definition": "#Type"},
  "output_schemas": [{"resource_type": "plugin.type", "module": "mu/plugin", "version": "v1", "definition": "#Type"}]
}
```

Plugin must: declare every supported method in `capabilities`. Declare `config_schema` so mu can validate target.config before planning.

### `plan` (optional; required for build/convergence plugins, called once per target)

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
    "sealed_routing": "strict"
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

Plugin must: translate target → actions. Consume deps via the `artifacts` map.
Populate `declared_outputs` (artifact-type → file path) so downstream targets
can consume. In convenience mode mu may inherit target sealed maps. In strict
mode, explicitly claim exact refs/modes on the actions that need them; every
target input must be claimed at least once and every output exactly once.
Provider values are not available while planning.

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

Mu is the execution layer; PUDL is the model/observation/approval layer. When a
registered `#SystemModel` is the source of truth, PUDL owns the loop:

```bash
pudl run <model>                              # observe-only
pudl run <model> --converge                   # apply through mu, then verify
pudl run-set <producer> <consumer>            # exact observe-only set
pudl run-set <producer> <consumer> --converge # whole-set preflight, then apply
```

There is no current `pudl drift` or `pudl export-actions` command. PUDL renders
temporary mu configuration, invokes `mu build`/`mu observe`, ingests receipts,
and records durable run reports internally.

## Exact value wiring

Plain scalar bindings come from successful PUDL catalog snapshots. Both the
consumer input and source schema field must declare `@pudl(binding=plain)`.
`run-set` is closed and explicit: it rejects missing producers/cycles, orders
producers first, and pins their observations. It never expands the named set.

Sealed values remain in mu's provider channel. PUDL-generated targets use
`sealed_routing: "strict"`; actions must explicitly claim exact declared refs
and modes. Every input must be claimed at least once and every output exactly
once. Planning validates routing without resolving values. Mu resolves inputs
immediately before execution and re-checks `secrets.writable_refs` before each
write. PUDL stores only redacted fingerprints. Sealed outputs are converge-only,
and any mutating set containing one requires exact-plan approval:

```bash
pudl run-set report [run-set-id]
pudl run-set resume <run-set-id>   # or reject
```

## Direct mu use

When hand-written `mu.cue` is the source of truth, use mu directly:

```bash
mu build --plan //target
mu build --emit-manifest //target > manifest.json
mu observe --json //target > observe.json
pudl mu ingest-manifest --path manifest.json --model <model>
pudl mu ingest-observe --path observe.json
```

Plugins remain ignorant of PUDL: they receive ordinary target/config data and
emit ordinary actions. See `mu guide pudl` for the full current contract.

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

## Strict action routing

```cue
sealed_routing: "strict"
```

Target sealed maps become the complete availability declaration, not implicit
action grants. Each action claims its exact refs and effective modes. All inputs
must be claimed at least once; every output exactly once. Undeclared/unused
names, ref or mode changes, and ambiguous output writers fail planning. Provider
values are resolved only immediately before action execution. PUDL-generated
targets always use strict mode.

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
