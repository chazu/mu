# The BRICK Ecosystem: mu + pudl

**Date:** 2026-03-24
**Status:** Working document

## What Is BRICK

BRICK stands for **Building block, Role, Implementation, Configuration, Kit**.
It is a classification system for composable infrastructure artifacts,
originating from the defn project's directory classification system (AIDR
00019). Every artifact in a BRICK ecosystem is a Block — a composable unit
classified by what it does and how it relates to other blocks.

### The Five Registers

Every platform artifact has five registers that BRICK makes explicit:

| Register | Question it answers | Example |
|----------|-------------------|---------|
| **Building block** | What is this thing? | A directory, a target, a module, a resource |
| **Role** | What does it do? | Defines contracts (interface), produces artifacts (component), composes others (kit), validates structure (relationship) |
| **Implementation** | How is it realized? | A Babashka plugin, a Go binary, a CUE schema, a Terraform module |
| **Configuration** | How is it parameterized? | The `config` map on a target, CUE values, tfvars files |
| **Kit** | What collection does it belong to? | A mu.json file, a CUE package, a monorepo workspace |

### The Four Kinds

In the defn project, BRICK classifies every directory with a BUILD.bazel
into one of four kinds:

| Kind | Role | defn example | mu+pudl equivalent |
|------|------|-------------|-------------------|
| **Relationship** | Defines how blocks connect and validate | `manifest/` (structure validation) | mu.json `deps` field, target dependency graph |
| **Interface** | Defines contracts, types, schemas, templates | `interface/app/` (Bazel macros, CUE schemas) | Plugin protocol (discover/plan/observe), CUE schemas in pudl |
| **Component** | Concrete instance that produces artifacts | `app/argocd/` (ArgoCD deployment) | A target in mu.json (`//k8s/api`, `//infra/vpc`) |
| **Kit** | Composes other blocks into a cohesive unit | Root directory (composes all top-level blocks) | A complete mu.json file, a pudl workspace |

### How mu+pudl Map to BRICK

```
BRICK Register    pudl                          mu
──────────────    ────                          ──
Building block    CUE definition                Target in mu.json
Role              CUE schema type               Toolchain name (k8s, terraform, file, shell)
Implementation    (delegates to mu)             Plugin (plugins/k8s/plugin.bb)
Configuration     CUE values, constraints       config: {} map on each target
Kit               CUE package / workspace       mu.json file + plugins directory
```

The key insight: pudl owns the **Building block** and **Configuration**
registers (desired state in CUE), while mu owns the **Implementation**
register (plugins that know how to converge). The **Role** register is shared
— pudl maps CUE schema types to mu toolchain names. The **Kit** register
spans both tools — a complete deployment is a pudl workspace that generates
mu.json files.

### BRICK in the defn Project

The defn project implements BRICK as a CUE-based classification system:

```cue
// schema/brick.cue
#BrickKind: "relationship" | "interface" | "component" | "kit"

#Brick: {
    path:        string
    kind:        #BrickKind
    desc?:       string
    composes?:   [...string]     // only kit bricks
    implements?: string          // only component bricks
}
```

Components declare which interface they implement:
```cue
// catalog/brick-app--argocd.cue
bricks: "app/argocd": {
    path:       "app/argocd"
    kind:       "component"
    desc:       "ArgoCD GitOps controller"
    implements: "interface/app"
}
```

Interfaces define contracts that components fulfill. Some interfaces are
"Midas" interfaces — they can stamp out new components from templates:
```cue
// catalog/brick-interface--app.cue
bricks: "interface/app": {
    path:        "interface/app"
    kind:        "interface"
    desc:        "app definition contract and Bazel macros"
    midas:       true
    stamping:    "macro"
    catalog_key: "apps"
}
```

Kits compose blocks into cohesive units. The root kit composes everything:
```cue
bricks: "": {
    path: ""
    kind: "kit"
    desc: "monorepo root composing all top-level blocks"
    composes: ["app", "aws", "go", "image", "k3d", "k8s", ...]
}
```

### BRICK + ACUTE + IDEA

Three complementary frameworks, each answering a different question:

**BRICK** classifies *what things are* — the static taxonomy of blocks.

| Register | Question | mu+pudl implementation |
|----------|----------|----------------------|
| Building block | What is this thing? | `config.Target` in mu, CUE definition in pudl |
| Role | What does it do? | `kind` field: relationship, interface, component, kit |
| Implementation | How is it realized? | Plugin (go, k8s, terraform, shell, file, docker, zig) |
| Configuration | How is it parameterized? | `config` map validated against plugin `config_schema` |
| Kit | What collection? | mu.json file, pudl workspace, `composes` field on kit targets |

**IDEA** classifies *what knowledge is* — four layers of understanding.

| Layer | What it holds | pudl implementation |
|-------|--------------|-------------------|
| **Intention** | What should be true. CUE schemas, constraints, governance policies. | `~/.pudl/schema/` — git-tracked CUE repository with `_pudl` metadata for inference, validation, and identity tracking |
| **Definition** | What you declared. Concrete desired-state values. | `~/.pudl/schema/definitions/*.cue` — named CUE instances that unify against Intention schemas (e.g., `api_deployment: k8s.#Deployment & {replicas: 3}`) |
| **Execution** | What tools computed. Results of convergence actions. | mu manifests ingested via `pudl import manifest.json --origin mu`, typed as `pudl/mu.#Manifest` with action outcomes, output digests, and BRICK metadata |
| **Application** | What actually exists. Observed live state. | Raw data imported via `pudl import` (JSON/YAML/CSV/NDJSON), auto-inferred against schemas, stored in SQLite catalog with resource identity and versioning |

**ACUTE** describes *how knowledge flows* — the pipeline that connects layers.

| Phase | What happens | Tool |
|-------|-------------|------|
| **Accumulate** | Import actual state from live systems | `pudl import --path /etc/nginx/nginx.conf` or `mu observe --json \| pudl import` |
| **Configure** | Normalize imported state, resolve naming differences | `pudl` schema inference (heuristic scoring + CUE unification) |
| **Unify** | Merge desired (Definition) with actual (Application), surface drift | `pudl drift check --all` (deep diff, field-level differences) |
| **Transform** | Export drifted resources as convergence targets | `pudl export-actions --all > converge.json` (schema ref → toolchain mapping) |
| **Execute** | Run convergence actions, report results | `mu build --emit-manifest --config converge.json` (DAG execution, manifest output) |

Together: BRICK says "this is a k8s component implementing the app interface."
IDEA says "its desired state is in the Definition layer, its actual state is
in the Application layer." ACUTE says "we accumulate actual state, unify it
with desired state, and execute convergence actions." The loop repeats.

mu and pudl are the tools that make this model executable.

## How mu+pudl Implement the Model

mu and pudl implement BRICK as two independent tools that communicate through
JSON files. Neither depends on the other at the code level. They compose
through Unix conventions: stdin/stdout, exit codes, and file I/O.

```
┌─────────────────────────────────────────────────────────────────┐
│                    THE BRICK LOOP                               │
│                                                                 │
│   ┌─────────┐      ┌─────────┐      ┌─────────┐               │
│   │  pudl   │      │  mu     │      │  pudl   │               │
│   │  export ├─────►│  build  ├─────►│  ingest │               │
│   │         │      │         │      │         │               │
│   │ mu.json │      │manifest │      │ actual  │               │
│   │(desired)│      │ (done)  │      │ state   │               │
│   └────▲────┘      └─────────┘      └────┬────┘               │
│        │                                  │                    │
│        │           ┌─────────┐            │                    │
│        │           │  pudl   │            │                    │
│        └───────────┤  drift  │◄───────────┘                    │
│                    │  check  │                                 │
│                    └─────────┘                                 │
│                                                                 │
│   Optional fast path:                                           │
│                                                                 │
│   ┌─────────┐                                                   │
│   │  mu     │  Direct drift detection without pudl.             │
│   │ observe │  Plugins check current state and report           │
│   │         │  converged / drifted / unknown.                   │
│   └─────────┘                                                   │
│                                                                 │
└─────────────────────────────────────────────────────────────────┘
```

## The Two Tools

### pudl — The Knowledge Layer

pudl is a personal data lake that owns the IDEA ontology. It is a Go binary
with a SQLite catalog, git-tracked CUE schema repository, and streaming
import pipeline.

**Storage layout (`~/.pudl/`):**
```
~/.pudl/
├── config.yaml                    # Configuration (incl. toolchain_mappings)
├── data/
│   ├── raw/YYYY/MM/DD/            # Date-partitioned raw imports
│   ├── metadata/                  # JSON metadata sidecars
│   ├── sqlite/catalog.db          # SQLite catalog (identity, versioning)
│   └── .drift/                    # Drift reports per definition
├── schema/                        # Git-tracked CUE repository
│   ├── .git/
│   ├── cue.mod/module.cue
│   ├── pudl/                      # Built-in schemas
│   │   ├── core/core.cue          # Catchall #Item, #Collection
│   │   ├── fs/fs.cue              # #File, #Dir, #Layout
│   │   ├── mu/mu.cue              # #Manifest, #ObserveResult, #PlanOutput
│   │   ├── brick/brick.cue        # #Target, #Interface, #Kit
│   │   ├── infra/                 # Infrastructure resource types
│   │   ├── artifact/              # #ImageRef, #ArtifactRef
│   │   └── ...                    # version, catalog, component, registry
│   └── definitions/               # Named desired-state instances
└── vaults/                        # Age-encrypted secrets
```

**What pudl does:**

| Capability | Command | Implementation |
|-----------|---------|---------------|
| Import any data | `pudl import <file>` | Streaming CDC parser, auto-format detection (JSON/YAML/CSV/NDJSON), content-hash dedup |
| Infer schemas | (automatic on import) | Heuristic scoring + CUE unification, cascade fallback to catchall |
| Track identity | (automatic on import) | Resource ID from schema `identity_fields`, version counter per resource |
| Define desired state | CUE files in `definitions/` | Named instances unifying against schemas (e.g., `api: k8s.#Deployment & {replicas: 3}`) |
| Detect drift | `pudl drift check` | Deep diff: declared keys vs latest imported state, field-level add/remove/change |
| Export to mu | `pudl export-actions` | Schema ref → toolchain mapping (configurable), desired state as `config` |
| Manage schemas | `pudl schema add/show/edit` | Git-backed with `status/commit/log` |
| Validate | `pudl validate`, `pudl verify` | CUE constraint checking, fixed-point verification |
| Store secrets | `pudl vault get/set` | Age-encrypted key-value store |
| Import mu results | `pudl import manifest.json --origin mu` | Auto-matched to `pudl/mu.#Manifest` schema |

**Built-in CUE schemas for mu integration:**

- `pudl/mu.#Manifest` — types `mu build --emit-manifest` output. Identity
  by timestamp, tracks summary and actions over time.
- `pudl/mu.#ObserveResult` — types `mu observe --json` output. Identity
  by target, tracks state and diff over time.
- `pudl/mu.#PlanOutput` — types `mu build --plan --json` output.
- `pudl/brick.#Target` — BRICK-classified mu target with kind, implements,
  composes fields. Enables definitions to carry BRICK metadata through
  the export → build → manifest → import round-trip.
- `pudl/brick.#Interface` — contract that components implement.
- `pudl/brick.#Kit` — composition of targets deployed together.

**Toolchain mapping (schema ref → mu toolchain):**

pudl maps CUE schema reference prefixes to mu toolchain names when
exporting convergence targets. Default mappings:

| Schema prefix | mu toolchain | Plugin |
|--------------|-------------|--------|
| `ec2.*`, `s3.*`, `iam.*`, `aws.*` | `aws` | (planned) |
| `k8s.*`, `kubernetes.*` | `k8s` | `plugins/k8s/plugin.bb` |
| `terraform.*`, `tf.*` | `terraform` | `plugins/terraform/plugin.bb` |
| `file.*`, `config.*` | `file` | `plugins/file/plugin.bb` |
| `docker.*`, `container.*` | `docker` | `plugins/docker/plugin.bb` |
| `shell.*`, `exec.*` | `shell` | Go built-in |
| `zig.*` | `zig` | `plugins/zig/plugin.bb` |

Custom mappings can be added in `~/.pudl/config.yaml`:
```yaml
toolchain_mappings:
  - prefix: "mycloud"
    toolchain: "mycloud-plugin"
```

User mappings take precedence over defaults.

pudl never executes actions. It never SSH's into a server, runs kubectl, or
calls cloud APIs. It only reads and compares.

### mu — The Execution Layer

mu is a Go binary that owns the action DAG: content-addressed, parallel,
plugin-driven.

**What mu does:**

| Capability | Command | Implementation |
|-----------|---------|---------------|
| Plan actions | `mu build --plan` | Coordinator.Plan(): toolchain build, plugin start, target resolve, plugin plan, DAG construction |
| Execute DAG | `mu build` | Coordinator.Execute(): parallel worker pool, CAS cache check/store |
| Validate configs | (automatic during plan) | Target config validated against plugin `config_schema` (type, required, enum) |
| Report results | `mu build --emit-manifest` | Versioned JSON manifest with per-action detail, BRICK metadata on targets |
| Observe drift | `mu observe` | Plugin observe method, capability negotiation, shell `observe_command` |
| Verify cache | `mu verify` | Re-hash all CAS blobs, detect corruption, `--fix` to delete |
| Shell escape | `toolchain: "shell"` | Go built-in, no bb required, `command` must be `[]string` |

**Key architectural features:**

- **Plan/Execute split** — `Coordinator.Plan()` builds the DAG and shuts down
  plugins before returning. `Coordinator.Execute()` runs the DAG. `Build()`
  is a convenience method that calls both. `--plan` mode calls only `Plan()`.

- **Impure actions** — actions with `impure: true` skip CAS cache lookup and
  storage entirely. Convergence actions (k8s apply, terraform apply) are
  inherently impure. Build actions (go build, zig build) are pure and cached.

- **Config validation** — after discover and before planning, mu validates
  each target's `config` against the plugin's declared `config_schema`.
  Catches missing required fields, type mismatches, and invalid enum values
  with clear error messages.

- **BRICK metadata passthrough** — targets carry optional `kind` and
  `implements` fields (set by pudl, ignored by mu during planning). These
  flow through to the manifest's `targets` section for round-tripping back
  to pudl.

- **Parallel plugin startup** — all plugin processes spawn and run discover
  concurrently via `errgroup.Group`, reducing startup from O(N * JVM_cold_start)
  to O(max(JVM_cold_start)).

- **Selective plugin startup for observe** — only starts plugins for
  toolchains referenced by the requested targets.

mu never defines desired state. It never stores CUE schemas or compares
states. It receives desired state as target configs and makes it real.

## How They Communicate

Three JSON documents flow between the tools. No shared libraries, no RPC,
no database. Just files.

### 1. mu.json — Desired State (pudl → mu)

pudl exports desired state as a standard mu configuration file. Each resource
that needs convergence becomes a target with a toolchain and config.

```json
{
  "targets": [
    {
      "target": "//k8s/api-deployment",
      "toolchain": "k8s",
      "sources": ["manifests/api-deployment.yaml"],
      "config": {
        "namespace": "production",
        "context": "prod-cluster",
        "server_side": true
      }
    },
    {
      "target": "//infra/vpc",
      "toolchain": "terraform",
      "sources": ["infra/vpc/main.tf", "infra/vpc/variables.tf"],
      "config": {
        "dir": "infra/vpc",
        "var_file": "prod.tfvars",
        "auto_approve": true
      }
    },
    {
      "target": "//config/nginx",
      "toolchain": "file",
      "config": {
        "path": "/etc/nginx/nginx.conf",
        "content": "server { listen 80; root /var/www/html; }",
        "mode": "0644"
      }
    }
  ]
}
```

pudl maps CUE schema references to mu toolchain names:

| CUE schema prefix | mu toolchain | Plugin type |
|-------------------|-------------|-------------|
| `file.*`, `config.*` | `file` | Babashka plugin |
| `k8s.*`, `kubernetes.*` | `k8s` | Babashka plugin |
| `terraform.*`, `tf.*` | `terraform` | Babashka plugin |
| `shell.*`, `exec.*` | `shell` | Go built-in |
| (unknown) | `generic` | Babashka plugin |

### 2. Manifest JSON — Execution Report (mu → pudl)

After `mu build --emit-manifest`, mu writes a versioned JSON document
describing what it did. pudl ingests this as Application-layer data.

```json
{
  "version": 1,
  "type": "mu.build.manifest/v1",
  "timestamp": "2026-03-24T14:30:00Z",
  "duration_s": 12.4,
  "actions": [
    {
      "id": "//k8s/api-deployment:apply",
      "cached": false,
      "exit_code": 0,
      "outputs": {}
    },
    {
      "id": "//infra/vpc:init",
      "cached": false,
      "exit_code": 0,
      "outputs": {}
    },
    {
      "id": "//infra/vpc:plan",
      "cached": false,
      "exit_code": 0,
      "outputs": {"infra/vpc/tfplan": "sha256:abc123..."}
    },
    {
      "id": "//infra/vpc:apply",
      "cached": false,
      "exit_code": 0,
      "outputs": {}
    }
  ],
  "summary": {
    "completed": 4,
    "cached": 0,
    "failed": 0,
    "cancelled": 0
  }
}
```

The manifest includes:
- **version** — schema version for forward compatibility
- **type** — format identifier for consumers
- **timestamp** — when the build completed (ISO 8601)
- **duration_s** — wall-clock time
- **actions** — per-action detail: ID, cached status, exit code, output digests
- **summary** — aggregate counts

Partial failure manifests include failed actions with their exit codes. pudl
can use this to learn which targets succeeded and which need retry.

### 3. Observe Results — Drift Check (mu → pudl, or standalone)

`mu observe --json` produces per-target state observations:

```json
[
  {"target": "//k8s/api-deployment", "state": "converged"},
  {"target": "//infra/vpc", "state": "drifted", "diff": "~ aws_instance.web: replicas 3 → 2"},
  {"target": "//config/nginx", "state": "unknown"}
]
```

States:
- **converged** — actual matches desired
- **drifted** — actual diverges from desired (diff included)
- **unknown** — plugin cannot observe (e.g., file plugin has no observe, or
  shell target without observe_command)

Exit codes: 0 if all converged/unknown, 1 if any drifted, 2 for usage error.

## The Full ACUTE Cycle

The ACUTE pipeline (Accumulate, Configure, Unify, Transform, Execute) maps
to concrete tool invocations:

### Phase 1: Accumulate

pudl imports actual state from live systems.

```bash
# Import Kubernetes resource state
pudl import --source k8s --context prod-cluster

# Import Terraform state
pudl import --source terraform --dir infra/vpc

# Import file state
pudl import --path /etc/nginx/nginx.conf
```

Or use mu as a shortcut for observation:

```bash
# mu asks plugins to check current state directly
mu observe --json --config converge.json //k8s/api-deployment //infra/vpc \
  | pudl import --origin mu
```

pudl's `pudl/mu.#ObserveResult` schema auto-matches the observe output,
storing it as Application-layer data with identity tracking per target.

### Phase 2: Configure

pudl normalizes imported state into a unified data lake.

```bash
pudl configure
```

This step resolves naming differences (e.g., Kubernetes uses `metadata.name`,
Terraform uses `resource.name`), normalizes types, and prepares data for
unification.

### Phase 3: Unify

pudl merges desired state (CUE definitions) with actual state (imported data)
in a CUE lattice. Conflicts surface as type errors. Drift surfaces as value
mismatches.

```bash
pudl drift check --all
```

Output:
```
//k8s/api-deployment    drifted   replicas: 3 → 2
//infra/vpc             converged
//config/nginx          drifted   content mismatch (12 bytes differ)
```

### Phase 4: Transform

pudl exports drifted resources as mu targets. Only resources that need
convergence are included.

```bash
pudl export-actions --drifted > /tmp/converge.json
```

### Phase 5: Execute

mu converges the drifted resources.

```bash
# Preview what would happen (no execution)
mu build --plan --json --config /tmp/converge.json //...

# Execute convergence and report results
mu build --emit-manifest --config /tmp/converge.json //... \
  > /tmp/manifest.json 2>build.log

# Feed results back to pudl (closes the loop)
# pudl auto-matches against pudl/mu.#Manifest schema
pudl import /tmp/manifest.json --origin mu
```

### The Loop Continues

After execution, pudl can re-observe to verify convergence:

```bash
# Re-import actual state
pudl import --source k8s --context prod-cluster

# Check drift again — should be clean if convergence succeeded
pudl drift check --all
# //k8s/api-deployment    converged
# //infra/vpc             converged
# //config/nginx          converged
```

## Plugin Architecture

mu's plugin system is the mechanism by which convergence actions are defined.
Each resource type has a dedicated plugin that speaks NDJSON over stdin/stdout.

### Plugin Protocol

Plugins implement up to three methods, declared in their discover response:

```
┌──────────┐                        ┌──────────────┐
│    mu    │  {"method":"discover"}  │    plugin    │
│          ├───────────────────────►│              │
│          │  {"name":"k8s",...,    │              │
│          │◄──"capabilities":      │              │
│          │   ["discover","plan",  │              │
│          │    "observe"]}         │              │
│          │                        │              │
│          │  {"method":"plan",...}  │              │
│          ├───────────────────────►│              │
│          │  {"actions":[...]}     │              │
│          │◄───────────────────────┤              │
│          │                        │              │
│          │  {"method":"observe"}   │              │
│          ├───────────────────────►│              │
│          │  {"state":"drifted",   │              │
│          │◄──"diff":"..."}        │              │
└──────────┘                        └──────────────┘
```

**discover** — what the plugin can do, which methods it supports.

**plan** — given a target with desired state in `config`, emit actions to
converge. Actions are shell commands with declared inputs, outputs,
environment, and network/impure flags.

**observe** — given a target, check current state and report
converged/drifted/unknown with an optional diff. Only convergence plugins
implement this; build plugins (go, zig, docker) return "unknown" by default.

### Plugin Capabilities

Plugins declare which methods they support via the `capabilities` field in
their discover response:

```json
{"capabilities": ["discover", "plan", "observe"]}
```

Old plugins that don't include `capabilities` default to `["discover", "plan"]`.
The coordinator checks capabilities before sending requests — plugins that
don't support observe are never asked.

### Available Plugins

| Plugin | Type | Toolchain | Capabilities | What it does |
|--------|------|-----------|--------------|-------------|
| `go` | Babashka | `go` | discover, plan | Builds Go binaries |
| `zig` | Babashka | `zig` | discover, plan | Builds Zig projects |
| `docker` | Babashka | `docker` | discover, plan | Builds Docker images |
| `file` | Babashka | `file` | discover, plan | Converges local files |
| `k8s` | Babashka | `k8s` | discover, plan, observe | Converges Kubernetes resources |
| `terraform` | Babashka | `terraform` | discover, plan, observe | Converges Terraform infrastructure |
| `shell` | Go built-in | `shell` | plan, observe* | Runs arbitrary commands |
| `cowsay` | Babashka | `cowsay` | discover, plan | Example/demo plugin |

\* Shell observe requires `observe_command` in target config.

### Pure vs Impure Actions

mu distinguishes between two kinds of actions:

**Pure actions** (builds) are deterministic: same inputs always produce the
same outputs. They are cached in the CAS. If the inputs haven't changed,
the action is skipped and outputs are restored from cache.

**Impure actions** (convergence) have external side effects: they modify
Kubernetes clusters, Terraform infrastructure, files on disk. They are
never cached. Every build re-executes them regardless of whether inputs
changed, because the external state may have drifted.

Plugins set `impure: true` on their actions to opt out of caching. The
shell built-in defaults to `impure: true`. Build plugins default to
`impure: false`.

```
                Pure actions                 Impure actions
                (go build, zig build)        (kubectl apply, terraform apply)
                ─────────────────────        ──────────────────────────────
Cache lookup    Yes                          Skipped
Cache storage   Yes                          Skipped
Deterministic   Yes                          No (depends on external state)
Side effects    No                           Yes
Default         impure: false                impure: true
Examples        Compile, link, archive       Apply, deploy, converge
```

## CLI Surface

### mu build

```bash
# Build targets (pure actions cached, impure actions always execute)
mu build //target1 //target2

# Preview the action DAG without executing (plan-only mode)
mu build --plan //target
mu build --plan --json //target     # machine-readable

# Build and emit a manifest for pudl consumption
mu build --emit-manifest //target > manifest.json

# Structured build summary (without full manifest)
mu build --json //target
```

### mu observe

```bash
# Check drift on specific targets
mu observe //k8s/api //infra/vpc

# Machine-readable output for pudl
mu observe --json //k8s/api //infra/vpc | pudl import --origin mu

# Exit codes for scripting
mu observe //target && echo "converged" || echo "drifted"
```

### mu verify

```bash
# Check CAS integrity
mu verify            # human-readable
mu verify --json     # machine-readable
mu verify --fix      # delete corrupt blobs
```

## Automation: The CI Pipeline

The full BRICK loop can be automated in CI:

```bash
#!/bin/bash
set -euo pipefail

# 1. Transform: export drifted targets
pudl export-actions --drifted > /tmp/converge.json

# 2. Preview: show what would change
mu build --plan --json --config /tmp/converge.json //...

# 3. Execute: converge and report
mu build --emit-manifest --config /tmp/converge.json //... \
  > /tmp/manifest.json

# 4. Ingest: feed results back to pudl (auto-matches pudl/mu.#Manifest)
pudl import /tmp/manifest.json --origin mu

# 5. Verify: re-observe to confirm convergence
mu observe --json --config /tmp/converge.json //... \
  | pudl import --origin mu

# 6. Check: assert no remaining drift
pudl drift check --all --strict
```

On a cron schedule, this creates a self-healing infrastructure loop. pudl
detects what's wrong, mu fixes it, pudl verifies the fix, and the cycle
repeats.

## Design Principles

### No Coupling

mu and pudl share no code, no libraries, no RPC protocol. They communicate
through:
- **mu.json** — pudl writes it, mu reads it
- **manifest.json** — mu writes it, pudl reads it
- **observe JSON** — mu writes it, pudl reads it (optional)

Either tool can be replaced. A different desired-state system could generate
mu.json. A different executor could consume pudl's export format. The
interface is JSON files and exit codes.

### Desired State, Not Drift Diffs

pudl exports **what should exist**, not **what changed**. The file plugin
receives `{"path": "...", "content": "..."}` — it doesn't know whether this
is a new file or a fix for drift. The k8s plugin receives a manifest — it
doesn't know whether this is a first deploy or a reconciliation.

This makes plugins simpler (no diff logic) and makes the system idempotent
(running convergence twice produces the same result).

### Plugins Define Semantics

mu is an empty vessel. It doesn't know what "deploy to Kubernetes" means or
how Terraform works. Plugins define:
- What actions to run (plan method)
- How to check current state (observe method)
- What resource types they handle (discover method)

mu only knows how to build a DAG and execute it. Everything else is
plugin-defined.

### Content-Addressed Where Possible

Pure build actions are fully content-addressed: the cache key is derived from
the command, input file hashes, environment, and network flag. If nothing
changed, the build is instantaneous.

Impure convergence actions are explicitly excluded from caching. The `impure`
flag is an honest declaration that says "this action has side effects — always
re-execute it." There is no TTL-based caching or heuristic skipping.

### Parallel by Default

mu's DAG executor runs actions in parallel across a worker pool. Independent
actions (different targets, no shared dependencies) execute concurrently.
Plugin startup is also parallelized — all plugin processes spawn and run
discover concurrently.

For convergence actions that modify shared external state (e.g., two Terraform
targets in the same account), users must express ordering via `deps` in
mu.json. mu respects dependency edges but maximizes parallelism for
independent work.
