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
  impure:     bool                    # side effects: run on the host, never cached
  toolchains: []string                # scratch toolchains unpacked into the sandbox
  host_paths: []string                # host files/dirs granted read+exec in the sandbox
}
```

An action's **cache key** is derived from its command, sorted input digests
and paths, environment, toolchain files, granted host paths and flags (see
`mu guide cache`). If the key matches a previous execution, the outputs are
restored from cache and the action is skipped. Pure actions run in the
sandbox (see [Sandbox Execution](#sandbox-execution)); impure actions run on
the host and are never cached.

Actions are the only things mu actually executes. Everything else is
bookkeeping to produce a graph of actions.

### 3. Plugin

An external executable that speaks NDJSON over stdin/stdout. Plugins are how
mu learns what actions to run. A plugin implements these methods:

**discover** (required) — "what can you do?"
```json
-> {"method": "discover"}
<- {"name": "go", "version": "0.1.0", "protocol_version": 1,
    "consumes": ["go_source"], "produces": ["executable"],
    "capabilities": ["discover", "plan"]}
```

**plan** (optional; required for build plugins) — "given this
target, what actions should I run?"
```json
-> {"method": "plan",
    "target": {"name": "//cmd/server", "toolchain": "go",
               "sources": ["main.go"], "config": {"output": "server"}},
    "toolchain_artifacts": {"bin/go": "sha256:abc..."}}
<- {"actions": [{"id": "build", "command": ["go", "build", "-o", "server"],
    "inputs": {"src": "main.go"}, "outputs": ["server"]}],
    "declared_outputs": {"executable": "server"}}
```

**resolve_secret** (optional) — resolve a secret reference to its value
**store_secret** (optional) — write a value to a secret backend

There are no other methods: the former `observe` (drift detection) and
`advise` (lifecycle observer) methods were removed.

A plugin can be written in any language. It reads JSON lines from stdin,
dispatches on `method`, and writes JSON responses to stdout. That's the
entire contract.

Plugins receive **toolchain artifacts** in plan requests — the content-addressed
binaries and files that were produced by the scratch build. This is how a Go
plugin knows where the `go` binary lives.

### 4. Target

A declared build unit. Defined in `mu.cue`. A target says "I want to build
*this thing* using *this plugin*."

```
Target = {
  name:           string              # e.g. "//cmd/server"
  toolchain:      string              # which plugin handles this (e.g. "go")
  sources:        []string            # input files
  deps:           []string            # other targets this depends on
  config:         map[any]            # plugin-specific configuration
  sealed_inputs:  map[name -> ref]    # secret references resolved at runtime
  sealed_outputs: map[name -> ref]    # secret capture destinations
}
```

The coordinator resolves targets into a dependency-ordered list, asks each
target's plugin to plan it, and merges the resulting action subgraphs into a
single global DAG.

Target sealed maps are capability availability, not secret values. Each action
must explicitly claim exact declared refs/modes; all inputs must be used and
every output must have one writer. The only implicit claim: a plan with exactly
one action and no claims gets the target's maps. Planning validates this routing
without resolving providers. Resolution happens only when an action executes.

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
- Actions name it in `toolchains`, and mu unpacks its artifacts into their
  sandbox; a plugin of the same name also receives them as `toolchain_artifacts`

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

Scratch only installs pre-compiled tools. Source-only tools are built in a
normal target, and downstream targets consume its output.

```
                    ┌──────────────────────────────────┐
                    │         mu (the binary)          │
                    │                                  │
  mu.cue ────────►  Config Loader                    │
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

**Configuration (`mu.cue`):**
```cue
package mu

toolchains: [
    {
        toolchain: "bb"
        from:      "scratch"
        config: {
            version: "1.12.216"
            url:     "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-linux-amd64.tar.gz"
            sha256:  "..."
        }
    },
    {
        toolchain: "go"
        from:      "scratch"
        config: {
            version:      "1.25.7"
            url:          "https://go.dev/dl/go1.25.7.linux-amd64.tar.gz"
            sha256:       "abc123..."
            strip_prefix: "go"
        }
    },
]
plugins: [
    {name: "go", script: "plugins/go/plugin.bb"},
]
targets: [{
    target:    "//cmd/server"
    toolchain: "go"
    sources: ["cmd/server/main.go", "go.mod", "go.sum"]
    config: {output: "server", pkg: "./cmd/server"}
}]
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
         Plugin responds with one action:
           build: go build -trimpath -o=server ./cmd/server
                  [toolchains: [go], network: true (go.mod present)]

Step 5: Merge all actions into global DAG

Step 6: Execute DAG
         Run actions in dependency order, skip cached ones
         build is pure: it runs in a sandbox holding the go toolchain and
         its declared sources, and downloads its modules itself
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
│  Sealed inputs/outputs     (secret lifecycle)    │
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
computed from what the action declares: command, input digests and paths,
environment, toolchain files and granted host paths. If any of it changes,
the cache misses and the action re-executes. If nothing changes, the cached
result is used regardless of wall-clock time or filesystem state.

The sandbox is what makes the declarations trustworthy: a pure action runs
where it can see only what the key covers, plus the platform's read-only
system directories (which are not hashed). Two gaps are deliberate. A
granted host directory enters the key by path only, so its contents are
trusted. And a `network: true` action can fetch anything; the key covers
the request, not the response, so such actions should pin what they fetch
(go.sum, lockfiles).

Toolchains built from scratch are fully content-addressed. The Go 1.26.2
toolchain is not "the go binary on my PATH" — it's a specific set of artifacts
at specific SHA-256 digests, downloaded from a specific URL whose checksum was
verified. An action that names it in `toolchains` gets those files unpacked
into its sandbox, and every digest, mode and symlink enters its key. Any
change to the toolchain (version bump, different platform build) changes the
keys of the actions that use it, which forces a rebuild.

`network: false` is the default, and in the sandbox it is enforced by the
kernel under Seatbelt and Linux namespaces. Impure actions (convergence,
anything with `sealed_outputs`) are outside this model entirely: they run on
the host with its filesystem and network and are never cached.

## Sandbox Execution

Every pure action executes inside a **sandbox** — a temporary directory that
serves as an isolated filesystem for the build step. The sandbox lifecycle:

1. Create a temp directory with standard subdirs: `bin/`, `work/`, `out/`,
   `tmp/`, `home/`
2. Unpack the named toolchains' artifacts from CAS into the root (e.g.
   `bin/go`), with their file modes and in-tree symlinks
3. Place each file input in `work/` at its project-relative path, fetched
   from CAS (upstream outputs) or the project and checked against its digest
4. Execute the command in `work/<work_dir>` with `bin/` first on `PATH`,
   `TMPDIR` set to `tmp/` and `HOME` to `home/`
5. Copy declared outputs from `out/` or the working directory back to the
   project
6. Clean up the temp directory

### Isolation Levels

mu auto-detects the strongest isolation available on the current platform
and records it per action in the build manifest (`isolation`):

- **Linux (Namespace):** User, mount, PID, IPC, UTS, and (unless
  `network: true`) network namespaces. pivot_root into the sandbox root,
  minimal `/dev`, system directories and `host_paths` bind-mounted
  read-only, everything read-only except `/work`, `/out`, `/tmp`, `/home`.
  No root required. This path builds for Linux but has not yet been run on
  a Linux host.
- **macOS (Seatbelt):** `sandbox-exec` with a deny-default SBPL profile.
  Writes are confined to the sandbox; data reads and exec are limited to the
  sandbox, granted `host_paths` and system trees. Network is denied unless
  `network: true`.
- **Copy (fallback):** Cross-platform temp directory with restricted `PATH`
  and `env`. Relative paths resolve inside the sandbox, but nothing stops the
  command from reading or writing absolute host paths or using the network;
  mu warns once per process when it falls back to this.

See `mu guide sandbox` for the exact profile and mounts.

## Plugin Runtime

Plugins are external executables that speak NDJSON over stdin/stdout. The
bundled plugins come in two kinds:

- **Babashka scripts** (`docker`, `doppler`, `go`, `k8s`, `lint`, `sops`,
  `terraform`): mu builds Babashka from scratch as a toolchain, downloading a
  specific version by URL and SHA-256, and runs each `plugin.bb` with the
  cached binary, so the runtime is hermetic and version-pinned.
- **Go binaries** (`file`, `keypair-gen`, `pass`, `remote-exec`,
  `remote-file`), written with the Go SDK (`mu guide sdk`). The binary is not
  committed; build it into the plugin directory first, or let
  `mu plugin install` build a catalog package.

Either way:

- Plugin authors implement `discover`, plus `plan` when the plugin builds
  targets, and may add optional methods
  (`resolve_secret`, `store_secret`)
- Any language works; a plugin only has to speak the protocol
- Plugin directories are deterministically bundled and stored in CAS, and
  third-party plugins are distributed through the GitHub release catalog
  (`mu plugin install`)
