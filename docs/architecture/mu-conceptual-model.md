# mu Conceptual Model

**Date:** 2026-03-12
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

Artifacts are stored in the CAS (Content-Addressable Store), which may be
backed by local disk, an OCI registry, or both.

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
binaries and files that were produced by bootstrapping the toolchain. This is
how a Go plugin knows where the `go` binary lives.

### 4. Target

A declared build unit. Defined in `BUILD.json` or `mu.json`. A target says
"I want to build *this thing* using *this plugin*."

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

A toolchain is a **target whose plugin is "bootstrap."**

There is no separate "toolchain" primitive at the conceptual level. A toolchain
is just a target that produces artifacts (binaries, libraries, headers) which
other targets consume. The only thing that makes it special is:

- Its plugin is `"bootstrap"` — a well-known name that mu handles internally
- Its config specifies where to download the toolchain (`url`, `sha256`)
- Its output artifacts are passed to downstream plugins as `toolchain_artifacts`

```
# This is a toolchain — it's just a target with plugin "bootstrap"
{
  "target": "//toolchains:go",
  "toolchain": "bootstrap",
  "config": {
    "version": "1.25.7",
    "url": "https://go.dev/dl/go1.25.7.linux-amd64.tar.gz",
    "sha256": "abc123...",
    "strip_prefix": "go"
  }
}

# This is a regular target — it depends on the toolchain above
{
  "target": "//cmd/server",
  "toolchain": "go",
  "sources": ["main.go"],
  "deps": ["//toolchains:go"]
}
```

## The Bootstrap Plugin

`"bootstrap"` is a reserved plugin name. When mu encounters a target with
`"toolchain": "bootstrap"`, it handles the planning internally rather than
delegating to an external process. The built-in logic:

1. **Fetch** — download the URL, verify SHA-256 (`network: true`)
2. **Extract** — unpack .tar.gz, .tar.xz, .zip (with optional `strip_prefix`)
3. **Verify** — run the binary with `--version` to confirm it works
4. **Register** — store all extracted files as content-addressed artifacts in CAS

This is equivalent to what an external bootstrap plugin would do via the NDJSON
protocol, but baked into the mu binary for zero-dependency bootstrapping. You
need tools before you can run tools — so the tool-fetcher must be built in.

The `MU_BOOTSTRAP` environment variable allows overriding this with an external
executable, should anyone need custom bootstrap logic.

```
                    ┌──────────────────────────────────┐
                    │         mu (the binary)          │
                    │                                  │
  mu.json ─────────►  Config Loader                   │
  BUILD.json ──────►  (parse, validate, merge)        │
                    │         │                        │
                    │         ▼                        │
                    │  ┌─────────────────────────┐     │
                    │  │     Coordinator          │     │
                    │  │                          │     │
                    │  │  1. Resolve target graph │     │
                    │  │  2. Plan each target:    │     │
                    │  │     ┌─────────────────┐  │     │
                    │  │     │ if bootstrap:   │  │     │
                    │  │     │   built-in      │──┼──► fetch, extract, verify
                    │  │     │ else:           │  │     │
                    │  │     │   ask plugin    │──┼──► NDJSON stdin/stdout
                    │  │     └─────────────────┘  │     │
                    │  │  3. Merge action DAG     │     │
                    │  │  4. Execute DAG          │     │
                    │  └─────────────────────────┘     │
                    │         │                        │
                    │         ▼                        │
                    │  ┌──────────────┐                │
                    │  │     CAS      │                │
                    │  │  (artifacts) │                │
                    │  └──────────────┘                │
                    └──────────────────────────────────┘
```

## How It All Connects: A Walkthrough

Consider a project that builds a Go server using a bootstrapped Go toolchain.

**Configuration:**
```json
// mu.json
{
  "plugins": [
    {"name": "go", "command": ["bb", "plugins/go/plugin.bb"]}
  ]
}

// BUILD.json
{
  "targets": [
    {
      "target": "//toolchains:go",
      "toolchain": "bootstrap",
      "config": {
        "version": "1.25.7",
        "url": "https://go.dev/dl/go1.25.7.linux-amd64.tar.gz",
        "sha256": "abc123...",
        "strip_prefix": "go"
      }
    },
    {
      "target": "//cmd/server",
      "toolchain": "go",
      "sources": ["cmd/server/main.go"],
      "deps": ["//toolchains:go"],
      "config": {"output": "server"}
    }
  ]
}
```

**Execution flow of `mu build //cmd/server`:**

```
Step 1: Resolve target graph
         //cmd/server depends on //toolchains:go
         Topological order: [//toolchains:go, //cmd/server]

Step 2: Plan //toolchains:go
         Plugin is "bootstrap" → use built-in logic
         Produce actions:
           fetch:    download URL, verify SHA-256       [network: true]
           extract:  unpack .tar.gz, strip prefix "go"
           verify:   run bin/go --version
           register: store all files in CAS
         Result: artifacts {"bin/go": "sha256:def...", ...}

Step 3: Plan //cmd/server
         Plugin is "go" → send plan request to go.bb plugin
         Include toolchain_artifacts: {"bin/go": "sha256:def...", ...}
         Plugin responds with actions:
           build: go build -o server ./cmd/server

Step 4: Merge all actions into global DAG
         fetch → extract → verify → register → build

Step 5: Execute DAG
         Run actions in dependency order, skip cached ones
         Store outputs in CAS

Step 6: Done
         //cmd/server produces artifact "server" at sha256:xyz...
```

**Cache behavior on second run:**

```
Step 2: Plan //toolchains:go
         CAS already has artifacts for Go 1.25.7 at these hashes
         → all bootstrap actions are cache hits, skip

Step 3: Plan //cmd/server
         Toolchain artifacts unchanged, sources unchanged
         → build action is a cache hit, skip

Step 5: Nothing to execute
         "0 actions executed, 5 cached"
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
│  Bootstrap plugin          (fetch/extract/store) │
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
│  Which toolchains to bootstrap                   │
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

Toolchains bootstrapped via the `"bootstrap"` plugin are fully content-addressed.
The Go 1.25.7 toolchain is not "the go binary on my PATH" — it's a specific
set of artifacts at specific SHA-256 digests, downloaded from a specific URL
whose checksum was verified. When a plugin receives `toolchain_artifacts`, it
references binaries by their CAS digest, not by filesystem path. Any change to
the toolchain (version bump, different platform build) changes the digests,
which changes the cache keys of all downstream actions, which forces a rebuild.

The `network: true` flag on actions marks the boundary between hermetic and
non-hermetic. Only bootstrap fetch actions (and explicitly marked plugin
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
