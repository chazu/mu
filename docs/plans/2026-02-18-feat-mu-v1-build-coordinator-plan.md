---
title: "feat: Implement mu v1 — Language-Agnostic Build Coordinator"
type: feat
date: 2026-02-18
---

# feat: Implement mu v1 — Language-Agnostic Build Coordinator

## Overview

mu is a language-agnostic build coordinator that knows nothing about languages, compilers, or toolchains. It coordinates a DAG of content-addressed actions emitted by external plugins via a stdin/stdout protocol. v1 delivers: `mu build`, `mu dev`, bootstrap, tiered CAS with OCI caching, and file watching. True to its philosophy of emptiness, mu only understands JSON natively. Non-JSON config formats (CUE, EDN, TOML, etc.) are handled by **config preprocessors** — external commands that transform their format into JSON. mu is as agnostic about its own config language as it is about build toolchains.

## Problem Statement / Motivation

Modern projects span multiple languages and toolchains. Existing build systems either force everything through a single config language (Bazel/Starlark) or don't provide caching and hermeticity (Make, scripts). mu inverts the model: the build system is an empty vessel. Plugins fill it with meaning. This gives you universal content-addressed caching, maximal parallelism, and composability across any toolchain — without the build system needing to understand any of them.

## Proposed Solution

A Go binary (`mu`) that:
1. Loads JSON build files defining targets, services, and triggers (non-JSON formats via config preprocessors)
2. Spawns plugin processes (Babashka scripts or any executable) speaking EDN/JSON
3. Asks plugins to emit action subgraphs for each target
4. Merges subgraphs into a global DAG
5. Executes actions in parallel via goroutine worker pool
6. Caches results in a tiered CAS (local disk → OCI registry)
7. Manages long-running services (Docker + host-native) for dev workflows
8. Watches files and triggers rebuilds + service restarts

## Technical Approach

### Architecture

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
                  go.bb  rust.bb  docker.bb    disk    OCI reg       docker  host  triggers
```

### Package Structure

```
cmd/mu/                  # CLI entry point (cobra or minimal)
internal/
├── cas/                 # CAS interfaces and TieredStore
│   ├── cas.go           # Store interface, Digest type, ActionKey/ActionResult
│   ├── tiered.go        # TieredStore (read-repair, write-through)
│   ├── disk/            # Local disk backend
│   │   └── disk.go      # DiskStore implementation
│   └── oci/             # OCI registry backend
│       └── oci.go       # OCIStore implementation (oras-go v2)
├── dag/                 # DAG construction and execution
│   ├── graph.go         # Action graph, node/edge types
│   ├── topo.go          # Topological sort (Kahn's algorithm)
│   └── executor.go      # Parallel executor (worker pool)
├── plugin/              # Plugin lifecycle and protocol
│   ├── manager.go       # PluginManager — spawn, discover, plan
│   ├── protocol.go      # Request/Response types
│   └── codec/           # Serialization
│       ├── codec.go     # Codec interface
│       ├── json.go      # JSON codec
│       └── edn.go       # EDN codec (olympos.io/encoding/edn)
├── config/              # Build file loading (JSON-native + preprocessors)
│   ├── loader.go        # Project root discovery, JSON loading, preprocessor dispatch
│   ├── types.go         # Go types: Target, Service, Trigger, Toolchain
│   ├── validate.go      # Go-level validation (required fields, patterns)
│   └── preprocess.go    # Config preprocessor: detect format, run external command → JSON
├── service/             # Service manager
│   ├── manager.go       # ServiceManager — start, stop, restart, healthcheck
│   ├── docker.go        # Docker runtime (via Docker API)
│   └── host.go          # Host-native runtime (os/exec)
├── trigger/             # File watcher and trigger orchestration
│   ├── watcher.go       # fsnotify wrapper, debouncing
│   └── trigger.go       # Trigger logic — rebuild + restart
├── bootstrap/           # Toolchain bootstrap logic
│   └── bootstrap.go     # Fetch, verify SHA256, extract, register
└── builtin/             # Built-in coordinator commands
    └── fetch.go         # Network-allowed fetch with hash verification
```

### Key Dependencies

| Component | Library | Version |
|-----------|---------|---------|
| OCI registry | `oras.land/oras-go/v2` | v2.6.0 |
| OCI types | `github.com/opencontainers/image-spec` | v1.1.1 |
| EDN codec | `olympos.io/encoding/edn` | latest (plugin wire format) |
| File watching | `github.com/fsnotify/fsnotify` | v1.9.0 |
| Hashing | `crypto/sha256` (stdlib) | — |
| Atomic writes | `github.com/google/renameio` | latest |
| JSON | `encoding/json` (stdlib) | — |

**Not dependencies of mu:** CUE, TOML, and any other config format. These are external preprocessor commands, not compiled into mu. mu's only config format is JSON.

### Design Decisions for SpecFlow Gaps

These decisions resolve the critical gaps identified during spec analysis. Each is a reasonable v1 default that can evolve later.

**Plugin lifecycle (Gap 5):** One process per plugin per build invocation. Plugin stays alive for the duration of the build. On crash: fail the build (no auto-retry in v1). Graceful shutdown via closing stdin.

**Plugin format negotiation (Gap 8):** Declared in `mu.plugins.edn` manifest via `:format` key. Default is JSON. Plugin is spawned with the declared format. No runtime negotiation.

**Plugin timeout (Gap 6):** 10s for `:discover`, 5min for `:plan`. Configurable globally in `mu.cue`. On timeout: kill plugin, fail affected targets.

**Plugin error reporting (Gap 9):** Errors are maps with `:error` key in the response. Stderr is captured and displayed on failure. Exit code != 0 is treated as crash.

**Plugin protocol versioning (Gap 7):** `:discover` response includes `:protocol-version 1`. mu checks compatibility. No backward compat in v1 — just fail with clear message.

**Build file discovery (Gap 4):** Walk up from cwd looking for `mu.json` (always JSON — the one file mu reads directly). If `mu.json` declares a `preprocessor`, find `BUILD.<ext>` files and pipe them through the preprocessor command to get JSON. Otherwise find `BUILD.json` files. `//` prefix is relative to root.

**CAS GC (Gap 11):** Manual `mu clean` command. `--max-age` and `--max-size` flags. No automatic GC in v1. OCI blobs rely on registry-side GC policies.

**CAS atomics (Gap 12):** Write to temp file in CAS dir, rename to final path (content hash). If two builds produce same hash concurrently, both rename to same path — idempotent by construction.

**CAS corruption (Gap 13):** No re-hash on read in v1 (performance). `mu verify` command to scan and re-hash. On detected corruption: delete + rebuild.

**OCI auth (Gap 16):** Read from `~/.docker/config.json` via standard Docker credential helpers. Support `MU_REGISTRY_AUTH` env var as override.

**OCI config (Gap 17):** Configured in `mu.cue` under `:cache :backends`. Supports multiple registries with read/write flags.

**Action environment (Gap 23):** Minimal env: `PATH` (toolchain bin dir only), `HOME` (temp dir), `TMPDIR` (action-specific temp). No inherited env. Declared `env` merged on top.

**Network actions (Gap 30):** Actions have `network: false` by default. Bootstrap actions set `network: true`. In v1 (honor system), this is metadata only — no enforcement.

**Parallelism (Gap 19):** Default workers = `runtime.NumCPU()`. Override with `--jobs N` flag or `mu.cue` config.

**Service startup order (Gap 31):** Services declare `depends-on` with condition (`:healthy` or `:started`). Topological sort. Wait for condition before starting dependents.

**Service logs (Gap 34):** Stream to console with `[service-name]` prefix and color coding. No log files in v1.

**Service shutdown (Gap 35):** SIGTERM, wait 10s, SIGKILL. Docker: `docker stop` (10s timeout). Remove containers on shutdown. Named volumes preserved.

**Service networking (Gap 39):** Docker services on a shared `mu-dev` bridge network. Service names as DNS hostnames. Host services use localhost.

**File watch scope (Gap 40):** Watch directories containing source files declared in triggers. Exclude `.git`, `node_modules`, CAS dir. Ignore editor temp files (`.swp`, `.#*`, `~` suffix).

**Debounce (Gap 41):** Default 300ms. Configurable per-trigger in BUILD.cue.

**Rebuild scope (Gap 42):** Hash changed files. Walk DAG to find actions with invalidated inputs. Rebuild only affected subgraph.

**Trigger errors (Gap 44):** Display error, keep watching. Services continue running with last good build. Desktop notification on failure if configured.

**Progress (Gap 48):** Line-based output: `[status] //target (time)`. Show in-flight actions count. No TUI in v1.

**Ctrl-C (Gap 21):** SIGINT → cancel context → kill in-flight actions (SIGTERM) → clean up partial outputs → exit.

### Design Decisions from Phase 1–2 Review

These decisions were made during implementation planning for Phases 1 and 2.

**Digest type:** Struct with `Algorithm` and `Hash` fields (not a plain string). This allows future migration to BLAKE3 without touching every call site. Format: `type Digest struct { Algorithm string; Hash string }`. String representation: `"sha256:abcdef..."`.

**CAS directory location:** Hardcoded to `~/.mu/cache/` in v1. Will become configurable when tiered cache (Phase 8) lands.

**`renameio` dependency:** Keep `google/renameio` for atomic writes rather than bare `os.Rename`. This prepares for eventual remote/cross-filesystem cache backends.

**Input digest resolution:** Plugins emit `ActionSpec` with file **paths** as inputs (not digests). The coordinator resolves paths → hashes files → produces `Action` with `Inputs map[string]Digest`. This resolution step sits between plugin output and DAG construction. `ActionSpec` (plugin wire type) and `Action` (executor type) are distinct types.

**Action working directory — in-place for v1:** Actions run in the project directory (or a specified `WorkDir`), not in isolated temp dirs. This matches the "honor system hermeticity" philosophy already applied to network access. `Inputs` still matters for cache key computation (hash declared inputs), but no physical filesystem isolation. Sandboxing is post-v1.

**ActionKey computation:** Hash of: sorted command args + sorted input digests (name→hash) + sorted env vars + network flag. This is the cache lookup key. Must be deterministic (sorted maps).

**Error strategy — cancel downstream, continue independent:** On action failure, cancel all transitive dependents but continue executing independent subgraphs. Building `//A` and `//B` where `//A` fails: `//B` and its deps continue. This is the only behavior in v1 — no `--keep-going` or `--fail-fast` flags needed since this is already the sensible default.

**Directory outputs — individual files only for v1:** Actions declare specific output file paths, not directories. CAS remains a pure blob store. Plugins know their outputs (Go → binary, protoc → specific `.go` files). Tree objects or tar-based directory storage deferred to post-v1.

**Large blob test:** Defer >1GB streaming test to integration suite. Unit tests use small blobs. Streaming through SHA-256 is straightforward and doesn't need a large-blob unit test.

## Implementation Phases

### Phase 1: Foundation — CAS + Hashing

**Goal:** Content-addressed blob storage on local disk.

**Files:**
- `internal/cas/cas.go` — `Store` interface, `Digest` type, `ActionKey`, `ActionResult`
- `internal/cas/disk/disk.go` — `DiskStore`: `Put`, `Get`, `Has`, `Delete`
- `go.mod` — Add `github.com/google/renameio`

**Digest type:**
```go
type Digest struct {
    Algorithm string // "sha256"
    Hash      string // hex-encoded hash
}

func (d Digest) String() string { return d.Algorithm + ":" + d.Hash }
```

**ActionKey and ActionResult:**
```go
// ActionKey is the cache lookup key — deterministic hash of action inputs.
// Computed from: sorted command args + sorted input digests + sorted env + network flag.
type ActionKey struct {
    Digest Digest // hash of the canonical key material
}

// ActionResult records what a cached action produced.
type ActionResult struct {
    Outputs  map[string]Digest // output name → content digest
    ExitCode int
}
```

**Store interface:**
```go
type Store interface {
    Has(ctx context.Context, dgst Digest) (bool, error)
    Get(ctx context.Context, dgst Digest) (io.ReadCloser, error)
    Put(ctx context.Context, r io.Reader) (Digest, error)
    Delete(ctx context.Context, dgst Digest) error
    GetActionResult(ctx context.Context, key ActionKey) (*ActionResult, error)
    PutActionResult(ctx context.Context, key ActionKey, result *ActionResult) error
}
```

**DiskStore layout:**
```
~/.mu/cache/
├── blobs/
│   ├── sha256/
│   │   ├── ab/
│   │   │   └── cdef1234...  (blob content)
│   │   └── ...
│   └── tmp/                  (atomic write staging)
└── actions/
    └── sha256-<key-hash>.json  (ActionResult JSON)
```

**Key behaviors:**
- SHA-256 hashing via `crypto/sha256`
- Atomic writes: temp file → `os.Rename`
- 2-level fan-out for blob dir (first 2 chars of hash)
- Action results stored as JSON files keyed by action key hash

**Tests:**
- Put/Get roundtrip
- Has for existing and missing blobs
- Concurrent Put of same content (idempotent)
- ActionResult Put/Get roundtrip
- Digest String() and Parse roundtrip

**Acceptance criteria:**
- [ ] Store interface defined with all methods
- [ ] DiskStore implements Store
- [ ] Atomic writes verified (kill mid-write, no corruption)
- [ ] Concurrent access safe
- [ ] Tests pass

---

### Phase 2: DAG Construction + Parallel Execution

**Goal:** Build and execute a DAG of actions with parallel scheduling.

**Files:**
- `internal/dag/graph.go` — `Action` type, `Graph` type (adjacency list)
- `internal/dag/topo.go` — Kahn's algorithm, cycle detection
- `internal/dag/executor.go` — Worker pool, channel-based scheduling

**Action type:**
```go
type Action struct {
    ID         string
    Command    []string
    Inputs     map[string]Digest  // name → content hash
    Outputs    []string           // declared output paths
    DependsOn  []string           // action IDs
    Env        map[string]string
    Network    bool               // allow network access
    WorkDir    string             // execution working directory
}
```

**Executor design:**
- Worker pool with configurable concurrency (`--jobs N`, default `runtime.NumCPU()`)
- `ready` channel for actions with all deps satisfied
- `results` channel for completed actions
- On action completion: hash declared output files, store in CAS, check what's unblocked
- On action failure: cancel transitive dependents, continue independent subgraphs
- Context cancellation for Ctrl-C

**ActionKey computation (for cache lookup):**
- Canonical key material: sorted command args + sorted input digests (name→hash) + sorted env vars + network flag
- SHA-256 hash of canonical key material → ActionKey.Digest
- Must be deterministic: all maps sorted by key before hashing

**Key behaviors:**
- Kahn's algorithm produces levels of parallelizable actions
- Cache check before execution: compute ActionKey from resolved inputs, check CAS. Hit → skip execution, restore outputs from CAS
- Actions execute in-place (project dir or Action.WorkDir) — honor system hermeticity for v1
- On success: hash each declared output file path, store blobs in CAS, store ActionResult mapping output names → digests
- Output files only (no directory outputs) — actions declare specific file paths

**Tests:**
- Linear chain: A → B → C
- Diamond: A → B, A → C, B+C → D
- Independent actions run in parallel (verify timing)
- Cycle detection
- Failure cancels downstream but independent subgraph continues
- Cache hit skips execution, restores outputs
- ActionKey determinism (same inputs → same key)
- Ctrl-C cancels in-flight

**Acceptance criteria:**
- [ ] Graph construction from action list
- [ ] Topological sort with cycle detection
- [ ] Parallel execution respects dependencies
- [ ] ActionKey computation is deterministic
- [ ] Cache integration (skip cached actions, restore outputs)
- [ ] Failure cancels downstream only, independent work continues
- [ ] Graceful cancellation
- [ ] Tests pass

---

### Phase 3: Plugin Protocol + Codec

**Goal:** Spawn plugin processes, send discover/plan requests, parse responses.

**Files:**
- `internal/plugin/codec/codec.go` — `Codec` interface
- `internal/plugin/codec/json.go` — JSON codec
- `internal/plugin/codec/edn.go` — EDN codec
- `internal/plugin/protocol.go` — Request/Response types
- `internal/plugin/manager.go` — `PluginManager`

**Codec interface:**
```go
type Codec interface {
    Encode(v any) ([]byte, error)
    Decode(data []byte, v any) error
    Name() string
}
```

**Protocol types:**
```go
type DiscoverResponse struct {
    Name            string            `json:"name" edn:"name"`
    Version         string            `json:"version" edn:"version"`
    ProtocolVersion int               `json:"protocol_version" edn:"protocol-version"`
    Consumes        []string          `json:"consumes" edn:"consumes"`
    Produces        []string          `json:"produces" edn:"produces"`
    ConfigSchema    map[string]any    `json:"config_schema" edn:"config-schema"`
    Format          string            `json:"format" edn:"format"` // "json" or "edn"
}

type PlanRequest struct {
    Method             string            `json:"method" edn:"method"`
    Target             TargetInfo        `json:"target" edn:"target"`
    Deps               []DepInfo         `json:"deps" edn:"deps"`
    ToolchainArtifacts map[string]string `json:"toolchain_artifacts" edn:"toolchain-artifacts"`
}

type PlanResponse struct {
    Actions []ActionSpec          `json:"actions" edn:"actions"`
    Outputs map[string]string     `json:"declared_outputs" edn:"declared-outputs"`
    Error   string                `json:"error,omitempty" edn:"error"`
}
```

**PluginManager behaviors:**
- Read `mu.plugins.edn` manifest to find plugins
- Spawn plugin process (`exec.Command`)
- Send `:discover` on startup, validate response
- Keep plugin alive for duration of build
- Send `:plan` per target, parse response
- Capture stderr for error reporting
- Timeout: 10s discover, 5min plan
- On crash (stdin closed / exit code != 0): fail build with stderr output

**Tests:**
- JSON codec roundtrip for all message types
- EDN codec roundtrip for all message types
- Spawn mock plugin (shell script), send discover, get response
- Plan request/response with mock plugin
- Plugin timeout (mock plugin that sleeps)
- Plugin crash (mock plugin that exits)
- Stderr capture on failure

**Acceptance criteria:**
- [ ] JSON and EDN codecs implemented
- [ ] PluginManager spawns and manages plugin processes
- [ ] Discover and Plan protocol working end-to-end
- [ ] Timeout and crash handling
- [ ] Tests pass with mock plugins

---

### Phase 4: Bootstrap — Toolchain Management

**Goal:** Fetch, verify, extract, and register toolchains as CAS artifacts.

**Files:**
- `internal/bootstrap/bootstrap.go` — `Bootstrapper` type
- `internal/builtin/fetch.go` — `ForgeFetch` (network-allowed download + SHA256 verify)

**Bootstrapper design:**
- Reads toolchain definitions from CUE config
- For each toolchain: check CAS for existing manifest
- If missing: fetch archive (URL + SHA256), extract, verify (run binary `--version`), register
- Produces a `ToolchainManifest` mapping logical names to CAS digests
- Manifests are stored in CAS and loaded on subsequent builds

**ForgeFetch:**
- Download to temp file
- Stream through SHA256 hasher
- Compare against expected hash
- Fail if mismatch with clear error showing expected vs actual
- Retry 3 times with exponential backoff on network errors

**Archive extraction:**
- Support `.tar.gz`, `.tar.xz`, `.zip`
- Optional `strip-prefix` to unwrap top-level dir
- Optional `install-script` for toolchains that need post-extract setup (Rust)

**Tests:**
- Fetch from HTTP server (httptest)
- SHA256 verification (pass and fail)
- Tar.gz extraction with strip-prefix
- Zip extraction
- Toolchain manifest creation and storage
- Cache hit skips fetch
- Network retry on transient error
- Toolchain upgrade (version change) triggers re-fetch

**Acceptance criteria:**
- [ ] Fetch with SHA256 verification
- [ ] Support tar.gz, tar.xz, zip
- [ ] Extract with strip-prefix
- [ ] Store toolchain artifacts in CAS
- [ ] Manifest registration
- [ ] Cache hit → skip
- [ ] Retry on network errors
- [ ] Tests pass

---

### Phase 5: Config Loading (JSON-Native + Preprocessors)

**Goal:** Load JSON build files natively. Support non-JSON formats (CUE, EDN, TOML, etc.) via external config preprocessors — commands that transform their format into JSON. mu never parses anything but JSON.

**Files:**
- `internal/config/types.go` — Go types (JSON struct tags only)
- `internal/config/loader.go` — Project root discovery, JSON loading, BUILD file merging
- `internal/config/validate.go` — Go-level validation (required fields, patterns)
- `internal/config/preprocess.go` — Config preprocessor dispatch

**Go types:**
```go
type ProjectConfig struct {
    Targets      []Target      `json:"targets,omitempty"`
    Toolchains   []Toolchain   `json:"toolchains,omitempty"`
    Services     []Service     `json:"services,omitempty"`
    Triggers     []Trigger     `json:"triggers,omitempty"`
    Cache        *CacheConfig  `json:"cache,omitempty"`
    Plugins      []PluginDef   `json:"plugins,omitempty"`
    Preprocessor *Preprocessor `json:"preprocessor,omitempty"`
}

type Target struct {
    Name      string         `json:"target"`
    Toolchain string         `json:"toolchain"`
    Sources   []string       `json:"sources"`
    Deps      []string       `json:"deps,omitempty"`
    Config    map[string]any `json:"config,omitempty"`
}

type Service struct {
    Name      string            `json:"service"`
    Runtime   string            `json:"runtime"`
    DependsOn map[string]string `json:"depends_on,omitempty"`
    Config    ServiceConfig     `json:"config"`
}

type Trigger struct {
    Name     string   `json:"trigger"`
    Watch    []string `json:"watch"`
    Run      string   `json:"run,omitempty"`
    Then     ThenSpec `json:"then,omitempty"`
    Debounce string   `json:"debounce,omitempty"`
}

type Toolchain struct {
    Name   string          `json:"toolchain"`
    Plugin string          `json:"plugin"`
    Config ToolchainConfig `json:"config"`
}

// Preprocessor defines how to convert non-JSON build files to JSON
type Preprocessor struct {
    Extension string   `json:"extension"`  // e.g. ".cue", ".edn", ".toml"
    Command   []string `json:"command"`    // e.g. ["cue", "export", "--out", "json"]
}
```

**Config preprocessor design:**

mu natively loads `mu.json` and `BUILD.json`. For non-JSON formats, the project root `mu.json` declares a preprocessor:

```json
{
  "preprocessor": {
    "extension": ".cue",
    "command": ["cue", "export", "--out", "json"]
  },
  "cache": {
    "backends": [
      {"type": "disk", "path": "~/.mu/cache"}
    ]
  }
}
```

When mu encounters a `BUILD.cue` file, it runs:
```
cue export --out json BUILD.cue
```
...captures stdout, and parses the resulting JSON. The preprocessor is just an external command — same philosophy as build plugins.

**Alternative: minimal mu.json root with preprocessor-only projects:**

For a project that uses CUE everywhere, the `mu.json` at the root can be just the preprocessor declaration. All actual config lives in `.cue` files:

```json
{
  "preprocessor": {"extension": ".cue", "command": ["cue", "export", "--out", "json"]}
}
```

**For pure JSON projects:** No preprocessor needed. `mu.json` + `BUILD.json` files work directly.

**Project root discovery:**
- Walk up from cwd looking for `mu.json` — this is always the root marker
- `mu.json` is always JSON (it's the one file mu always reads directly)
- If `preprocessor` is declared, find `BUILD.<ext>` files and run them through the command
- If no preprocessor, find `BUILD.json` files

**Preprocessor protocol:**
- mu passes the file path as the last argument to the command
- Command writes JSON to stdout
- Exit code 0 = success, anything else = error
- Stderr captured and displayed on failure

**Validation:**
- Go-level validation after JSON parsing (regardless of original format)
- Required fields: `target` and `toolchain` on targets, `service` and `runtime` on services
- Target names match `^//` pattern
- Service runtime is `"docker"` or `"host"`
- Debounce is valid duration string
- No duplicate target/service names

**Example preprocessor commands for common formats:**

| Format | Preprocessor command | Tool |
|--------|---------------------|------|
| CUE | `["cue", "export", "--out", "json"]` | `cue` CLI |
| EDN | `["bb", "-e", "(-> *input* slurp edn/read-string json/generate-string println)"]` | Babashka |
| TOML | `["yj", "-tj"]` | `yj` (format converter) |
| YAML | `["yj", "-yj"]` | `yj` |
| Jsonnet | `["jsonnet"]` | `jsonnet` CLI |
| Dhall | `["dhall-to-json"]` | `dhall` CLI |
| KCL | `["kcl", "run", "--format", "json"]` | `kcl` CLI |

The beauty: mu doesn't need to know about any of these. Users bring their own preprocessor. Any language that can produce JSON can be a config format for mu.

**Tests:**
- Load valid BUILD.json → correct Go types
- Preprocessor: CUE file → JSON → correct Go types (mock preprocessor in test)
- Preprocessor failure (exit code 1) → clear error with stderr
- Preprocessor not found → clear error ("command not found: cue")
- Project root discovery (walk up looking for mu.json)
- No preprocessor + BUILD.json → direct JSON loading
- Validation catches missing required fields
- Validation catches invalid target names
- Glob expansion in sources
- Multiple BUILD files merged
- Preprocessor with file that produces invalid JSON → parse error

**Acceptance criteria:**
- [ ] JSON loading native (no external deps for config)
- [ ] Preprocessor dispatch: detect extension, run command, capture JSON
- [ ] Preprocessor error handling (exit code, stderr, missing command)
- [ ] Project root discovery via mu.json
- [ ] Recursive BUILD file discovery
- [ ] Go-level validation
- [ ] Glob expansion for source patterns
- [ ] Tests pass

---

### Phase 6: `mu build` — End-to-End

**Goal:** Wire everything together into a working `mu build //target` command.

**Files:**
- `cmd/mu/main.go` — CLI entry point
- `cmd/mu/build.go` — `mu build` command
- `internal/coordinator/coordinator.go` — Orchestrates config → plugins → DAG → execute

**Coordinator flow:**
1. Load config — JSON native or via preprocessor (Phase 5)
2. Discover plugins (Phase 3)
3. Bootstrap toolchains if needed (Phase 4)
4. For target and transitive deps:
   a. Resolve toolchain plugin
   b. Send `plan` request with target info + dep artifacts + toolchain artifacts
   c. Receive action subgraph
5. Merge all subgraphs into global DAG
6. Execute DAG (Phase 2) with CAS integration (Phase 1)
7. Report results

**CLI:**
```
mu build //target           # build single target
mu build //target1 //target2  # build multiple
mu build //...              # build all targets
mu build --jobs 4           # limit parallelism
mu build --no-cache         # skip cache reads
mu build --verbose          # show plugin I/O
```

**Output format:**
```
mu build //services/api:binary
  bootstrapping...
  ✓ //toolchains:go          cached
  building...
  ● //lib/auth               built (0.6s)
  ● //lib/db                 built (0.4s)
  ● //services/api:binary    built (1.8s)
  3 targets built in 2.1s (1 cached)
```

**Tests:**
- End-to-end: build a Go binary using mock Go plugin
- Transitive deps resolved correctly
- Cache hit → instant completion
- Incremental rebuild (one file changed)
- Build failure → clear error with action stderr
- `--no-cache` forces rebuild
- `//...` builds all targets

**Example plugin (Go toolchain in Babashka):**
- Create `plugins/go/plugin.bb` — reference implementation
- Responds to `:discover` and `:plan`
- Emits `go build` actions

**Acceptance criteria:**
- [ ] `mu build //target` works end-to-end
- [ ] Transitive dependency resolution
- [ ] Cache integration (skip cached, store new)
- [ ] Progress output
- [ ] Error reporting with action context
- [ ] Example Go plugin working
- [ ] Tests pass

---

### Phase 7: OCI Cache Backend

**Goal:** Push/pull CAS blobs and action results to OCI registries.

**Files:**
- `internal/cas/oci/oci.go` — `OCIStore` implementing `Store`
- `go.mod` — Add `oras.land/oras-go/v2`, `github.com/opencontainers/image-spec`

**OCIStore design:**
- Blobs stored as OCI blobs (content-addressed by sha256 — same digest)
- Action results stored as OCI manifests tagged by action key hash
- Each action output is a layer in the manifest
- Custom media types: `application/vnd.mu.blob.v1`, `application/vnd.mu.action-result.v1+json`

**Auth:** Read from Docker credential helpers (`~/.docker/config.json`). Use `oras-go` built-in auth client.

**Key behaviors:**
- `Put`: push blob to registry, handle "already exists" gracefully
- `Get`: fetch blob by digest
- `Has`: check existence by digest (HEAD request)
- `PutActionResult`: create OCI manifest with output layers, tag by action key
- `GetActionResult`: fetch manifest by tag, parse action result from config blob

**Tests:**
- Push/pull blob roundtrip (use local registry in test — `registry:2` container)
- Action result manifest creation
- Cache miss returns nil
- Auth from Docker config
- Network error handling (registry down)
- Already-exists dedup

**Acceptance criteria:**
- [ ] OCIStore implements Store interface
- [ ] Blob push/pull working
- [ ] Action result as OCI manifest
- [ ] Auth via Docker credential helpers
- [ ] Error handling for network issues
- [ ] Tests pass (with local registry)

---

### Phase 8: Tiered Cache

**Goal:** Chain local + OCI backends with read-repair and write-through.

**Files:**
- `internal/cas/tiered.go` — `TieredStore`

**TieredStore design:**
```go
type TieredStore struct {
    backends []Store
    writable []bool
    repair   bool  // read-repair: backfill faster tiers on hit
}
```

**Behaviors:**
- `Get`: check backends in order (L1 local → L2 OCI). On hit in Ln (n>0), async backfill to L1..L(n-1) if `repair=true`.
- `Put`: write to all writable backends. Local write is synchronous. OCI write is async (write-back) with retry.
- `Has`: check L1 first, then L2 if miss.
- `GetActionResult` / `PutActionResult`: same tiered logic.

**Config (JSON example — works in any format):**
```json
{
  "cache": {
    "backends": [
      {"type": "disk", "path": "~/.mu/cache", "max_size": "10GB"},
      {"type": "oci", "registry": "registry.example.com/mu-cache", "read": true, "write": true}
    ],
    "read_repair": true,
    "write_through": true
  }
}
```

**Tests:**
- L1 hit → return immediately, no L2 call
- L1 miss, L2 hit → return from L2, async backfill to L1
- L1 miss, L2 miss → cache miss
- Put writes to all writable backends
- OCI write failure → log error, don't fail build
- Config parsing from CUE

**Acceptance criteria:**
- [ ] TieredStore composes multiple backends
- [ ] Read-repair from slow → fast tiers
- [ ] Write-through to all writable backends
- [ ] Async OCI writes don't block build
- [ ] Configurable via mu.cue
- [ ] Tests pass

---

### Phase 9: Service Manager

**Goal:** Start, stop, restart, and healthcheck long-running services.

**Files:**
- `internal/service/manager.go` — `ServiceManager`
- `internal/service/docker.go` — Docker runtime
- `internal/service/host.go` — Host-native runtime

**ServiceManager design:**
- Topological sort services by `depends_on`
- Start in order, waiting for conditions (`:healthy` or `:started`)
- Healthcheck loop per service (HTTP, TCP, or command)
- Restart: stop → start (with new artifacts if rebuilt)
- Shutdown: SIGTERM → wait 10s → SIGKILL (or `docker stop`)

**Docker runtime:**
- Use Docker Engine API (`github.com/docker/docker/client`)
- Create shared `mu-dev` network
- Create containers with mounts, ports, env, healthchecks
- Service names as container names (for DNS)
- Labels: `mu.service=<name>`, `mu.managed=true`

**Host runtime:**
- `os/exec.Cmd` with `Process.Signal`
- Stdout/stderr piped to console with `[service-name]` prefix
- Env vars from service config
- Healthcheck: run command or HTTP GET

**Healthcheck:**
- Types: `http` (GET endpoint), `tcp` (port check), `command` (run command)
- Retry with configurable interval and max retries
- Report healthy/unhealthy to ServiceManager

**.env support:**
- Load `.env` file from project root
- Interpolate `${VAR}` in service configs
- Merge with service-level `environment` (service wins)

**Tests:**
- Start/stop host service
- Healthcheck pass and fail (with retry)
- Startup order respects depends_on
- Restart with new artifacts
- Graceful shutdown on context cancel
- .env loading and interpolation
- Docker tests (integration, requires Docker daemon)

**Acceptance criteria:**
- [ ] Docker and host runtimes implemented
- [ ] Dependency-ordered startup with healthcheck conditions
- [ ] Healthcheck with retry
- [ ] Restart preserving order constraints
- [ ] Graceful shutdown
- [ ] .env support
- [ ] Service logs with prefixed output
- [ ] Tests pass

---

### Phase 10: File Watching + Triggers

**Goal:** Watch files, debounce, trigger rebuilds, restart services.

**Files:**
- `internal/trigger/watcher.go` — fsnotify wrapper
- `internal/trigger/trigger.go` — Trigger orchestration

**Watcher design:**
- Wrap `fsnotify.Watcher`
- Watch directories containing trigger source patterns
- Filter: ignore `.git`, `node_modules`, editor temp files (`.swp`, `.#*`, `~` suffix), `Chmod` events
- Debounce: configurable per-trigger (default 300ms)
- Coalesce multiple file changes into single trigger event

**Trigger orchestration:**
- On trigger fire:
  1. Hash changed files
  2. Invalidate affected actions in DAG
  3. Re-plan affected targets via plugins
  4. Execute invalidated subgraph
  5. If success and trigger has `then.restart`: restart named services
  6. If failure: log error, keep watching, notify if configured

**Tests:**
- File change detected within debounce window
- Multiple rapid changes coalesced
- Editor temp files ignored
- Rebuild triggers on source change
- Service restart after successful rebuild
- Failed rebuild keeps services running
- Watcher handles directory creation/deletion

**Acceptance criteria:**
- [ ] fsnotify-based file watching
- [ ] Debouncing with configurable delay
- [ ] Editor temp file filtering
- [ ] Trigger → rebuild → restart pipeline
- [ ] Error handling (rebuild failure doesn't crash watcher)
- [ ] Tests pass

---

### Phase 11: `mu dev` — Dev Workflow Orchestration

**Goal:** Compose build + services + triggers into `mu dev`.

**Files:**
- `cmd/mu/dev.go` — `mu dev` command

**`mu dev` flow:**
1. Load config
2. Discover plugins
3. Bootstrap toolchains if needed
4. Build all targets referenced by services and triggers
5. Start services in dependency order (Phase 9)
6. Activate triggers (Phase 10)
7. Enter watch loop
8. On Ctrl-C: stop triggers → stop services → exit

**Output:**
```
mu dev
  ● //services/api:binary    built (1.2s, cached)
  ● //dev:postgres            healthy (postgres:16 on :5432)
  ● //dev:api                 running (on :8080)
  ◎ watching 47 files for //dev:rebuild-on-change
  ◎ watching 38 files for //dev:test-on-change

  ∆ internal/handler/auth.go changed
  ● //services/api:binary    rebuilt (0.8s)
  ↻ //dev:api                restarted
  ● //services/api:test      3 passed (2.1s)
```

**Tests:**
- End-to-end dev mode with mock services
- File change → rebuild → restart cycle
- Ctrl-C graceful shutdown
- Service failure during startup
- Build failure during watch

**Acceptance criteria:**
- [ ] `mu dev` starts services and watches files
- [ ] Rebuild → restart cycle works
- [ ] Graceful shutdown
- [ ] Clear status output
- [ ] Tests pass

---

### Phase 12: Utility Commands + Polish

**Goal:** Supporting commands and overall polish.

**Commands:**
- `mu clean` — GC the local CAS (`--max-age`, `--max-size`)
- `mu plugin list` — List registered plugins with capabilities
- `mu plugin discover <name>` — Show plugin's discover response
- `mu verify` — Re-hash CAS and report corruption
- `mu version` — Print mu version

**Polish:**
- Color output (respect `NO_COLOR` env var)
- `--verbose` flag for plugin I/O logging
- `--json` flag for machine-readable output
- Meaningful exit codes (0 success, 1 build failure, 2 config error)
- Man page / `--help` text

**Acceptance criteria:**
- [ ] All utility commands working
- [ ] Color output with NO_COLOR support
- [ ] Verbose mode shows plugin communication
- [ ] Exit codes documented and consistent
- [ ] --help text for all commands

## Alternative Approaches Considered

**Bazel/Buck2 rules instead of plugins:** Rejected — forces Starlark/Python, couples build logic to coordinator.

**gRPC plugin protocol instead of stdin/stdout:** Rejected for v1 — adds proto dependency, harder for Babashka plugins. Can add as alternative transport later.

**HashiCorp go-plugin:** Considered — too opinionated for our multi-language plugin model. Custom stdin/stdout protocol is simpler and more flexible.

**go-containerregistry instead of oras-go:** Rejected — known issues with OCI v1.1 `artifactType` field. oras-go is purpose-built for custom artifacts.

**Built-in config format loaders (CUE/EDN/TOML compiled in):** Rejected — contradicts mu's philosophy of emptiness. mu should be as agnostic about its config language as it is about build toolchains. JSON is the common denominator (stdlib, zero deps). Non-JSON formats are handled by config preprocessors — external commands that produce JSON. This means any language that outputs JSON can be a config format for mu, without mu needing to know about it.

**Nix-style derivations:** Philosophically aligned but too coupled to Nix store model.

## Acceptance Criteria

### Functional Requirements

- [ ] `mu build //target` builds targets via plugins and caches results
- [ ] `mu dev` starts services, watches files, rebuilds and restarts on change
- [ ] Plugins communicate via stdin/stdout (EDN or JSON)
- [ ] Bootstrap fetches and verifies toolchains
- [ ] CAS caches to local disk and OCI registries
- [ ] JSON build files natively, any format via config preprocessors
- [ ] Docker and host-native service runtimes
- [ ] File watching with debounced triggers

### Non-Functional Requirements

- [ ] Parallel action execution saturates available CPU cores
- [ ] Cache hit builds complete in <100ms
- [ ] Plugin spawn + discover completes in <1s
- [ ] File change → rebuild starts within debounce window + 100ms

### Quality Gates

- [ ] All packages have tests
- [ ] `go vet` and `staticcheck` clean
- [ ] No data races (`go test -race`)
- [ ] Example Go plugin (Babashka) working end-to-end

## Dependencies & Prerequisites

- Go 1.25+ (already in go.mod)
- Babashka (for example plugins — not required for mu itself)
- Docker (for container service runtime and OCI cache integration tests)
- An OCI-compatible registry for integration testing (can use local `registry:2`)
- Config preprocessor tools are optional, user-provided (e.g., `cue` CLI for CUE, `bb` for EDN, `yj` for TOML/YAML)

## Risk Analysis & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Preprocessor UX friction | Medium | Medium | Ship example preprocessor configs for common formats |
| EDN library unmaintained | Low | Low | Stable, complete. Fork if critical bug found |
| OCI registry compatibility issues | Medium | Medium | Test against multiple registries (Docker Hub, ghcr.io, local) |
| fsnotify kqueue FD limits on macOS | Medium | Low | Document limit, add fallback warning |
| Plugin protocol needs iteration | High | Medium | Version field in protocol, fail clearly on mismatch |
| Scope creep | High | High | Strict phase boundaries, v1 = honor system hermeticity |

## Future Considerations (Post-v1)

- **Strict sandboxing:** landlock/seccomp on Linux, sandbox-exec on macOS
- **Remote execution:** Execute actions on remote machines (Bazel RE API compatible?)
- **SBOM/provenance:** Attach attestations to OCI artifacts
- **Plugin distribution:** OCI artifacts as plugin packages
- **Build telemetry:** OpenTelemetry export, cache hit rate dashboard
- **TUI:** Rich terminal UI with live action graph visualization
- **BLAKE3:** Migrate from SHA-256 for faster hashing
- **Incremental plugin protocol:** Plugins can cache state between plan calls

## References & Research

### Internal
- Brainstorm: `docs/brainstorms/2026-02-18-mu-v1-brainstorm.md`
- Design exploration: `idea.md`

### Libraries
- oras-go v2: https://oras.land/docs/client_libraries/go/
- CUE Go API: https://cuelang.org/docs/tutorial/loading-cue-go-api/
- olympos.io/encoding/edn: https://pkg.go.dev/olympos.io/encoding/edn
- fsnotify: https://github.com/fsnotify/fsnotify
- OCI image spec: https://github.com/opencontainers/image-spec
- google/renameio: https://pkg.go.dev/github.com/google/renameio

### Prior Art
- Buck2: https://buck2.build/docs/
- Bazel Remote APIs: https://github.com/bazelbuild/remote-apis
- ORAS (OCI Registry As Storage): https://oras.land/
- CUE language: https://cuelang.org/
