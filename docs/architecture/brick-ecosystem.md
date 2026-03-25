# The BRICK Ecosystem: mu + pudl

**Date:** 2026-03-24
**Status:** Working document

## What Is BRICK

BRICK is an infrastructure management model inspired by the ACUTE pipeline
and IDEA ontology from the defn-dev project. It describes a self-refining
feedback loop where desired state is declared, actual state is observed, drift
is detected, and convergence actions are executed — continuously.

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

pudl owns the **IDEA ontology**: Intention, Definition, Execution, Application.

| Layer | What it holds | Where it lives |
|-------|--------------|----------------|
| **Intention** | CUE schemas, governance policies, constraints | `*.cue` files defining resource types |
| **Definition** | Declared desired state for each resource | `*.cue` files referencing Intention schemas |
| **Execution** | Outputs of convergence actions (what mu did) | Manifest JSON ingested from mu |
| **Application** | Observed actual state of the world | Imported from live systems by pudl |

pudl's job:
1. Define desired state in CUE (schemas + values)
2. Import actual state from live systems (`pudl import`)
3. Unify desired and actual state in a CUE lattice
4. Detect drift (desired vs actual divergence)
5. Export convergence targets as mu.json (`pudl export-actions`)
6. Ingest execution results from mu manifests (`pudl ingest`)

pudl never executes actions. It never SSH's into a server, runs kubectl, or
calls cloud APIs. It only reads and compares.

### mu — The Execution Layer

mu owns the **action DAG**: content-addressed, parallel, plugin-driven.

mu's job:
1. Load targets from mu.json (hand-written or pudl-generated)
2. Ask plugins to plan convergence actions for each target
3. Build a dependency-ordered DAG of actions
4. Execute actions in parallel with a worker pool
5. Cache pure action results in the CAS (skip impure convergence actions)
6. Report what was done as a structured manifest
7. Optionally observe current state via plugins (drift detection shortcut)

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
  | pudl ingest --layer application
```

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
pudl ingest --layer execution < /tmp/manifest.json
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
mu observe --json //k8s/api //infra/vpc | pudl ingest

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

# 4. Ingest: feed results back to pudl
pudl ingest --layer execution < /tmp/manifest.json

# 5. Verify: re-observe to confirm convergence
mu observe --json --config /tmp/converge.json //... \
  | pudl ingest --layer application

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
