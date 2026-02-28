# Research: Go Toolchain Plugin Design

**Date:** 2026-02-28
**Status:** Research complete
**Issue:** mu-y26

## Summary

This document investigates how a Go toolchain plugin should work within mu's plugin protocol. Go has unique characteristics — a module system with MVS, a built-in build cache (GOCACHE), CGO interop, build tags, and a compile+link pipeline — that all need careful mapping onto mu's action-graph model.

## Key Design Decision: Opaque vs Decomposed

The central question is whether the Go plugin should:

**A) Treat `go build` as a single opaque action** — simple, leverages Go's own cache, but hides parallelism from mu.

**B) Decompose into per-package compile + link actions** — maximum cache sharing and parallelism, but duplicates Go's own logic.

**Recommendation: Option A (opaque) with GOCACHEPROG integration.**

Go's build toolchain is deeply integrated — the compiler, linker, module resolver, and cache all cooperate through internal protocols (`importcfg` files, build IDs, ActionIDs). Decomposing this into per-package actions would mean reimplementing Go's import graph resolution, `importcfg` generation, and build ID propagation in Babashka. The Rust plugin can decompose because `rustc` is designed to be invoked per-crate externally. Go's `cmd/compile` and `cmd/link` are not designed for external orchestration.

The pragmatic middle ground: treat `go build` as a coarse-grained action at the mu level, but use **GOCACHEPROG** (Go 1.24+) to bridge Go's internal cache with mu's CAS. This gives us content-addressed caching and remote cache sharing without fighting Go's toolchain.

## Question-by-Question Analysis

### 1. How should the plugin handle Go's module system (go.mod/go.sum)?

**Approach: Pre-build resolution, lockfile-as-input.**

Go uses Minimum Version Selection (MVS) — deterministic by design, no solver needed. The module graph is fully determined by `go.mod` files. The plugin should:

- Declare `go.mod` and `go.sum` as inputs to every action
- **Not** perform module resolution itself — let `go build` handle it
- Require modules to be downloaded before the build action runs (via a separate `go mod download` action or a pre-build phase)
- Use `GOMODCACHE` to point at a content-addressed or hermetic module cache

The module download can be a separate action:

```
action "mod-download" {
  command: ["go", "mod", "download"]
  inputs:  {go.mod, go.sum}
  outputs: [$GOMODCACHE]  // or a hash manifest of downloaded modules
  network: true           // module download needs network access
}
```

This cleanly separates the network-accessing phase (non-hermetic) from the compilation phase (hermetic). The Rust plugin in idea.md punts on Cargo resolution similarly: `{:resolve {:error "use cargo outside the build graph"}}`.

**Vendoring alternative:** For strict hermeticity, the plugin can support `GOFLAGS=-mod=vendor` where a `vendor/` directory is an explicit input. This avoids network access entirely and makes the module graph a static filesystem artifact.

### 2. Should it call `go build` as a single action or decompose into compile+link?

**Single action, for these reasons:**

1. **Go's compiler expects to manage its own import graph.** Running `cmd/compile` directly requires generating `importcfg` files that map import paths to `.a` archive locations. Go's build tool does this internally; reproducing it externally is fragile and version-dependent.

2. **Go's build cache is already content-addressed.** The ActionID is SHA-256 over source contents, compiler version, flags, GOOS/GOARCH, etc. This is the same model as mu's CAS. Duplicating it adds complexity without benefit.

3. **`go build -x` shows the pipeline is tightly coupled.** Compile actions reference work directories (`$WORK/b001/`) and use internal build IDs for incremental validation. These are not stable external interfaces.

4. **The GOCACHEPROG protocol (Go 1.24+) provides the integration point.** mu can implement a GOCACHEPROG daemon that bridges GET/PUT operations to mu's CAS. This gives us remote caching and content-addressed storage without decomposing Go's build pipeline.

The plugin emits a build action like:

```json
{
  "actions": [
    {
      "id": "build",
      "command": ["go", "build", "-o", "api-server", "./cmd/api"],
      "inputs": {
        "go.mod": "<digest>",
        "go.sum": "<digest>",
        "cmd/api/main.go": "<digest>",
        "internal/...": "<digest>"
      },
      "outputs": ["api-server"],
      "env": {
        "GOOS": "linux",
        "GOARCH": "amd64",
        "CGO_ENABLED": "0",
        "GOMODCACHE": "/cas/modules",
        "GOCACHEPROG": "/path/to/mu-cache-bridge"
      }
    }
  ],
  "declared_outputs": {
    "executable": "api-server"
  }
}
```

### 3. How does it handle CGO?

CGO fundamentally changes the build: it introduces a C toolchain dependency (compile and link time), system headers, and potentially shared libraries. The plugin needs to detect and handle this.

**Detection:** The plugin should scan source files for `import "C"` or check for `.c`/`.cc`/`.cpp` files in the package. Alternatively, accept a `cgo: true` config flag.

**When CGO is enabled, additional inputs:**
- C compiler binary (`CC`, typically `gcc` or `clang`) — from a C toolchain registered in mu
- C/C++ source files (`.c`, `.cc`, `.h`, `.hpp`)
- System libraries referenced via `#cgo LDFLAGS`
- `pkg-config` metadata if used
- Environment: `CGO_CFLAGS`, `CGO_LDFLAGS`, `CGO_CPPFLAGS`, `CC`, `CXX`

**Cross-toolchain composition:** This is where mu's artifact-type system shines. The Go plugin declares it consumes `native_library` artifacts. A C toolchain plugin (or the Go plugin itself, via CGO) can produce them. The coordinator stitches the graphs:

```json
{
  "target": "//lib/bindings",
  "toolchain": "go",
  "sources": ["bindings.go"],
  "deps": ["//lib/native:crypto"],
  "config": {"cgo": true}
}
```

The `//lib/native:crypto` target (built by a C plugin) produces a `native_library` artifact. The Go plugin receives it as a dep artifact and maps it to `CGO_LDFLAGS` or mounts it in the build context.

**Recommendation:** Default to `CGO_ENABLED=0` for simplicity and cross-compilation friendliness. When CGO is needed, require explicit opt-in via config and declare the C toolchain as a dependency:

```json
{
  "config": {
    "cgo": true,
    "cc_toolchain": "//toolchains:clang"
  }
}
```

### 4. What about build tags and conditional compilation?

Build tags and GOOS/GOARCH constraints affect which source files are compiled. The plugin must account for them in action inputs and cache keys.

**File-name conventions:** Go automatically includes/excludes files based on `_GOOS.go`, `_GOARCH.go`, `_GOOS_GOARCH.go` suffixes. The plugin doesn't need to implement this — `go build` handles it. But the plugin must ensure that GOOS/GOARCH are part of the action's environment and thus the cache key.

**Custom build tags** (`-tags "integration production"`): These must be part of the config and flow into the action's environment/command:

```json
{
  "config": {
    "tags": ["production", "netgo"],
    "goos": "linux",
    "goarch": "amd64"
  }
}
```

The plugin translates to: `go build -tags "production netgo"` and sets `GOOS`/`GOARCH` in env.

**Cache key impact:** Since GOOS, GOARCH, CGO_ENABLED, and build tags all affect which files are compiled and how, they must all be inputs to mu's action cache key. Conveniently, `go build` already includes all of these in its own ActionID, so the opaque-action approach gets correct caching for free.

### 5. How does `go test` fit?

idea.md references a separate `go-test` toolchain. This makes sense because:

- `go test` compiles a **different binary** than `go build` — it includes `_test.go` files and a synthesized `main()` that calls `testing.Main`
- Test results have their own cache (separate from build cache)
- Tests may need network access, test fixtures, or integration dependencies

**Recommendation: `go-test` as a separate toolchain plugin** (or a mode of the `go` plugin).

The `go-test` plugin produces two actions:

```json
{
  "actions": [
    {
      "id": "test-compile",
      "command": ["go", "test", "-c", "-o", "pkg.test", "./pkg/..."],
      "inputs": {"...": "..."},
      "outputs": ["pkg.test"]
    },
    {
      "id": "test-run",
      "command": ["./pkg.test", "-test.v", "-test.timeout=60s"],
      "inputs": {"pkg.test": "{action:test-compile}"},
      "outputs": ["test-results.json"],
      "depends_on": ["test-compile"]
    }
  ],
  "declared_outputs": {
    "test_binary": "pkg.test",
    "test_results": "test-results.json"
  }
}
```

Separating compile from run enables:
- Caching the test binary across runs (only recompile when source changes)
- Running the same test binary with different flags or in different environments
- Parallel test execution across packages (each package gets its own test binary)

**Alternative:** Single `go test -json ./...` action. Simpler, but loses the compile/run separation. Fine as a starting point.

**Config options:**
```json
{
  "config": {
    "race": true,
    "count": 1,
    "timeout": "120s",
    "tags": ["integration"],
    "run": "TestFoo.*",
    "cover": true
  }
}
```

### 6. What artifacts should it declare?

The Go plugin should declare these artifact types:

| Artifact Type | Produced By | Description |
|---|---|---|
| `executable` | `go build` (package main) | Compiled binary |
| `go_archive` | `go build` (library package) | `.a` package archive (rare — usually intermediate) |
| `test_binary` | `go test -c` | Test executable |
| `test_results` | test execution | JSON test output |
| `go_module_cache` | `go mod download` | Downloaded module tree |
| `c_archive` | `go build -buildmode=c-archive` | Static C library + header |
| `c_shared` | `go build -buildmode=c-shared` | Shared library (`.so`/`.dll`) |

**Cross-language composition artifacts:**

- **Producing for C consumers:** `go build -buildmode=c-archive` or `c-shared` produces a `.a`/`.so` plus a C header. These map to mu's `native_library` artifact type, consumable by a C/C++ plugin.
- **Consuming from C producers:** When CGO is enabled, the Go plugin consumes `native_library` artifacts from upstream C/Rust targets.

**Discover response:**

```json
{
  "name": "go",
  "version": "0.1.0",
  "consumes": ["source:go", "native_library", "protobuf_schema", "go_module_cache"],
  "produces": ["executable", "go_archive", "native_library", "c_archive", "c_shared"],
  "config_schema": {
    "output": {"type": "string"},
    "goos": {"type": "string", "default": "linux"},
    "goarch": {"type": "string", "default": "amd64"},
    "cgo": {"type": "boolean", "default": false},
    "tags": {"type": "array", "items": "string"},
    "gcflags": {"type": "string"},
    "ldflags": {"type": "string"},
    "buildmode": {"type": "string", "default": "exe"},
    "race": {"type": "boolean", "default": false},
    "trimpath": {"type": "boolean", "default": true}
  }
}
```

### 7. How does Go's internal build cache interact with mu's CAS?

This is the most interesting question. Go's GOCACHE and mu's CAS are both content-addressed stores keyed by action hashes. They're doing the same thing at different granularities.

**Three strategies:**

**A) Ignore GOCACHE, let Go rebuild from scratch each time.**
Simple but wasteful. Every mu cache miss triggers a full Go rebuild even if GOCACHE would have had hits. Only viable for small projects.

**B) Persist GOCACHE as a mu artifact.**
Store the entire GOCACHE directory as an input/output of the build action. On cache miss, restore GOCACHE from CAS before running `go build`, then store the updated GOCACHE after. This gives Go its incremental compilation while mu handles the coarse-grained caching.

```json
{
  "id": "build",
  "inputs": {
    "...sources...": "...",
    ".gocache": "<digest-of-previous-gocache-snapshot>"
  },
  "outputs": ["api-server", ".gocache"],
  "env": {"GOCACHE": "$WORK/.gocache"}
}
```

**C) GOCACHEPROG bridge (recommended for Go 1.24+).**
Implement mu's CAS as a GOCACHEPROG backend. Go's build tool sends GET/PUT requests for individual compilation artifacts; the bridge stores/retrieves them in mu's CAS. This is the most elegant solution:

- Fine-grained cache sharing (per-package, not per-target)
- Remote cache works across developers/CI
- No need to snapshot/restore the entire GOCACHE
- Uses Go's official extension point

The GOCACHEPROG protocol is JSON Lines over stdin/stdout:
```json
// GET request from go build:
{"ID": 1, "Command": "get", "ActionID": "<base64>"}
// Response (hit):
{"ID": 1, "OutputID": "<base64>", "Size": 4096, "DiskPath": "/cas/blobs/sha256:..."}
// Response (miss):
{"ID": 1, "Miss": true}

// PUT request from go build:
{"ID": 2, "Command": "put", "ActionID": "...", "OutputID": "...", "BodySize": 4096}
// followed by raw bytes on stdin
```

The mu-cache-bridge binary translates these to mu CAS `Put`/`Get` calls. This can be a small Go binary shipped alongside the plugin.

**Recommendation:** Start with strategy B (GOCACHE-as-artifact) for simplicity. Graduate to strategy C (GOCACHEPROG) when mu targets Go 1.24+ and the CAS has a stable API.

## Proposed Plugin Structure

```
plugins/go/
├── plugin.bb           # Main plugin (discover + plan)
├── go-test-plugin.bb   # Test toolchain plugin
└── README.md
```

Or as a single plugin with mode dispatch:

```clojure
(defn discover []
  {:name "go"
   :version "0.1.0"
   :consumes #{:source/go :native-library :protobuf-schema}
   :produces #{:executable :go-archive :native-library :c-archive}
   :config-schema {...}})

(defn plan-build [{:keys [target config deps]}]
  (let [sources (:sources target)
        output (or (:output config) (last (str/split (:name target) #"/")))
        cgo? (get config :cgo false)
        env (cond-> {"GOOS" (get config :goos "linux")
                     "GOARCH" (get config :goarch "amd64")
                     "CGO_ENABLED" (if cgo? "1" "0")
                     "GOMODCACHE" "{toolchain:gomodcache}"}
              cgo? (assoc "CC" "{dep:cc_toolchain}"))]
    {:actions
     [(merge
        {:id :mod-download
         :command ["go" "mod" "download"]
         :inputs {"go.mod" :source "go.sum" :source}
         :outputs ["gomodcache.marker"]
         :env env
         :network true}
        )
      {:id :build
       :command (into ["go" "build"
                       "-trimpath"
                       (str "-o=" output)]
                      (when-let [tags (:tags config)]
                        [(str "-tags=" (str/join "," tags))])
                      ["."])
       :inputs (merge
                 (into {} (map #(vector % :source) sources))
                 {"go.mod" :source
                  "go.sum" :source})
       :outputs [output]
       :depends-on [:mod-download]
       :env env}]
     :declared-outputs {:executable output}}))
```

## Open Questions / Future Work

1. **Multi-package builds:** Should a single mu target map to one Go package or an entire module? The `./...` wildcard suggests module-level is more natural for Go.

2. **Go workspace mode (`go.work`):** Go 1.18+ workspaces allow multiple modules in a single build. The plugin should eventually support `go.work` as an input.

3. **`go generate`:** Should codegen be a separate action or folded into the build? Likely separate — it's a pre-build step that produces `.go` files consumed by the compile action.

4. **Race detector:** `-race` changes the compiler output significantly (different runtime). It should be a config flag that alters the cache key.

5. **GOCACHEPROG implementation:** Designing the mu-cache-bridge binary is a natural follow-up task once the CAS API stabilizes.

6. **Hermetic module proxy:** For full hermeticity, modules should be fetched through a mu-controlled proxy (like Athens or GOPROXY=file://...) rather than the public proxy.
