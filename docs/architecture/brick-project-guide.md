# Building a BRICK Project with mu + pudl

**Date:** 2026-03-25
**Status:** Design document

How to structure a project using BRICK classification with mu and pudl,
modeled after the patterns in the defn monorepo. Covers what exists today,
what's proposed, and how each block type maps to the mu/pudl split.

## The Architecture

```
pudl (knowledge layer)              mu (execution layer)
─────────────────────              ────────────────────
CUE schemas & definitions          Plugins & actions
Drift detection                    Build / converge / observe
Constraint enforcement             DAG execution
Catalog & interfaces               CAS & sandboxing
                    ↓
             pudl export-actions
                    ↓
               mu.json (targets)
                    ↓
              mu build / observe
                    ↓
             mu --emit-manifest
                    ↓
           pudl ingest-manifest
```

pudl defines what should exist and detects when reality doesn't match.
mu makes reality match. Neither knows about the other's internals — they
communicate through mu.json and build manifests.

## The Four BRICK Kinds

Every block in a BRICK project is classified as one of:

| Kind | What it is | defn example | mu+pudl equivalent |
|------|-----------|-------------|-------------------|
| **Interface** | Contract other blocks must satisfy | `interface/app` (Kustomize macro) | pudl CUE schema + mu plugin protocol |
| **Component** | Concrete instance that does something | `app/argocd` (ArgoCD deployment) | mu target with `kind: "component"` |
| **Kit** | Composes blocks into a cohesive unit | `app/` (all 29 k8s apps) | mu target with `kind: "kit"` and `deps` |
| **Relationship** | Validates structure and connections | `manifest/` (directory validation) | pudl CUE close() schemas |

## Project Layout

A BRICK project using mu+pudl would be structured as:

```
my-project/
├── mu.json                    # Root kit — composes everything
├── definitions/               # pudl CUE definitions (desired state)
│   ├── apps.cue               # App catalog
│   ├── secrets.cue            # Secret definitions
│   ├── policy.cue             # Policy constraints
│   └── structure.cue          # Project structure rules
├── schemas/                   # pudl CUE schemas (contracts)
│   ├── app.cue                # #App interface schema
│   ├── secret.cue             # #Secret interface schema
│   └── brick.cue              # BRICK classification (from pudl bootstrap)
├── plugins/                   # mu plugins
│   ├── go/plugin.bb
│   ├── k8s/plugin.bb
│   ├── docker/plugin.bb
│   ├── pass/plugin.bb         # (proposed)
│   └── lint/plugin.bb         # (proposed)
├── app/                       # Component blocks (k8s apps)
│   ├── api/
│   │   ├── mu.json            # target: //app/api, toolchain: k8s
│   │   └── deployment.yaml
│   └── web/
│       ├── mu.json
│       └── deployment.yaml
├── infra/                     # Component blocks (terraform)
│   ├── vpc/
│   │   ├── mu.json            # target: //infra/vpc, toolchain: terraform
│   │   └── main.tf
│   └── rds/
│       ├── mu.json
│       └── main.tf
├── image/                     # Component blocks (container images)
│   └── api/
│       ├── mu.json            # target: //image/api, toolchain: docker
│       └── Dockerfile
├── secret/                    # Component blocks (secrets)
│   └── db-password/
│       └── mu.json            # target: //secret/db-password, toolchain: pass
├── lint/                      # Observe-only blocks (standards)
│   └── go/
│       └── mu.json            # target: //lint/go, toolchain: lint
├── env/                       # Kit blocks (per-environment composition)
│   ├── staging/
│   │   └── mu.json            # deps: [//app/api, //app/web, //infra/vpc]
│   └── production/
│       └── mu.json
└── cmd/                       # Go build targets
    └── server/
        ├── mu.json            # target: //cmd/server, toolchain: go
        └── main.go
```

## Block Types in Detail

### Kubernetes Apps

**In defn:** Each app is a Kustomize component implementing `interface/app`.
The interface provides a Bazel macro (`app_kustomize()`) that orchestrates
helm chart rendering across k8s versions. A catalog (`apps.cue`) is the
source of truth — the gen command stamps component directories from it.

**In mu+pudl:**

The mu target:
```json
{
  "target": "//app/api",
  "toolchain": "k8s",
  "kind": "component",
  "implements": "//interface/app",
  "sources": ["deployment.yaml", "service.yaml"],
  "config": {
    "namespace": "default",
    "server_side": true
  }
}
```

The pudl definition (desired state):
```cue
api_app: brick.#Target & {
    name:       "//app/api"
    kind:       "component"
    toolchain:  "k8s"
    implements: "//interface/app"
    config: {
        namespace: "production"
        server_side: true
    }
}
```

The pudl interface schema (contract):
```cue
#App: brick.#Interface & {
    contract: {
        // Every app must declare namespace and have sources
        config: {
            namespace: string
            server_side: bool | *true
        }
        sources: [...string] & list.MinItems(1)
    }
}
```

**What exists today:** mu's k8s plugin handles apply and structured drift
detection. pudl's `brick.#Target` schema and `export-actions` bridge work.
The `kind` and `implements` fields flow through mu's build manifests.

**Interface enforcement** is implemented in pudl — `pudl definition validate`
checks that components satisfy their interface contracts via CUE unification.
See pudl's `docs/mu-integration.md` for details.

**What's missing:** No catalog-driven generation (the Midas
pattern) — currently you create component directories by hand.

### Infrastructure (Terraform)

**In defn:** Terraform modules are vendored. Each org/account combo is a
component with `main.tf` referencing modules via relative paths. State
lives in S3 with per-account backends.

**In mu+pudl:**

```json
{
  "target": "//infra/vpc",
  "toolchain": "terraform",
  "kind": "component",
  "implements": "//interface/terraform-module",
  "sources": ["main.tf", "variables.tf"],
  "config": {
    "workspace": "production",
    "backend": "s3"
  }
}
```

The terraform plugin plans via `terraform plan` and observes via
`terraform plan -detailed-exitcode` (exit 2 = drift).

**Multi-account pattern:** Each account gets its own component directory
under `infra/org/<org>/<account>/`, just like defn. A kit target composes
them:

```json
{
  "target": "//infra",
  "toolchain": "shell",
  "kind": "kit",
  "deps": ["//infra/vpc", "//infra/rds", "//infra/org/prod/1"],
  "config": {"command": ["true"]}
}
```

### Container Images

**In defn:** Docker and Packer images live under `image/`. Each is a
component implementing `interface/image`.

**In mu+pudl:**

```json
{
  "target": "//image/api",
  "toolchain": "docker",
  "kind": "component",
  "implements": "//interface/image",
  "sources": ["Dockerfile", "*.go"],
  "config": {
    "tag": "registry.example.com/api:latest"
  }
}
```

**Buildpacks (proposed):** A `buildpack` plugin would detect the language
from source and build without a Dockerfile:

```json
{
  "target": "//image/api",
  "toolchain": "buildpack",
  "sources": ["*.go", "go.mod"],
  "config": {
    "builder": "gcr.io/buildpacks/builder:v1",
    "tag": "registry.example.com/api:latest"
  }
}
```

### Secrets

**In defn:** Secrets are managed via External Secrets Operator (ESO) with
CUE definitions. Each app's `secrets.cue` declares ExternalSecret manifests
that reference a secrets backend.

**In mu+pudl (proposed):**

The mu target:
```json
{
  "target": "//secret/db-password",
  "toolchain": "pass",
  "kind": "component",
  "implements": "//interface/secret",
  "sources": [],
  "config": {
    "pass_path": "infra/production/db-password",
    "output": "k8s-secret",
    "namespace": "default",
    "secret_name": "db-credentials"
  }
}
```

The `pass` plugin:
- **Plan:** `pass show infra/production/db-password` → create k8s secret YAML
  → `kubectl apply`
- **Observe:** `kubectl get secret db-credentials -o json` → compare value hash
  against `pass show` hash → report drift if mismatched

The pudl constraint:
```cue
// Every secret in the production namespace must come from pass
_secret_policy: {
    for name, def in definitions
    if def._schema == "brick.#Target"
    if def.config.namespace == "production"
    if def.toolchain == "k8s"
    if def.config._has_secrets == true {
        // Must have a corresponding pass secret target
        _requires_pass_target: true
    }
}
```

**For 1Password:** Same pattern, different plugin (`op`):
```json
{
  "target": "//secret/api-key",
  "toolchain": "op",
  "config": {
    "vault": "Production",
    "item": "API Key",
    "output": "env-file",
    "path": ".env.production"
  }
}
```

### Code (Go Binaries, Libraries, Internal Packages)

**In defn:** Go code is managed via Bazel with a complex Midas system.
`interface/go-cmd` provides CUE templates that stamp out `BUILD.bazel` and
`command.go` for each CLI command. `interface/go-lib` does the same for
libraries. A gen command reads the catalog and stamps components.

**In mu+pudl:**

Go binaries are straightforward — mu's go plugin already handles this:
```json
{
  "target": "//cmd/server",
  "toolchain": "go",
  "kind": "component",
  "implements": "//interface/go-cmd",
  "sources": ["cmd/server/*.go", "internal/**/*.go", "go.mod", "go.sum"],
  "config": {
    "output": "server",
    "pkg": "./cmd/server"
  }
}
```

Libraries and internal packages don't produce build artifacts — they're
inputs to other targets. In mu, they'd appear as sources in dependent
targets rather than as their own targets. However, they could have
observe-only targets for linting/testing:

```json
{
  "target": "//lint/internal-auth",
  "toolchain": "lint",
  "kind": "component",
  "implements": "//interface/go-lib",
  "sources": ["internal/auth/*.go"],
  "config": {
    "command": ["golangci-lint", "run", "./internal/auth/..."]
  }
}
```

**What mu doesn't need:** defn's Bazel-level `go_library` and `go_binary`
rules are unnecessary because `go build` handles dependency resolution
internally. The Go toolchain is simpler than Bazel's — you point at a
main package and it figures out the deps. mu's go plugin leverages this.

### Bots

**In defn:** Bot components are generated from catalog entries by Midas
interfaces. Each bot type (slack, discord, gmail, matrix, telegram) has
an interface with CUE templates. The gen command stamps manifest files,
BUILD.bazel, and mise.toml for each bot instance.

**In mu+pudl:**

A bot is a composition of a Go binary + a Docker image + k8s deployment:

```json
{
  "target": "//bot/wintermute",
  "toolchain": "shell",
  "kind": "kit",
  "deps": [
    "//cmd/wintermute",
    "//image/wintermute",
    "//app/wintermute"
  ],
  "config": {"command": ["true"]}
}
```

Where:
- `//cmd/wintermute` → go plugin (builds the binary)
- `//image/wintermute` → docker plugin (builds the container)
- `//app/wintermute` → k8s plugin (deploys to cluster)

The pudl definition ties it together:
```cue
wintermute: brick.#Kit & {
    name: "//bot/wintermute"
    kind: "kit"
    composes: ["//cmd/wintermute", "//image/wintermute", "//app/wintermute"]
}
```

**Bot config (Slack manifest, env vars)** would be managed via the `file`
plugin for the manifest and `pass`/`op` for API tokens:
```json
{
  "target": "//secret/wintermute-slack-token",
  "toolchain": "pass",
  "config": {
    "pass_path": "bots/wintermute/slack-token",
    "output": "k8s-secret",
    "namespace": "bots"
  }
}
```

### Environments

**In defn:** Environments compose apps, clusters, and configs into
deployable units. Each env (defn-a, defn-b, defn-c) has an `apps.yaml`
mapping apps to namespaces and a bootstrap manifest.

**In mu+pudl:**

An environment is a kit that composes everything needed for a deployment:

```json
{
  "target": "//env/staging",
  "toolchain": "shell",
  "kind": "kit",
  "deps": [
    "//app/api",
    "//app/web",
    "//infra/vpc",
    "//secret/db-password",
    "//lint/go"
  ],
  "config": {"command": ["true"]}
}
```

`mu build //env/staging` would build/converge everything in the staging
environment. `mu observe //env/staging` would check all components for drift.

The pudl definition adds environment-specific config overrides:
```cue
staging: brick.#Kit & {
    name: "//env/staging"
    kind: "kit"
    desc: "Staging environment"
    composes: ["//app/api", "//app/web", "//infra/vpc"]
}

staging_api: brick.#Target & {
    name:       "//app/api"
    kind:       "component"
    toolchain:  "k8s"
    config: {
        namespace: "staging"      // env-specific override
        replicas:  1              // staging doesn't need HA
    }
}
```

### Formatters / Linting

**In defn:** 14+ formatters, each a component implementing
`interface/fmt`. Bazel runs them via `fmt_test` rules that fail if
files aren't formatted.

**In mu+pudl (proposed):**

The `lint` plugin wraps any linter as an observe target:

```json
{
  "target": "//lint/go",
  "toolchain": "lint",
  "kind": "component",
  "implements": "//interface/fmt",
  "sources": ["**/*.go"],
  "config": {
    "command": ["gofmt", "-l", "."],
    "fix_command": ["gofmt", "-w", "."]
  }
}
```

- **Observe:** Run gofmt -l. Exit 0 + no output = converged. Any output = drifted.
- **Plan:** Run gofmt -w to auto-fix.

Multiple linters per language:
```json
{"target": "//lint/go-fmt", "config": {"command": ["gofmt", "-l", "."]}}
{"target": "//lint/go-vet", "config": {"command": ["go", "vet", "./..."]}}
{"target": "//lint/go-lint", "config": {"command": ["golangci-lint", "run"]}}
```

A kit composes all linters:
```json
{
  "target": "//lint",
  "kind": "kit",
  "deps": ["//lint/go-fmt", "//lint/go-vet", "//lint/go-lint"]
}
```

### Project Structure Validation

**In defn:** `manifest/manifest.cue` uses CUE's `close({})` to
exhaustively validate the entire directory tree. Every file and
directory must be declared — unexpected files are errors.

**In mu+pudl:**

This is primarily **pudl's domain** — CUE's `close()` constraint is
exactly the right tool. pudl would validate:

```cue
// schemas/structure.cue
#ProjectStructure: close({
    "mu.json":     _#file
    "go.mod":      _#file
    "go.sum":      _#file
    "README.md":   _#file
    "cmd":         close({[string]: _#goPackage})
    "internal":    close({[string]: _#goPackage})
    "plugins":     close({[string]: _#pluginDir})
    "app":         close({[string]: _#k8sApp})
    "infra":       close({[string]: _#terraformModule})
})
```

**mu's role** is limited to filesystem-level checks that pudl can't do
(pudl walks CUE definitions, not the filesystem). The proposed `structure`
plugin handles runtime checks:

```json
{
  "target": "//standards/structure",
  "toolchain": "structure",
  "config": {
    "required_files": ["README.md", "LICENSE", "go.mod"],
    "required_dirs": ["cmd", "internal"],
    "forbidden_patterns": ["vendor/**", "*.exe", ".env"]
  }
}
```

**Split:** pudl validates the structural schema (close constraints).
mu's structure plugin validates filesystem reality.

## Interfaces: How defn's Contracts Manifest in mu+pudl

### What an Interface Is

In defn, an interface is a directory (`interface/app/`) containing:
1. A contract schema (what fields a component must have)
2. A stamping mechanism (macro or generator to create components)
3. A catalog key (which catalog list enumerates implementations)

In mu+pudl, an interface is split across both tools:

**pudl owns the contract:**
```cue
// schemas/app.cue
#AppInterface: brick.#Interface & {
    name: "//interface/app"
    kind: "interface"
    contract: {
        toolchain: "k8s"
        config: {
            namespace: string
            server_side: bool | *true
        }
        sources: [...string] & list.MinItems(1)
    }
}
```

Any component that declares `implements: "//interface/app"` must satisfy
this contract. pudl validates this via CUE unification — if the component's
fields don't unify with the interface's contract, it's an error.

**mu carries the classification metadata:**

The `kind` and `implements` fields on mu targets are informational — mu
passes them through in build manifests but doesn't enforce them. Enforcement
is pudl's job. This is by design: mu is the executor, not the validator.

### Midas: Stamping Components from Interfaces

**In defn:** The gen command reads a catalog, loads an interface's
templates, and stamps out component directories. 19 generators run in
parallel during Phase A, producing BUILD.bazel, source files, and CUE
definitions for hundreds of components.

**In mu+pudl:** This would be a pudl feature — a `pudl stamp` command
that reads a catalog definition and generates component directories:

```bash
# Add a new app to the catalog
pudl catalog add app nginx --chart-repo https://charts.bitnami.com --chart-version 15.1.0

# Stamp the component directory from the interface template
pudl stamp //interface/app nginx
```

This would:
1. Read the `#AppInterface` schema for the contract
2. Read a template (CUE or file-based) from the interface definition
3. Create `app/nginx/mu.json` with the target config
4. Create `app/nginx/kustomization.yaml` (or whatever the template produces)
5. Register the brick in the catalog

**Implementation:** pudl already has definition discovery and CUE schema
loading. The stamping mechanism needs:
- A template spec on interface definitions (what files to generate)
- A stamp command that evaluates templates with catalog data
- Integration with the brick catalog (auto-register new bricks)

This is analogous to defn's gen command but driven by CUE instead of Go.

### Interface Summary

| defn interface | mu toolchain | pudl schema | Notes |
|---------------|-------------|-------------|-------|
| `interface/app` | k8s | `#AppInterface` | Kustomize apps |
| `interface/k8s` | k8s | `#K8sPlatform` | Platform composition |
| `interface/k3d` | shell | `#K3dCluster` | Local clusters |
| `interface/image` | docker / buildpack | `#ImageInterface` | Container images |
| `interface/go-cmd` | go | `#GoCmdInterface` | Go CLI commands |
| `interface/go-lib` | lint (observe-only) | `#GoLibInterface` | Go libraries (lint/test) |
| `interface/fmt` | lint | `#FmtInterface` | Code formatters |
| `interface/slack-bot` | shell (kit) | `#SlackBotInterface` | Slack bots |
| `interface/aws` | terraform | `#AWSInterface` | AWS resources |
| `interface/env` | shell (kit) | `#EnvInterface` | Deployment environments |

## What Needs to Be Built

### Already Working

| Capability | Tool | Status |
|-----------|------|--------|
| K8s apply + structured drift detection | mu (k8s plugin) | Done |
| Terraform plan/apply | mu (terraform plugin) | Done |
| Docker image build | mu (docker plugin) | Done |
| Go binary build | mu (go plugin) | Done |
| File convergence | mu (file plugin) | Done |
| Shell targets + kits | mu (shell builtin) | Done |
| Plugin CAS distribution | mu (digest resolution) | Done |
| BRICK target classification | mu (kind/implements fields) | Done |
| Drift detection | pudl (drift check) | Done |
| Export to mu.json | pudl (export-actions) | Done |
| BRICK toolchain mapping | pudl (brick.#Target) | Done |
| CUE schema validation | pudl (schema validate) | Done |

### Proposed: mu Plugins

| Plugin | Purpose | Complexity |
|--------|---------|-----------|
| `pass` | Secret management via password-store | Medium |
| `op` | Secret management via 1Password CLI | Medium |
| `lint` | Wrap any linter as observe target | Low |
| `structure` | Filesystem structure validation | Low |
| `policy` | OPA/conftest runtime policy | Medium |
| `buildpack` | Cloud Native Buildpacks | Medium |
| `docs` | Documentation completeness checks | Low |

### Proposed: pudl Features

| Feature | Purpose | Complexity |
|---------|---------|-----------|
| `pudl stamp` | Generate components from interface templates (Midas) | High |
| Close schema validation | Validate project structure via CUE close() | Medium |
| ~~Interface contract enforcement~~ | ~~Verify components satisfy interface schemas~~ | **Done** |
| Catalog management | Add/list/query catalog entries | Low |
| ACUTE loop closure | Ingest mu manifests and observe results | Medium |
| Secret policy constraints | CUE rules for secrets hygiene | Low |

## The Full Loop

When everything is built, a BRICK project workflow looks like:

```bash
# 1. Define desired state (pudl)
vim definitions/apps.cue          # Add a new app to catalog

# 2. Stamp component from interface (pudl)
pudl stamp //interface/app nginx  # Creates app/nginx/ from template

# 3. Validate constraints (pudl)
pudl drift check --all            # Detect drift from desired state

# 4. Export and converge (mu)
pudl export-actions --all | mu build --config -

# 5. Observe (mu)
mu observe //env/production       # Structured drift detection

# 6. Close the loop (pudl)
mu observe --json //env/production | pudl ingest-observe
pudl drift check --all            # Re-check with fresh state
```

Steps 3-6 repeat continuously. The catalog + interface system (steps 1-2)
is how you grow the project — adding a new app, bot, or infra component
is a catalog entry + stamp, not manual directory creation.
