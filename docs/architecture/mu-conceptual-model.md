# mu Conceptual Model

**Date:** 2026-03-13
**Status:** Working document

## Core Premise

mu is an empty vessel. It knows nothing about programming languages, compilers,
or toolchains. Its entire job is:

1. Build a DAG of actions
2. Execute those actions (in parallel, respecting dependencies)
3. Cache the results (content-addressed, by input hash)

Everything else — what to build, how to build it, what tools to use — comes
from plugins.

## The Five Primitives

mu has exactly five externally-exposed abstractions. They form a layered
dependency chain:

```
  Artifact
     ^
     |
   Action ──────── uses ──────► Artifact (as input)
     ^                           produces ──► Artifact (as output)
     |
   Target ──────── planned by ──► Plugin
     |
     └── may depend on ──► Toolchain (which is just a Target)
```

### 1. Artifact

A content-addressed blob. Identified by its SHA-256 digest. Immutable.

An artifact has no opinion about what it contains — it could be a Go binary, a
.tar.gz archive, a JSON manifest, a compiled object file, or a downloaded
executable. mu doesn't care. It's just bytes with a hash.

```
Artifact = {
  digest: "sha256:<hex>"    # identity
  content: []byte           # opaque to mu
}
```

Artifacts are stored in the CAS (Content-Addressable Store), backed by OCI
layout locally and OCI registries remotely.

### 2. Action

A hermetic transformation: input artifacts in, output artifacts out. An action
is a command that mu executes in a controlled environment.

```
Action = {
  id:         string                  # unique within the DAG
  command:    []string                # the executable + arguments
  inputs:     map[name -> Digest]     # named input artifacts (resolved to hashes)
  outputs:    []string                # declared output file paths
  depends_on: []string                # other action IDs (ordering constraints)
  env:        map[string -> string]   # environment variables (explicit, hermetic)
  network:    bool                    # whether network access is allowed
}
```

An action's **cache key** is derived from its command, sorted input digests,
and environment. If the key matches a previous execution, the outputs are
restored from cache and the action is skipped.

Actions are the only things mu actually executes. Everything else is
bookkeeping to produce a graph of actions.

### 3. Plugin

An external executable that speaks NDJSON over stdin/stdout. Plugins are how
mu learns what actions to run. A plugin implements two methods:

**discover** — "what can you do?"
```json
-> {"method": "discover"}
<- {"name": "go", "version": "0.1.0", "protocol_version": 1,
    "consumes": ["go_source"], "produces": ["executable"]}
```

**plan** — "given this target, what actions should I run?"
```json
-> {"method": "plan",
    "target": {"name": "//cmd/server", "toolchain": "go",
               "sources": ["main.go"], "config": {"output": "server"}},
    "toolchain_artifacts": {"bin/go": "sha256:abc..."}}
<- {"actions": [{"id": "build", "command": ["go", "build", "-o", "server"],
    "inputs": {"src": "main.go"}, "outputs": ["server"]}],
    "declared_outputs": {"executable": "server"}}
```

A plugin can be written in any language. It reads JSON lines from stdin,
dispatches on `method`, and writes JSON responses to stdout. That's the
entire contract.

Plugins receive **toolchain artifacts** in plan requests — the content-addressed
binaries and files that were produced by the scratch build. This is how a Go
plugin knows where the `go` binary lives.

### 4. Target

A declared build unit. Defined in `mu.json`. A target says "I want to build
*this thing* using *this plugin*."

```
Target = {
  name:      string         # e.g. "//cmd/server"
  toolchain: string         # which plugin handles this (e.g. "go")
  sources:   []string       # input files
  deps:      []string       # other targets this depends on
  config:    map[any]        # plugin-specific configuration
}
```

The coordinator resolves targets into a dependency-ordered list, asks each
target's plugin to plan it, and merges the resulting action subgraphs into a
single global DAG.

Targets can depend on other targets. When target A depends on target B, B's
actions are planned and executed first, and B's output artifacts are available
to A's plugin during planning.

### 5. Toolchain

A toolchain is a **target built from scratch.**

There is no separate "toolchain" primitive at the conceptual level. A toolchain
is just a target that produces artifacts (binaries, libraries, headers) which
other targets consume. The only thing that makes it special is:

- Its `from` field is `"scratch"` — mu handles it internally
- Its config specifies where to download the toolchain (`url`, `sha256`)
- Its output artifacts are passed to downstream plugins as `toolchain_artifacts`

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
  ],
  "targets": [
    {
      "target": "//cmd/server",
      "toolchain": "go",
      "sources": ["main.go"],
      "config": {"output": "server"}
    }
  ]
}
```

## The Scratch Environment

`"scratch"` is the base environment that all toolchains are built from. When mu
encounters a toolchain with `"from": "scratch"`, it handles the build
internally using built-in logic:

1. **Fetch** — download the URL, verify SHA-256 (`network: true`)
2. **Extract** — unpack .tar.gz, .tar.xz, .zip (with optional `strip_prefix`)
3. **Verify** — run the binary with `--version` to confirm it works
4. **Register** — store all extracted files as content-addressed artifacts in CAS

This is baked into the mu binary because you need tools before you can run
tools — the tool-fetcher must be built in.

The `MU_SCRATCH` environment variable allows overriding this with an external
executable, should anyone need custom scratch build logic.

```
                    ┌──────────────────────────────────┐
                    │         mu (the binary)          │
                    │                                  │
  mu.json ─────────►  Config Loader                   │
                    │  (parse, validate)               │
                    │         │                        │
                    │         ▼                        │
                    │  ┌─────────────────────────┐     │
                    │  │     Coordinator          │     │
                    │  │                          │     │
                    │  │  1. Build from scratch   │     │
                    │  │     (toolchains)         │     │
                    │  │  2. Start plugins        │     │
                    │  │  3. Resolve target graph │     │
                    │  │  4. Plan each target:    │     │
                    │  │     ask plugin ──────────┼──► NDJSON stdin/stdout
                    │  │  5. Merge action DAG     │     │
                    │  │  6. Execute DAG          │     │
                    │  └─────────────────────────┘     │
                    │         │                        │
                    │         ▼                        │
                    │  ┌──────────────┐                │
                    │  │     CAS      │                │
                    │  │  (OCI store) │                │
                    │  └──────────────┘                │
                    └──────────────────────────────────┘
```

## How It All Connects: A Walkthrough

Consider a project that builds a Go server using a Go toolchain built from
scratch.

**Configuration (`mu.json`):**
```json
{
  "toolchains": [
    {
      "toolchain": "bb",
      "from": "scratch",
      "config": {
        "version": "1.12.216",
        "url": "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-linux-amd64.tar.gz",
        "sha256": "..."
      }
    },
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
  ],
  "plugins": [
    {"name": "go", "script": "plugins/go/plugin.bb"}
  ],
  "targets": [
    {
      "target": "//cmd/server",
      "toolchain": "go",
      "sources": ["cmd/server/main.go", "go.mod", "go.sum"],
      "config": {"output": "server"}
    }
  ]
}
```

**Execution flow of `mu build //cmd/server`:**

```
Step 1: Build toolchains from scratch
         bb:  download babashka tarball, verify SHA-256, extract, register
         go:  download Go tarball, verify SHA-256, extract, register
         Both cached in CAS as content-addressed artifacts

Step 2: Start plugins
         Resolve bb binary from CAS → run plugin script
         Send discover request to go plugin

Step 3: Resolve target graph
         //cmd/server has no target deps
         Topological order: [//cmd/server]

Step 4: Plan //cmd/server
         Plugin is "go" → send plan request to go.bb plugin
         Include toolchain_artifacts: {"bin/go": "sha256:def...", ...}
         Plugin responds with actions:
           mod-download: go mod download [network: true]
           build: go build -o server ./cmd/server

Step 5: Merge all actions into global DAG
         mod-download → build

Step 6: Execute DAG
         Run actions in dependency order, skip cached ones
         Store outputs in CAS

Step 7: Done
         //cmd/server produces artifact "server" at sha256:xyz...
```

**Cache behavior on second run:**

```
Step 1: Build toolchains from scratch
         CAS already has artifacts for bb and Go at these hashes
         → all scratch build actions are cache hits, skip

Step 4: Plan //cmd/server
         Toolchain artifacts unchanged, sources unchanged
         → build action is a cache hit, skip

Step 6: Nothing to execute
         "0 actions executed, 2 cached"
```

## What Is Intrinsic vs. What Comes From Outside

```
┌─────────────────────────────────────────────────┐
│              Intrinsic to mu                     │
│                                                  │
│  DAG construction          (build a graph)       │
│  DAG execution             (run actions)         │
│  CAS storage               (store/retrieve)      │
│  Action caching            (skip if cached)      │
│  Plugin protocol           (NDJSON over stdio)   │
│  Scratch environment       (fetch/extract/store) │
│  Sandbox execution         (hermetic rootfs)     │
│                                                  │
├─────────────────────────────────────────────────┤
│              Comes from plugins                  │
│                                                  │
│  What a "Go build" means                         │
│  What a "Rust compile" means                     │
│  What a "Docker image" means                     │
│  How to link across languages                    │
│  What sources to watch                           │
│  What outputs to produce                         │
│                                                  │
├─────────────────────────────────────────────────┤
│              Comes from config                   │
│                                                  │
│  Which plugins to use                            │
│  Which toolchains to build from scratch          │
│  Which targets to build                          │
│  Dependency relationships between targets        │
│  Plugin-specific configuration                   │
│                                                  │
└─────────────────────────────────────────────────┘
```

## Hermeticity Model

Every action declares its inputs and outputs explicitly. The cache key is
computed from the command, input digests, and environment — nothing else.
If anything changes, the cache misses and the action re-executes. If nothing
changes, the cached result is used regardless of wall-clock time, filesystem
state, or anything else.

Toolchains built from scratch are fully content-addressed. The Go 1.25.7
toolchain is not "the go binary on my PATH" — it's a specific set of artifacts
at specific SHA-256 digests, downloaded from a specific URL whose checksum was
verified. When a plugin receives `toolchain_artifacts`, it references binaries
by their CAS digest, not by filesystem path. Any change to the toolchain
(version bump, different platform build) changes the digests, which changes the
cache keys of all downstream actions, which forces a rebuild.

The `network: true` flag on actions marks the boundary between hermetic and
non-hermetic. Only scratch build fetch actions (and explicitly marked plugin
actions) may access the network. Everything else is sealed.

```
network boundary
─────────────────────────────────────────────
     fetch (network: true)
         │
         ▼
     extract, verify, compile, link, test ...
     (network: false — hermetic from here down)
```

## Sandbox Execution

Actions execute inside a **sandbox** — a temporary directory that serves as an
isolated filesystem for the build step. The sandbox lifecycle:

1. Create a temp directory with standard subdirs: `bin/`, `work/`, `out/`, `tmp/`
2. Unpack toolchain artifacts from CAS into the rootfs (e.g. `bin/go`)
3. Copy/hardlink source files into `work/`
4. Execute the command with `PATH` restricted to `bin/`, `TMPDIR` to `tmp/`
5. Extract declared outputs from `work/` back to the project
6. Clean up the temp directory

### Isolation Levels (Progressive)

**Current: copy sandbox.** Cross-platform, no root required. The sandbox is a
temp directory with controlled `PATH` and `env`. Actions cannot *accidentally*
read host files, but there is no OS-level enforcement preventing it.

**Planned: OS-level isolation.**

- **Linux:** User namespaces + `pivot_root` + overlayfs. No root required.
  The toolchain OCI image becomes the overlay lower dir, sources are
  bind-mounted. Network blocked via network namespace unless `network: true`.
- **macOS:** `sandbox-exec` with profiles restricting filesystem reads/writes
  to the sandbox directory only.
- **Network:** Linux network namespaces; macOS `sandbox-exec` network deny
  profile. Only actions with `network: true` get access.

## Plugin Runtime

Plugins are `.bb` (Babashka) scripts that speak NDJSON over stdin/stdout.
Rather than requiring `bb` on the host's PATH, mu builds Babashka from scratch
as a toolchain — downloading a specific version by URL and SHA-256 — and uses
the cached binary to run plugin scripts. This means:

- Plugins are distributed as plain `.bb` files (no compilation needed)
- The bb runtime is hermetic and version-pinned
- Plugin authors only need to implement `discover` and `plan` methods
- Users who prefer a different plugin language can define their own toolchain
