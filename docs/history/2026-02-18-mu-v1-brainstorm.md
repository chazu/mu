# Brainstorm: mu v1 — Language-Agnostic Build Coordinator

**Date:** 2026-02-18
**Status:** Refined

## What We're Building

**mu** is a language-agnostic build system that knows nothing about languages, compilers, or toolchains. It coordinates a DAG of content-addressed actions emitted by external plugins. The name means "emptiness" — mu has no built-in semantics. Plugins fill it with meaning.

### Core Primitives

1. **Artifacts** — content-addressed blobs in a CAS (content-addressed store)
2. **Actions** — hermetic transformations: input artifacts → output artifacts
3. **Plugins** — external executables that emit action subgraphs via a protocol
4. **Services** — long-running processes for dev workflows
5. **Triggers** — file watchers that re-execute targets and restart services

### What mu Does

- Resolves the DAG of actions
- Checks the cache (local disk → OCI registry, tiered)
- Schedules actions with maximal parallelism
- Manages plugin lifecycle (discover, plan)
- Bootstraps toolchains as content-addressed artifacts
- Orchestrates dev workflows (services + triggers + rebuilds)

### What mu Does NOT Do

- Know anything about any programming language
- Contain any language-specific build rules
- Manage language-level dependency resolution (that's the plugin's job or a pre-build step)

## Why This Approach

**Plugin protocol over built-in rules.** Instead of Bazel's approach (build system contains rules for each language via Starlark), mu inverts it: each toolchain is a plugin that emits action graphs. The build system is just the executor. This is the LSP model applied to builds.

**Content-addressed everything.** Universal caching across all languages. Toolchain upgrades are hash changes. Remote cache works automatically.

**OCI as the cache layer.** Reuses infrastructure every org already operates. Auth, replication, GC, monitoring — all solved. Blobs map to OCI blobs, action results map to OCI manifests.

**CUE for configuration, EDN/JSON for protocol.** CUE provides schema validation and policy enforcement at the human-facing config layer. The plugin wire protocol stays simple structured data.

## Key Decisions

1. **Implementation language:** Go (coordinator) + Babashka (example plugins)
2. **Plugin protocol:** EDN/JSON over stdin/stdout, with format negotiation
3. **Build file format:** CUE (with policy/schema validation)
4. **Plugin wire format:** EDN for bb plugins, JSON as lowest common denominator
5. **Caching:** Full tiered CAS — local disk → OCI registry (read-repair, write-through)
6. **Dev runtime:** Both container-based and host-native services from the start
7. **Hermeticity:** Honor system for v1 (plugins declare inputs/outputs, no enforcement). Architecture supports strict sandboxing later.
8. **Bootstrap:** mu manages toolchain downloads and verification from v1
9. **File watching:** Core feature — `mu dev` with triggers and service restarts from v1
10. **Implementation approach:** Bottom-up — CAS → DAG → Plugins → Build → Dev

## Architecture Overview

```
                    ┌──────────────┐
  BUILD.cue ───────►│              │
  policy.cue ──────►│  CUE eval    │──── validated config ────► Coordinator
  toolchains.cue ──►│              │                            │
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
                    │  Plugin Manager │            │     CAS       │   │  Services  │
                    │  (stdin/stdout) │            │  (tiered)     │   │  Manager   │
                    └────────┬────────┘            └───────┬───────┘   └─────┬──────┘
                             │                             │                 │
                    ┌────────┼────────┐           ┌────────┼────┐     ┌──────┼──────┐
                    ▼        ▼        ▼           ▼        ▼    ▼     ▼      ▼      ▼
                  go.bb  rust.bb  docker.bb    disk    OCI reg  S3  docker  host  triggers
```

## Package Structure

```
cmd/mu/             # CLI entry point
internal/
├── cas/            # content-addressed store (Store interface, TieredStore)
├── cas/disk/       # local disk backend
├── cas/oci/        # OCI registry backend
├── dag/            # DAG construction, topo sort, parallel scheduling
├── plugin/         # plugin lifecycle, protocol, codec (EDN/JSON)
├── config/         # CUE build file loading and validation
├── service/        # service manager (docker + host runtimes)
├── trigger/        # file watcher, debounce, rebuild triggers
├── sandbox/        # (future) hermetic execution enforcement
└── builtin/        # built-in commands (forge-fetch, forge-register-toolchain)
plugins/
├── go/plugin.bb
├── bootstrap/plugin.bb
└── ...
```

## Implementation Order (Bottom-Up)

1. **CAS** — Store interface, local disk backend, content hashing
2. **DAG** — Action graph, topological sort, parallel executor with goroutines
3. **Plugin protocol** — Codec interface (EDN/JSON), plugin lifecycle (spawn, discover, plan)
4. **Bootstrap plugin** — Toolchain fetch, verify, register
5. **CUE config** — Build file loading, schema validation
6. **`mu build`** — End-to-end: parse config → plan via plugins → execute DAG → cache results
7. **OCI cache backend** — Push/pull blobs and action results to OCI registries
8. **Tiered cache** — Chain local + OCI backends with read-repair and write-through
9. **Service manager** — Docker and host-native runtimes, healthchecks, lifecycle
10. **Triggers** — File watching, debounce, rebuild + restart orchestration
11. **`mu dev`** — Compose services + triggers into the dev experience

## Open Questions

- **Dependency resolution:** Should mu have any opinion on how plugins handle language-level deps, or is it purely "lockfile consumed by plugin"?
- **Build file discovery:** Walk up the directory tree? Explicit root marker? Both?
- **Plugin distribution:** How do users install/update third-party plugins? OCI artifacts? Git repos?
- **Network actions:** Bootstrap needs network access. How do we model the network-allowed vs hermetic distinction in the action graph?
- **Incremental compilation:** Some toolchains (Go, Rust) have their own incremental caching. How does that interact with mu's CAS? Does the plugin just declare the compiler's cache dir as an input/output?

## Next Steps

Run `/workflows:plan` to create a detailed implementation plan following the bottom-up order.
