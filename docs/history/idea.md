# 💬 What would a graph based build…

## User

What would a graph based build system like bazel look like if you made it totally language agnostic. The build system itself would only handle coordination of tools.

## Claude

This is a really fun design space. Let me think through what you'd get if you stripped a build system down to pure graph coordination and pushed *everything* else out to external toolchains.

## The Core Primitive

The system only knows about **artifacts** (content-addressed blobs) and **actions** (hermetic transformations from input artifacts to output artifacts). It has no built-in knowledge of what a "compile" or "link" or "test" means. An action is just:

```
action {
  inputs:  [artifact_hash...]
  outputs: [declared_path...]
  command: [string...]  // opaque to the system
  env:     {k: v...}    // hermetic, explicit
}
```

The build system's entire job is: resolve the DAG, check the cache, schedule actions with maximal parallelism, and enforce hermeticity (sandboxed execution, no network, no undeclared inputs).

## Toolchain as a First-Class Concept

Since the system knows nothing about languages, you'd need a **toolchain protocol** — essentially a contract that any language ecosystem implements to participate:

```
toolchain {
  name: "rust"
  resolve: (source_files, deps) -> action_graph
  # Given these inputs, tell me what actions to run
}
```

This is the key inversion: instead of the build system containing rules for each language, each toolchain is a **plugin that emits action graphs**. The build system is just the executor. Bazel already half-does this with its `rule()` mechanism, but it forces you through Starlark and its own dependency model. A truly agnostic system would let the toolchain be an arbitrary binary that speaks a protocol — something like:

1. Build system sends: "here are the declared sources and dep artifacts"
2. Toolchain responds: "here's the action subgraph to produce the outputs"
3. Build system merges that into the global DAG, caches, schedules

This is closer to how **Buck2** works with its `bxl` and how **Please** approaches things, but taken further.

## What You Gain

**Composability across ecosystems.** A Go binary that links a Rust FFI library that code-gens from a Protobuf schema is just three toolchain plugins emitting subgraphs that get stitched together at the artifact level. The build system doesn't care — it just sees hashes in, hashes out.

**The cache is universal.** Because everything is content-addressed actions, you get remote caching and remote execution for free across all languages. No special integration per ecosystem.

**Toolchains can evolve independently.** The Rust toolchain plugin can adopt a new compiler version without the build system knowing or caring.

## The Hard Problems

**Dependency resolution is where it gets gnarly.** Every language ecosystem has its own dependency model (Cargo's semver resolution, npm's tree, Go's MVS). You have a few options:

- Let each toolchain handle its own resolution, then materialize the result as pinned artifacts (basically what Bazel does with `rules_go` etc., and it's ugly)
- Define a universal dependency protocol and force toolchains to map onto it (ambitious, probably too constraining)
- Treat package managers as a separate layer that runs *before* the build system, producing a lockfile that the toolchain plugin consumes (pragmatic, what most people actually do)

**Cross-language interfaces** are the other big one. The build system can stitch artifact graphs together, but *someone* has to know that a `.so` produced by Rust can be linked by a C toolchain. You'd need some kind of **artifact type system** — not types in the PL sense, but metadata on artifacts that lets toolchains declare "I produce X" and "I consume X":

```
artifact_type "native_library" {
  properties: {abi, arch, os, link_type}
}
```

Toolchains register their inputs/outputs against these types, and the build system can do basic compatibility checking at graph construction time rather than failing at link time.

**Hermeticity enforcement** is also tricky when you're totally agnostic. Bazel can sandbox pretty aggressively because it controls the execution model. If toolchains are arbitrary binaries, you're looking at something like landlock/seccomp on Linux or sandbox-exec on macOS to enforce that actions only touch declared inputs.

## Where This Gets Interesting for You

Given your IDP work — this pattern maps really well onto the "paved road" concept. The build system becomes infrastructure, toolchain plugins become the curated, compliant paths, and teams can't accidentally break hermeticity because the system enforces it. You could imagine the toolchain protocol including compliance metadata (SBOM generation, license scanning, vulnerability checking) as first-class action outputs rather than bolted-on post-hoc.

And there's a connection to your tuplespace/coordination interests — the action DAG with content-addressed artifacts is essentially a monotonic knowledge base. You could model it as a datalog program: "artifact X exists when actions A, B, C have all completed and produced matching hashes." That's basically what Buck2 does internally with its DICE computation framework.

The closest things to this vision in practice are probably **Buck2** (Meta), **Shake** (Haskell, very principled), and **Justbuild** (which is explicitly minimal and content-addressed). None of them have fully nailed the "toolchain as external protocol" part though — there's still too much coupling.

## User

Wait so plugins. How are they defined? Who defines them? What’s the interface they provide and obey?

## Claude

Great question, this is really the crux of the whole design. Let me work through it concretely.

## The Plugin is Just a Binary That Speaks a Protocol

A toolchain plugin is an executable — could be written in anything — that implements a request/response protocol over stdin/stdout (or a Unix socket, or gRPC, whatever). The build system doesn't care what it's written in. It's the same pattern as LSP for editors or CNI for Kubernetes networking: a well-defined interface boundary where the two sides know nothing about each other's internals.

The protocol has a small number of verbs:

### 1. **Discover**: "What can you do?"

```json
// build system asks:
{"method": "discover"}

// plugin responds:
{
  "name": "rust",
  "version": "0.3.1",
  "consumes": ["source:rust", "native_library", "protobuf_schema"],
  "produces": ["native_library", "executable", "rust_metadata"],
  "config_schema": { /* JSON schema for per-target config */ }
}
```

This is registration. The build system now knows that when it encounters a target that needs to produce a `native_library` from `source:rust` files, this plugin is a candidate. The `config_schema` tells the build system what knobs the plugin accepts so it can validate build files before ever invoking the plugin.

### 2. **Plan**: "Given these inputs, what actions do you need?"

This is the big one. The build system has resolved which targets exist and what their declared dependencies are. Now it asks the plugin to turn that into concrete actions:

```json
// build system sends:
{
  "method": "plan",
  "target": {
    "name": "//lib/crypto",
    "sources": ["lib/crypto/aes.rs", "lib/crypto/sha.rs"],
    "config": {"edition": "2021", "opt_level": 2}
  },
  "deps": [
    {
      "target": "//lib/utils",
      "artifacts": {
        "rust_metadata": "sha256:abc123...",
        "native_library": "sha256:def456..."
      }
    }
  ],
  "toolchain_artifacts": {
    "rustc": "sha256:aaa...",
    "stdlib": "sha256:bbb..."
  }
}
```

```json
// plugin responds with an action subgraph:
{
  "actions": [
    {
      "id": "compile_aes",
      "command": ["rustc", "--crate-type=rlib", "--edition=2021",
                  "-O2", "--extern", "utils={dep:utils_meta}", 
                  "aes.rs", "-o", "aes.o"],
      "inputs": {
        "aes.rs": "sha256:...",
        "utils_meta": "sha256:abc123..."
      },
      "outputs": ["aes.o"],
      "env": {"RUSTC_BOOTSTRAP": "0"}
    },
    {
      "id": "compile_sha",
      "command": ["rustc", /* ... */ "sha.rs", "-o", "sha.o"],
      "inputs": {"sha.rs": "sha256:...", "utils_meta": "sha256:abc123..."},
      "outputs": ["sha.o"]
    },
    {
      "id": "link_rlib",
      "command": ["ar", "rcs", "libcrypto.a", "aes.o", "sha.o"],
      "inputs": {"aes.o": "{action:compile_aes}", "sha.o": "{action:compile_sha}"},
      "outputs": ["libcrypto.a", "libcrypto.rmeta"],
      "depends_on": ["compile_aes", "compile_sha"]
    }
  ],
  "declared_outputs": {
    "native_library": "libcrypto.a",
    "rust_metadata": "libcrypto.rmeta"
  }
}
```

Notice what happened: the plugin knows *everything* about how Rust compilation works — that you compile individual crate roots, that `rmeta` files are needed for downstream type checking, that you need `ar` for static linking. The build system knows *nothing* about any of that. It just received a subgraph of actions with declared inputs, outputs, and dependencies. It can hash them, check the cache, and schedule them.

### 3. **Resolve** (optional): "Figure out my transitive dependencies"

This is the contentious one. Some plugins might need to do dependency resolution (Cargo-style semver solving, Go MVS, etc.):

```json
{"method": "resolve", "manifest": "Cargo.toml", "lockfile": "Cargo.lock"}
```

The plugin responds with a pinned dependency graph that the build system can then use for planning. But honestly, you might want to keep this *outside* the plugin protocol entirely — let it be a separate pre-build step that produces a lockfile, and the plugin just reads the lockfile during `plan`. Mixing resolution into the build graph creates nasty problems with reproducibility and caching.

## Who Writes Plugins?

Three tiers:

**Tier 1 — The build system project ships reference plugins** for major ecosystems (C/C++, Rust, Go, Python, Java, TypeScript). These are the "batteries included" path. They live in their own repos, versioned independently, but are well-tested against the protocol.

**Tier 2 — Language communities write their own.** The Zig community writes a Zig plugin, the Elixir community writes an Elixir plugin. The protocol is public and stable, so they can do this without any coordination with the build system maintainers. This is where the LSP analogy is strongest — Microsoft didn't need to write every language server, they just needed to nail the protocol.

**Tier 3 — Internal/bespoke plugins.** This is the IDP angle. Your org has a custom code generator, or a proprietary compiler, or a compliance step. You write a thin plugin that wraps it. The `plan` response is usually trivial — one or two actions — but now it participates in the full build graph with caching and parallelism for free.

## What the Build System Guarantees to the Plugin

This is the other side of the contract:

- **Hermetic execution environment.** When the build system runs an action, only declared inputs are visible. The plugin can rely on this — it doesn't need to worry about stale files or ambient state.
- **Content-addressed storage.** All artifact references are by hash. The plugin never deals with file paths in some global workspace — it gets hashes and produces hashes.
- **Parallel scheduling.** Independent actions from the same plugin's subgraph will be parallelized. The plugin expresses ordering constraints through `depends_on`, and the build system handles the rest.
- **Caching.** If the action key (command + input hashes + env) matches a cache entry, the action is skipped. The plugin doesn't implement caching — it's automatic.

## What the Plugin Guarantees to the Build System

- **Determinism.** Same inputs → same action graph. The `plan` response must be a pure function of its inputs. If you call it twice with the same request, you get the same actions.
- **Completeness.** All inputs must be declared. If `rustc` implicitly reads something, the plugin must declare it as an input. Violations get caught by the sandbox, but the plugin shouldn't rely on that.
- **Typed outputs.** The `declared_outputs` map artifact type names to files. This is how cross-language composition works — a downstream C plugin knows it can consume the `native_library` output.

## The Subtlety: Toolchains Are Also Artifacts

Here's where it gets elegant. The Rust compiler itself is just another artifact in the content-addressed store. So you can have a *bootstrap plugin* that knows how to fetch/build toolchains:

```json
{
  "method": "plan",
  "target": {"name": "//toolchains/rust:1.78"},
  "sources": [],
  "config": {"version": "1.78.0", "host": "x86_64-linux"}
}
```

This produces actions that download and verify the Rust toolchain, and the resulting artifacts (`rustc`, `stdlib`) become inputs to all the other Rust actions. Now toolchain versioning is just part of the DAG — upgrade Rust by changing a hash, and everything downstream rebuilds.

## The Shape of Build Files

The build files themselves become quite thin, because they're not encoding build *logic*, just declaring intent:

```
target "//lib/crypto" {
  type     = "native_library"
  toolchain = "rust"
  sources  = glob("**/*.rs")
  deps     = ["//lib/utils"]
  config   = {edition = "2021", opt_level = 2}
}
```

The build system reads this, finds the `rust` plugin, sends it the `plan` request, and integrates the response into the global DAG. The build file is a declaration of *what* you want. The plugin decides *how*.

This is basically the Nix model applied to builds — the evaluator (build system) is separate from the builders (plugins), connected by a protocol of content-addressed derivations. Nix got a lot of this right conceptually but made it hard to extend because the protocol between the evaluator and builders is implicit and deeply tied to the Nix store model.

## User

Let’s say I want to implement the build tool in go and use babashka for writing plugins. What would that look like

## Claude

That's a great pairing. Go gives you a fast, concurrent DAG executor with easy binary distribution. Babashka gives you a data-oriented, expressive plugin layer that starts up in milliseconds (no JVM cold start) and treats EDN as a native data format — which is way nicer than shelling out to things that speak JSON.

Let me sketch this out concretely.

## The Protocol: EDN over stdin/stdout

Babashka's sweet spot is streaming EDN. So the protocol between the Go coordinator and the bb plugins is just EDN maps on stdin/stdout, one per line:

```clojure
;; coordinator sends:
{:method :plan
 :target {:name "//lib/crypto"
          :sources ["lib/crypto/aes.rs" "lib/crypto/sha.rs"]
          :config {:edition "2021" :opt-level 2}}
 :deps [{:target "//lib/utils"
         :artifacts {:rust-metadata "sha256:abc123"
                     :native-library "sha256:def456"}}]
 :toolchain-artifacts {:rustc "sha256:aaa"
                       :stdlib "sha256:bbb"}}

;; plugin responds:
{:actions [{:id :compile-aes
            :command ["rustc" "--crate-type=rlib" "aes.rs" "-o" "aes.o"]
            :inputs {"aes.rs" "sha256:..."
                     "utils.rmeta" "sha256:abc123"}
            :outputs ["aes.o"]
            :env {}}
           ,,,]
 :declared-outputs {:native-library "libcrypto.a"
                    :rust-metadata "libcrypto.rmeta"}}
```

EDN is perfect here because the plugin definitions are fundamentally *data* — declarative descriptions of what actions to run. You're not writing imperative build logic, you're assembling maps.

## The Go Coordinator

The core is maybe four packages:

```
cmd/forge/          # (or whatever you call it)
├── main.go
internal/
├── dag/            # DAG construction, topo sort, parallel scheduling
├── cas/            # content-addressed store (local + remote cache)
├── sandbox/        # hermetic execution (landlock/seccomp)
├── plugin/         # plugin lifecycle, EDN protocol
└── config/         # build file parsing
```

The plugin manager in Go spawns bb processes and talks to them:

```go
package plugin

import (
    "bufio"
    "os/exec"

    "olympos.io/encoding/edn"
)

type Plugin struct {
    cmd    *exec.Cmd
    stdin  io.Writer
    stdout *bufio.Scanner
}

type PlanRequest struct {
    Method string      `edn:"method"`
    Target Target      `edn:"target"`
    Deps   []DepInfo   `edn:"deps"`
    Tools  map[string]string `edn:"toolchain-artifacts"`
}

type PlanResponse struct {
    Actions []Action          `edn:"actions"`
    Outputs map[string]string `edn:"declared-outputs"`
}

func (p *Plugin) Plan(req PlanRequest) (*PlanResponse, error) {
    // encode request as EDN, write to stdin
    data, _ := edn.Marshal(req)
    p.stdin.Write(append(data, '\n'))

    // read response line
    p.stdout.Scan()
    var resp PlanResponse
    edn.Unmarshal([]byte(p.stdout.Text()), &resp)
    return &resp, nil
}
```

The plugin stays alive as a long-running process for the duration of the build — you're not paying bb startup cost per action, just once per plugin per build invocation. Babashka starts in ~10ms so even cold start is negligible, but keeping it warm avoids re-reading config.

The DAG executor is where Go really shines:

```go
package dag

func (d *DAG) Execute(ctx context.Context, workers int) error {
    ready := make(chan *Action, 256)
    results := make(chan ActionResult, 256)

    // fan out workers
    for i := 0; i < workers; i++ {
        go func() {
            for action := range ready {
                result := d.sandbox.Run(ctx, action)
                results <- result
            }
        }()
    }

    // schedule actions whose inputs are all satisfied
    pending := d.actions
    for len(pending) > 0 {
        res := <-results
        d.cas.Store(res.Outputs)
        // check what's now unblocked
        for _, a := range pending {
            if d.allInputsSatisfied(a) {
                // check cache first
                if cached, ok := d.cas.Lookup(a.CacheKey()); ok {
                    results <- cached
                } else {
                    ready <- a
                }
            }
        }
    }
}
```

## The Babashka Plugin Side

Here's where it gets nice. A Rust toolchain plugin in bb:

```clojure
#!/usr/bin/env bb
;; plugins/rust/plugin.bb

(require '[clojure.edn :as edn]
         '[clojure.string :as str])

(defn discover []
  {:name "rust"
   :version "0.1.0"
   :consumes #{:source/rust :native-library :protobuf-schema}
   :produces #{:native-library :executable :rust-metadata}
   :config-schema {:edition {:type :string :default "2021"}
                   :opt-level {:type :int :default 0}
                   :crate-type {:type :string :default "rlib"}}})

(defn plan-compile
  "Emit a compile action for a single source file."
  [{:keys [config toolchain-artifacts deps]} source-file]
  (let [stem (str/replace source-file #"\.rs$" "")
        extern-flags (mapcat (fn [{:keys [target artifacts]}]
                               ["--extern"
                                (str (last (str/split target #"/"))
                                     "=" (:rust-metadata artifacts))])
                             deps)]
    {:id (keyword (str "compile-" stem))
     :command (into ["rustc"
                     (str "--edition=" (:edition config))
                     (str "-O" (:opt-level config))
                     (str "--crate-type=" (:crate-type config))]
                    (concat extern-flags
                            [source-file "-o" (str stem ".o")]))
     :inputs (merge {source-file :source}
                    (into {} (map (fn [d] [(:rust-metadata (:artifacts d))
                                          :dep-metadata]) deps)))
     :outputs [(str stem ".o")]
     :env {}}))

(defn plan-link
  "Emit a link action that combines compiled objects."
  [target-name compile-actions]
  (let [objects (mapcat :outputs compile-actions)]
    {:id :link
     :command (into ["ar" "rcs" (str target-name ".a")] objects)
     :inputs (into {} (map (fn [a] [(first (:outputs a))
                                     {:from-action (:id a)}])
                           compile-actions))
     :outputs [(str target-name ".a")
               (str target-name ".rmeta")]
     :depends-on (mapv :id compile-actions)}))

(defn plan [{:keys [target] :as request}]
  (let [compiles (mapv #(plan-compile request %) (:sources target))
        link (plan-link (:name target) compiles)]
    {:actions (conj compiles link)
     :declared-outputs {:native-library (first (:outputs link))
                        :rust-metadata (second (:outputs link))}}))

(defn handle [msg]
  (case (:method msg)
    :discover (discover)
    :plan (plan msg)
    :resolve {:error "use cargo outside the build graph"}))

;; Main loop: read EDN from stdin, write EDN to stdout
(doseq [line (line-seq (java.io.BufferedReader. *in*))]
  (let [msg (edn/read-string line)
        resp (handle msg)]
    (println (pr-str resp))
    (flush)))
```

That's a complete plugin. ~60 lines. The data-oriented nature of Clojure means the plugin is basically just transforming maps — there's no framework, no base class, no registration boilerplate. You read a map, you return a map.

## Plugin Discovery and Registration

The build system needs to find plugins. Simple approach — a `plugins/` directory with a manifest:

```edn
;; forge.plugins.edn
[{:name "rust"
  :command ["bb" "plugins/rust/plugin.bb"]
  :artifact "sha256:..." ;; hash of the plugin itself for reproducibility
  }
 {:name "go"
  :command ["bb" "plugins/go/plugin.bb"]}
 {:name "protobuf"
  :command ["bb" "plugins/protobuf/plugin.bb"]}]
```

On startup, the Go coordinator reads this, starts each plugin, sends `:discover`, and builds a registry of what each plugin consumes/produces. Then when it parses build files and encounters a target, it matches the target's declared toolchain to a plugin.

## Build Files

You could write build files in EDN too, which keeps the whole system in one data format:

```edn
;; lib/crypto/BUILD.edn
[{:target "//lib/crypto"
  :toolchain :rust
  :sources ["aes.rs" "sha.rs"]
  :deps ["//lib/utils"]
  :config {:edition "2021"
           :opt-level 2}}

 {:target "//lib/crypto:test"
  :toolchain :rust
  :sources ["test.rs"]
  :deps ["//lib/crypto"]
  :config {:crate-type "bin"
           :test true}}]
```

Or if you want something more accessible to people who aren't Clojure-brained, use a thin HCL or TOML frontend that the Go side parses into the same internal representation.

## A More Interesting Plugin: Protobuf

This is where the cross-language composition shows up:

```clojure
#!/usr/bin/env bb
;; plugins/protobuf/plugin.bb

(defn discover []
  {:name "protobuf"
   :consumes #{:source/proto}
   :produces #{:source/go :source/rust :source/python
               :protobuf-descriptor}
   :config-schema {:languages {:type :set
                               :values #{:go :rust :python}}}})

(defn plan [{:keys [target]}]
  (let [langs (get-in target [:config :languages] #{:go})
        proto-files (:sources target)
        ;; one action per language output
        gen-actions
        (for [lang langs]
          {:id (keyword (str "gen-" (name lang)))
           :command (into ["protoc"
                           (str "--" (name lang) "_out=gen/" (name lang))]
                          proto-files)
           :inputs (into {} (map #(vector % :source) proto-files))
           :outputs [(str "gen/" (name lang))]
           :env {}})]
    {:actions (vec gen-actions)
     :declared-outputs
     (into {} (map (fn [a] [(keyword (str "source/" (name (last (str/split (name (:id a)) #"-")))))
                             (first (:outputs a))])
                   gen-actions))}))
```

Now a Rust target can depend on a protobuf target, and the coordinator stitches the graphs together: protobuf plugin produces `:source/rust` artifacts, Rust plugin consumes `:source/rust` as additional inputs. Neither plugin knows about the other.

## Why This Combo Works

**Babashka as a plugin runtime** gives you: fast startup, excellent data manipulation primitives, no compilation step for plugin development, and EDN is both the wire format and the native data format. Writing a plugin is literally just writing functions that transform maps.

**Go as the coordinator** gives you: goroutines for parallel action scheduling, good cross-platform sandbox support, easy single-binary distribution, and solid libraries for content-addressed storage and hashing.

The philosophical alignment is nice too — the Go side is imperative, concurrent, and concerned with *execution*. The Babashka side is functional, declarative, and concerned with *description*. Each language is doing what it's best at.

One thing worth considering: you could also support plugins in *any* language that speaks EDN-over-stdin (or JSON, since EDN is a superset concern). The bb plugins would be the blessed path, but a team that wants to write a plugin in Python or Rust could do so. The protocol is the contract, not the runtime.

## User

we'll want to support multiple serialization formats - json, edn, etc. what might a docker image build look like?

## Claude

Good call on multi-format — you don't want to force everyone into EDN just because the blessed plugin path uses bb. Let me work through both pieces.

## Format Negotiation

Keep it simple. The plugin declares what it speaks during startup, and the coordinator adapts:

```go
// Plugin manifest
type PluginManifest struct {
    Name    string   `json:"name"`
    Command []string `json:"command"`
    Format  string   `json:"format"` // "edn", "json", "msgpack"
}
```

Or let the plugin self-declare via a `--format` flag or an initial handshake line. The coordinator side just needs a codec interface:

```go
package codec

type Codec interface {
    Encode(v any) ([]byte, error)
    Decode(data []byte, v any) error
    ContentType() string
}

func ForFormat(f string) Codec {
    switch f {
    case "edn":
        return &EDNCodec{}
    case "json":
        return &JSONCodec{}
    case "msgpack":
        return &MsgpackCodec{}
    default:
        return &JSONCodec{} // safe default
    }
}
```

Each plugin connection wraps its stdin/stdout with the appropriate codec. The internal representation in Go is always the same structs — you're just swapping the serialization at the boundary. JSON is the lowest common denominator, EDN is the nice path for bb plugins, msgpack if someone cares about performance for huge action graphs.

## Docker Image Build Plugin

This is a great test case because Docker builds are interesting — they're inherently layer-based, have their own caching model, and the inputs aren't always known statically (a `COPY . .` depends on the full build context). A plugin has to bridge between Docker's world and the content-addressed action DAG.

```clojure
#!/usr/bin/env bb
;; plugins/docker/plugin.bb

(require '[clojure.edn :as edn]
         '[clojure.string :as str]
         '[babashka.fs :as fs])

(defn discover []
  {:name "docker"
   :version "0.1.0"
   :consumes #{:dockerfile :native-library :executable
               :static-files :source/any}
   :produces #{:docker-image :docker-image-tar}
   :config-schema
   {:registry {:type :string :required false}
    :repository {:type :string :required true}
    :tag {:type :string :default "latest"}
    :platform {:type :string :default "linux/amd64"}
    :build-args {:type :map :default {}}
    :target {:type :string :required false
             :doc "Multi-stage target to build"}}})
```

The `plan` is where it gets interesting. You can't just shell out to `docker build` as a single opaque action — that defeats the whole purpose because Docker would manage its own cache internally, invisible to our content-addressed store. Instead, the plugin should decompose the build into actions the coordinator can cache:

```clojure
(defn parse-dockerfile
  "Crude Dockerfile parser - extract stages and COPY/ADD directives
   to understand the dependency structure."
  [dockerfile-content]
  (let [lines (str/split-lines dockerfile-content)
        stages (atom [])
        current (atom {:name nil :from nil :commands [] :copies []})]
    (doseq [line lines]
      (let [trimmed (str/trim line)]
        (cond
          (str/starts-with? (str/upper-case trimmed) "FROM ")
          (do
            (when (:from @current)
              (swap! stages conj @current))
            (let [parts (str/split trimmed #"\s+")
                  ;; FROM image AS name
                  as-name (when (some #{"AS" "as"} parts)
                            (last parts))]
              (reset! current {:name as-name
                               :from (second parts)
                               :commands []
                               :copies []})))

          (re-matches #"(?i)^(COPY|ADD)\s+.*" trimmed)
          (let [;; check for --from=stage references
                from-stage (second (re-find #"--from=(\S+)" trimmed))
                ;; last token is destination, everything else is source
                parts (-> trimmed
                          (str/replace #"(?i)^(COPY|ADD)\s+" "")
                          (str/replace #"--from=\S+\s*" "")
                          (str/split #"\s+"))
                srcs (butlast parts)
                dest (last parts)]
            (swap! current update :copies conj
                   {:sources srcs
                    :dest dest
                    :from-stage from-stage}))

          :else
          (when (not (or (str/blank? trimmed)
                         (str/starts-with? trimmed "#")))
            (swap! current update :commands conj trimmed)))))
    (when (:from @current)
      (swap! stages conj @current))
    @stages))
```

Now the plan function can emit structured actions. The key insight: we use **buildkit-style export** rather than `docker build`. Buildkit can import/export individual layers as OCI tarballs, which are content-addressable:

```clojure
(defn plan-stage
  "Emit actions for a single Dockerfile stage."
  [{:keys [target config dep-artifacts]} stage stage-index prior-stages]
  (let [stage-id (or (:name stage) (str "stage-" stage-index))
        context-hash (get-in target [:source-hash])

        ;; If this stage COPYs --from=another-stage, that's
        ;; an intra-build dependency
        stage-deps (->> (:copies stage)
                        (keep :from-stage)
                        (map (fn [s] (keyword (str "stage-" s))))
                        vec)

        ;; If this stage COPYs from the build context,
        ;; those are external inputs
        context-copies (->> (:copies stage)
                            (remove :from-stage)
                            (mapcat :sources))

        ;; If deps produce artifacts (e.g., a Go binary),
        ;; they get mounted into the build context
        build-args (merge (:build-args config)
                          (into {}
                            (map (fn [[k v]] [(str "DEP_" (str/upper-case (name k))) v])
                                 dep-artifacts)))]

    {:id (keyword stage-id)
     :command
     ["buildctl" "build"
      "--frontend" "dockerfile.v0"
      "--local" (str "context=context/" stage-id)
      "--local" "dockerfile=."
      "--opt" (str "target=" stage-id)
      "--opt" (str "platform=" (:platform config))
      ;; export as OCI tarball — content-addressable
      "--output" (str "type=oci,dest=" stage-id ".tar")
      ;; pass build args
      ,@(mapcat (fn [[k v]] ["--opt" (str "build-arg:" k "=" v)])
                build-args)]
     :inputs
     (merge
       ;; the Dockerfile itself
       {"Dockerfile" (:dockerfile-hash target)}
       ;; context files this stage actually touches
       (into {} (map #(vector % context-hash) context-copies))
       ;; artifacts from build deps (e.g., compiled binary)
       dep-artifacts
       ;; outputs from prior stages
       (into {} (map (fn [s] [(str (name s) ".tar")
                               {:from-action s}])
                     stage-deps)))
     :outputs [(str stage-id ".tar")]
     :depends-on stage-deps
     :env {}}))

(defn plan-tag-and-push
  "Final action: tag the image and optionally push to registry."
  [{:keys [config]} final-stage-action]
  (let [{:keys [registry repository tag]} config
        full-tag (str (when registry (str registry "/"))
                      repository ":" tag)]
    {:id :tag-and-push
     :command
     (if registry
       ;; push to registry using crane/skopeo — no daemon needed
       ["skopeo" "copy"
        (str "oci-archive:" (name (:id final-stage-action)) ".tar")
        (str "docker://" full-tag)]
       ;; just load into local daemon
       ["ctr" "image" "import"
        (str (name (:id final-stage-action)) ".tar")])
     :inputs {(str (name (:id final-stage-action)) ".tar")
              {:from-action (:id final-stage-action)}}
     :outputs [(str "image-digest-" repository ".txt")]
     :depends-on [(:id final-stage-action)]
     :env {}}))

(defn plan [{:keys [target] :as request}]
  (let [dockerfile (slurp (first (filter #(str/ends-with? % "Dockerfile")
                                         (:sources target))))
        stages (parse-dockerfile dockerfile)
        ;; determine which stage to build
        target-stage (:target (:config target))
        stages (if target-stage
                 (let [idx (->> stages
                                (keep-indexed
                                  (fn [i s] (when (= (:name s) target-stage) i)))
                                first)]
                   (subvec (vec stages) 0 (inc idx)))
                 stages)

        ;; emit actions per stage
        stage-actions
        (reduce
          (fn [acc [idx stage]]
            (conj acc (plan-stage request stage idx (map :id acc))))
          []
          (map-indexed vector stages))

        ;; final tagging/push action
        push-action (plan-tag-and-push request (last stage-actions))]

    {:actions (conj stage-actions push-action)
     :declared-outputs
     {:docker-image (first (:outputs push-action))
      :docker-image-tar (first (:outputs (last stage-actions)))}}))
```

And the build file:

```edn
;; services/api/BUILD.edn
[{:target "//services/api:binary"
  :toolchain :go
  :sources ["cmd/api/*.go" "internal/**/*.go"]
  :deps ["//lib/auth" "//lib/db"]
  :config {:output "api-server"}}

 {:target "//services/api:image"
  :toolchain :docker
  :sources ["Dockerfile"]
  :deps ["//services/api:binary"]
  :config {:repository "myorg/api-server"
           :tag "{{git-sha}}"
           :registry "registry.internal.io"
           :platform "linux/amd64"
           :target "production"}}]
```

With a multi-stage Dockerfile like:

```dockerfile
FROM golang:1.22 AS build
# This stage is actually skipped — the Go plugin already built the binary
# But we keep it for docker-build compatibility

FROM gcr.io/distroless/static:nonroot AS production
COPY --from=build /app/api-server /usr/local/bin/api-server
EXPOSE 8080
ENTRYPOINT ["api-server"]
```

## The Interesting Design Decisions

**Daemonless by default.** The plugin uses `buildctl` (BuildKit) and `skopeo` rather than the Docker daemon. This means builds can run in the same sandboxed environment as any other action — no privileged socket, no side-channel cache that's invisible to the coordinator. The OCI tarball is just a file, content-addressable like anything else.

**Layer-level vs stage-level granularity.** I went with stage-level actions here, which is a pragmatic middle ground. You *could* decompose individual Dockerfile instructions into separate actions, but that's reimplementing BuildKit's layer cache and the juice isn't worth the squeeze. Stages are the natural boundary — they correspond to meaningful build phases and they're what people actually use for caching in multi-stage builds.

**Dep artifacts flow through build args.** When `//services/api:image` depends on `//services/api:binary`, the coordinator resolves the Go plugin's output, and the Docker plugin receives the binary as a dep artifact. The plugin maps it into a build arg or mounts it into the build context. The Dockerfile doesn't need to know where the binary came from — it just `COPY`s it.

**Registry push is an action.** This means pushing is cached too — if the image tarball hash hasn't changed and the digest file exists, the push is skipped. Idempotent deploys for free.

The thing I like about this approach is that it degrades gracefully. If someone doesn't want the decomposed buildkit approach, a "simple" Docker plugin could just emit a single opaque `docker build` action — you lose granular caching but everything still works. The sophisticated version is opt-in complexity, not mandatory.

## User

okay, hrm. how would a user configure, say, a docker compose for a golang webservice, with docker build, voluming-in of the built binary, a postgres running in a container, .env support, and recompilation on code change and tests running on code change?

## Claude

Now we're leaving pure build-graph territory and entering **dev workflow orchestration** — long-running processes, file watching, service dependencies, and the feedback loop. This is where most build systems punt and say "just use docker-compose separately," but if we're designing this thing right, it should handle it.

The core tension: the build graph is a DAG of terminating actions. A dev environment is a set of **long-running processes** that react to changes. You need a second primitive.

## New Primitive: Services

Alongside `target` (a thing you build), you need `service` (a thing you run) and `trigger` (a thing that reacts to changes):

```edn
;; BUILD.edn at repo root, or a dedicated dev.edn

;; ──────────────────────────────────────────────
;; Build targets (these are normal DAG actions)
;; ──────────────────────────────────────────────

{:target "//services/api:binary"
 :toolchain :go
 :sources ["cmd/api/**/*.go" "internal/**/*.go" "go.mod" "go.sum"]
 :deps ["//lib/auth" "//lib/db"]
 :config {:output "api-server"
          :gcflags "all=-N -l"  ;; disable optimizations for dev
          :tags ["dev"]}}

{:target "//services/api:test"
 :toolchain :go-test
 :sources ["cmd/api/**/*.go" "internal/**/*_test.go" "internal/**/*.go"]
 :deps ["//lib/auth" "//lib/db"]
 :config {:race true
          :count 1
          :tags ["dev"]}}

;; ──────────────────────────────────────────────
;; Services (long-running, compose-like)
;; ──────────────────────────────────────────────

{:service "//dev:postgres"
 :runtime :docker
 :config {:image "postgres:16-alpine"
          :ports {"5432" "5432"}
          :env-file ".env"
          :environment {:POSTGRES_DB "${DB_NAME}"
                        :POSTGRES_USER "${DB_USER}"
                        :POSTGRES_PASSWORD "${DB_PASSWORD}"}
          :volumes {"pgdata" "/var/lib/postgresql/data"}
          :healthcheck {:command ["pg_isready" "-U" "${DB_USER}"]
                        :interval "2s"
                        :retries 10}}}

{:service "//dev:api"
 :runtime :docker
 :depends-on {"//dev:postgres" :healthy}
 :config {:image "golang:1.22"    ;; run in a container for parity
          :ports {"8080" "8080"
                  "2345" "2345"}   ;; delve debugger
          :env-file ".env"
          :environment {:DATABASE_URL "postgres://${DB_USER}:${DB_PASSWORD}@postgres:5432/${DB_NAME}?sslmode=disable"
                        :ENV "development"}
          :volumes {;; mount the built binary from the build target
                    "//services/api:binary" "/app/api-server"
                    ;; also mount source for delve
                    "./services/api" "/src"
                    ;; mount the .env file
                    "./.env" "/app/.env"}
          :command ["/app/api-server"]
          :working-dir "/app"}}

;; ──────────────────────────────────────────────
;; Triggers (react to file changes)
;; ──────────────────────────────────────────────

{:trigger "//dev:rebuild-on-change"
 :watch ["cmd/api/**/*.go" "internal/**/*.go" "go.mod"]
 :run "//services/api:binary"       ;; rebuild the target
 :then {:restart "//dev:api"}       ;; restart the service
 :debounce "500ms"}

{:trigger "//dev:test-on-change"
 :watch ["internal/**/*.go"]
 :run "//services/api:test"
 :debounce "1s"
 :config {:notify true              ;; desktop notification on fail
          :fail-fast true}}         ;; stop on first failure
```

## What the User Actually Types

```bash
$ forge dev
```

That's it. The coordinator reads the build file, and:

1. Resolves all `service` entries topologically by `depends-on`
2. Builds any targets referenced in service volumes (`//services/api:binary`)
3. Starts services in dependency order, waiting for healthchecks
4. Activates all triggers and begins watching

The output looks something like:

```
forge dev
  ● //services/api:binary    built (1.2s, cached)
  ● //dev:postgres            healthy (postgres:16-alpine on :5432)
  ● //dev:api                 running (golang:1.22 on :8080)
  ◎ watching 47 files for //dev:rebuild-on-change
  ◎ watching 38 files for //dev:test-on-change
```

And when you edit a file:

```
  ∆ internal/handler/auth.go changed
  ● //services/api:binary    rebuilt (0.8s)
  ↻ //dev:api                restarted
  ● //services/api:test      3 passed (2.1s)
```

## The .env Story

The `.env` handling is explicit — it's just a file that services reference. The coordinator loads it and interpolates `${VAR}` references in service configs before passing them to Docker. No magic:

```bash
# .env
DB_NAME=myapp_dev
DB_USER=postgres
DB_PASSWORD=devpassword
API_PORT=8080
SECRET_KEY=dev-secret-do-not-use
```

The coordinator treats `.env` as an input to service configs. If you add `.env` to a trigger's watch list, changing env vars restarts affected services:

```edn
{:trigger "//dev:env-change"
 :watch [".env"]
 :then {:restart ["//dev:api" "//dev:postgres"]}}
```

## How This Maps to Docker Compose Under the Hood

The coordinator doesn't generate a `docker-compose.yml` — it manages containers directly via the Docker API (or podman). But conceptually the mapping is clear:

```yaml
# What you'd write by hand in docker-compose.yml
# (the coordinator does this programmatically)
services:
  postgres:
    image: postgres:16-alpine
    ports: ["5432:5432"]
    env_file: .env
    environment:
      POSTGRES_DB: ${DB_NAME}
    volumes:
      - pgdata:/var/lib/postgresql/data
    healthcheck:
      test: ["CMD", "pg_isready", "-U", "${DB_USER}"]
      interval: 2s
      retries: 10

  api:
    image: golang:1.22
    depends_on:
      postgres: { condition: service_healthy }
    ports: ["8080:8080", "2345:2345"]
    env_file: .env
    volumes:
      - ./build/api-server:/app/api-server  # <-- this is the built artifact
      - ./services/api:/src
    command: ["/app/api-server"]

volumes:
  pgdata:
```

The key difference is that the `./build/api-server` volume mount is managed — the coordinator knows it comes from a build target and keeps it up to date. In vanilla docker-compose you'd need a separate `make` or `go build` step and hope you remember to run it.

## The Service Runtime Plugin

The `runtime: :docker` in service definitions is itself a plugin:

```clojure
#!/usr/bin/env bb
;; plugins/docker-runtime/plugin.bb

(defn discover []
  {:name "docker-runtime"
   :type :runtime             ;; not a build toolchain, a runtime
   :provides #{:docker}
   :capabilities #{:healthcheck :volumes :ports :networks :env-file}})

(defn start [{:keys [service resolved-volumes env]}]
  ;; resolved-volumes has artifact paths already substituted
  ;; e.g., "//services/api:binary" -> "/home/user/.forge/cas/sha256-abc.../api-server"
  (let [{:keys [image ports environment volumes command
                healthcheck working-dir]} (:config service)
        container-name (-> (:name service)
                           (str/replace "//" "")
                           (str/replace ":" "-"))]
    {:action :create-and-start
     :container {:name container-name
                 :image image
                 :ports (mapv (fn [[h c]] {:host h :container c}) ports)
                 :env (merge env environment)
                 :mounts (mapv (fn [[src dest]]
                                {:source (get resolved-volumes src src)
                                 :target dest
                                 :type (if (str/starts-with? src "/")
                                         :bind :volume)})
                               volumes)
                 :command command
                 :working-dir working-dir
                 :healthcheck healthcheck
                 :network "forge-dev"  ;; shared network for service discovery
                 :labels {"forge.service" (:name service)
                          "forge.managed" "true"}}}))

(defn stop [{:keys [service]}]
  {:action :stop-and-remove
   :container-name (-> (:name service)
                        (str/replace "//" "")
                        (str/replace ":" "-"))})

(defn restart [req]
  [(stop req) (start req)])
```

## The File Watcher in Go

The coordinator's trigger system uses fsnotify (or something similar) and debounces changes before feeding them back into the build graph:

```go
package trigger

type Trigger struct {
    Name     string
    Patterns []string
    Target   string        // build target to re-run
    Restart  []string      // services to restart after build
    Debounce time.Duration
    Notify   bool
}

func (t *Trigger) Run(ctx context.Context, dag *dag.DAG, 
                       services *service.Manager) {
    watcher := fswatch.New(t.Patterns)
    events := debounce(watcher.Events(), t.Debounce)

    for {
        select {
        case changed := <-events:
            log.Info("files changed", "trigger", t.Name, 
                     "files", changed)

            // invalidate and re-execute just the affected subgraph
            if t.Target != "" {
                result := dag.Rebuild(ctx, t.Target)
                if result.Err != nil {
                    if t.Notify {
                        notify.Send("Build failed", result.Err.Error())
                    }
                    continue
                }
            }

            // restart dependent services with new artifacts
            for _, svc := range t.Restart {
                services.Restart(ctx, svc)
            }

        case <-ctx.Done():
            return
        }
    }
}
```

## What This Gets You Over Docker Compose

**Build integration.** Compose doesn't know about your build. You either run `go build` manually before `docker-compose up`, or you use `build:` directives which do a full `docker build` (slow, no incremental compilation). Here, the Go toolchain plugin does proper incremental builds and the binary is volume-mounted.

**Selective restarts.** When you change a Go file, only the binary rebuilds and only the API service restarts. Postgres stays up. Compose's `watch` feature is getting there but it's still coarse.

**Tests in the loop.** The test trigger runs in parallel with the rebuild trigger. You get your binary restarted *and* test results, from the same file save.

**Same tool, all the way.** `forge build //services/api:image` for CI, `forge dev` for local. The build files are the source of truth for both. No drift between your Makefile, your docker-compose.yml, your Dockerfile, and your CI pipeline.

**Hermetic builds even in dev.** The binary is still built through the build graph with caching. If your coworker runs `forge dev`, they get a cache hit on the binary if nothing changed. Compose can't do this.

## Where It Gets Tricky

The honest tension is that you've now got three primitives — targets, services, triggers — and the interaction surface between them is non-trivial. Some things to decide:

**Should services be able to depend on other services' *outputs*?** Like, a migration service that runs once and produces a "database is migrated" marker that the API service waits on. That's somewhere between a build target and a service.

**Volume mounts from build targets need lifecycle management.** When the binary is rebuilt, the old file descriptor is still open in the container. You need to either restart the container (current approach) or run the binary through a process supervisor inside the container that handles SIGHUP.

**Networking.** The `forge-dev` network is implicit here. For more complex setups with multiple service groups that should be isolated, you'd want network as a first-class config.

But for the 90% case — a web service, a database, rebuild on change — this is about 40 lines of EDN compared to maintaining a Makefile + docker-compose.yml + a shell script that glues them together.

## User

next, what would the bootstrap plugin look like?

## Claude

This is the plugin that makes the whole system self-hosting — it's responsible for fetching, verifying, and managing the toolchains that every other plugin depends on. Without it, you're back to "make sure you have Go 1.22 installed" which defeats hermeticity.

## The Problem

Every plugin emits actions that reference tools — `rustc`, `go`, `protoc`, `buildctl`, `skopeo`. Those tools need to come from somewhere, and "whatever's on PATH" is the enemy of reproducibility. The bootstrap plugin treats toolchains as artifacts in the CAS, just like compiled outputs. The Go compiler is just a blob with a hash.

## Toolchain Definitions

First, how users declare what toolchains they need:

```edn
;; toolchains.edn (repo root)

[{:toolchain "//toolchains:go"
  :plugin :bootstrap
  :config {:name "go"
           :version "1.22.4"
           :sources
           {:linux/amd64
            {:url "https://go.dev/dl/go1.22.4.linux-amd64.tar.gz"
             :sha256 "ba9ef7c15a2ba8e6b0b1ff12ce0f21a46e8228ed2feca2be88b859d6a0e8c697"
             :strip-prefix "go"}
            :linux/arm64
            {:url "https://go.dev/dl/go1.22.4.linux-arm64.tar.gz"
             :sha256 "a8e177c354d2e4a1b61020aca3562e27ea3e8f8247eca3170e3fa1e0c2f9e771"
             :strip-prefix "go"}
            :darwin/arm64
            {:url "https://go.dev/dl/go1.22.4.darwin-arm64.tar.gz"
             :sha256 "242b0ec4a44b3669a1e0e3da0c8ff3c4b3e4e94b9e0b6a44e96e29f2ff22e0c4"
             :strip-prefix "go"}}
           :provides {:go "bin/go"
                      :gofmt "bin/gofmt"}}}

 {:toolchain "//toolchains:rust"
  :plugin :bootstrap
  :config {:name "rust"
           :version "1.78.0"
           :sources
           {:linux/amd64
            {:url "https://static.rust-lang.org/dist/rust-1.78.0-x86_64-unknown-linux-gnu.tar.xz"
             :sha256 "..."
             :install-script "install.sh"  ;; some toolchains need an install step
             :install-args ["--prefix=." "--without=rust-docs"]}
            :darwin/arm64
            {:url "https://static.rust-lang.org/dist/rust-1.78.0-aarch64-apple-darwin.tar.xz"
             :sha256 "..."
             :install-script "install.sh"
             :install-args ["--prefix=." "--without=rust-docs"]}}
           :provides {:rustc "bin/rustc"
                      :cargo "bin/cargo"
                      :rust-std "lib/rustlib"}}}

 {:toolchain "//toolchains:protoc"
  :plugin :bootstrap
  :config {:name "protoc"
           :version "26.1"
           :sources
           {:linux/amd64
            {:url "https://github.com/protocolbuffers/protobuf/releases/download/v26.1/protoc-26.1-linux-x86_64.zip"
             :sha256 "..."
             :strip-prefix ""}
            :darwin/arm64
            {:url "https://github.com/protocolbuffers/protobuf/releases/download/v26.1/protoc-26.1-osx-aarch64.zip"
             :sha256 "..."}}
           :provides {:protoc "bin/protoc"
                      :protoc-include "include"}}}

 ;; You can also build toolchains from source
 {:toolchain "//toolchains:tinygo"
  :plugin :bootstrap
  :config {:name "tinygo"
           :version "0.32.0"
           :build-from-source
           {:repository "https://github.com/tinygo-org/tinygo"
            :ref "v0.32.0"
            :build-deps ["//toolchains:go" "//toolchains:llvm"]
            :build-command ["go" "build" "-o" "tinygo" "."]
            :provides {:tinygo "tinygo"}}
           ;; prebuilt is preferred, source is fallback
           :sources
           {:linux/amd64
            {:url "https://github.com/tinygo-org/tinygo/releases/download/v0.32.0/tinygo0.32.0.linux-amd64.tar.gz"
             :sha256 "..."
             :strip-prefix "tinygo"}}}}]
```

## The Plugin Itself

```clojure
#!/usr/bin/env bb
;; plugins/bootstrap/plugin.bb

(require '[clojure.edn :as edn]
         '[clojure.string :as str]
         '[babashka.fs :as fs])

(defn discover []
  {:name "bootstrap"
   :version "0.1.0"
   :type :bootstrap           ;; special type — runs before other plugins
   :consumes #{}              ;; no build inputs (except network)
   :produces #{:toolchain}
   :config-schema
   {:name {:type :string :required true}
    :version {:type :string :required true}
    :sources {:type :map :required false
              :doc "Platform->download spec"}
    :build-from-source {:type :map :required false}
    :provides {:type :map :required true
               :doc "Logical name -> relative path within extracted toolchain"}}})

(defn current-platform []
  (let [os (str/lower-case (System/getProperty "os.name"))
        arch (System/getProperty "os.arch")]
    (keyword
      (str (cond
             (str/includes? os "linux") "linux"
             (str/includes? os "mac")   "darwin"
             (str/includes? os "win")   "windows"
             :else (throw (ex-info "unsupported OS" {:os os})))
           "/"
           (case arch
             "amd64"   "amd64"
             "x86_64"  "amd64"
             "aarch64" "arm64"
             "arm64"   "arm64"
             (throw (ex-info "unsupported arch" {:arch arch})))))))

(defn archive-type [url]
  (cond
    (str/ends-with? url ".tar.gz")  :tar-gz
    (str/ends-with? url ".tar.xz")  :tar-xz
    (str/ends-with? url ".zip")     :zip
    :else (throw (ex-info "unknown archive type" {:url url}))))

(defn plan-fetch
  "Emit actions to download and verify a toolchain archive."
  [name version {:keys [url sha256] :as source}]
  (let [archive-name (str name "-" version (case (archive-type url)
                                             :tar-gz ".tar.gz"
                                             :tar-xz ".tar.xz"
                                             :zip    ".zip"))]
    {:id (keyword (str "fetch-" name))
     :command ["forge-fetch"          ;; built-in to the coordinator
               "--url" url
               "--sha256" sha256
               "--output" archive-name]
     :inputs {"__url" url             ;; pseudo-input for cache keying
              "__sha256" sha256}
     :outputs [archive-name]
     :env {}
     :network true}))                 ;; special flag: this action needs network

(defn plan-extract
  "Emit action to extract and optionally install the toolchain."
  [name version source fetch-action]
  (let [archive (first (:outputs fetch-action))
        extract-dir (str name "-" version)
        strip (:strip-prefix source "")]
    {:id (keyword (str "extract-" name))
     :command
     (if (:install-script source)
       ;; some toolchains (rust) need a post-extract install step
       ["sh" "-c"
        (str/join " && "
          [(case (archive-type (:url source))
             :tar-gz (str "tar xzf " archive)
             :tar-xz (str "tar xJf " archive)
             :zip    (str "unzip -q " archive))
           (when (seq strip) (str "cd " strip))
           (str "./" (:install-script source) " "
                (str/join " " (:install-args source)))
           (str "mv " extract-dir " $OUT")])]
       ;; simple extraction
       ["sh" "-c"
        (str (case (archive-type (:url source))
               :tar-gz "tar xzf "
               :tar-xz "tar xJf "
               :zip    "unzip -q -d extracted ")
             archive
             (when (seq strip)
               (str " && mv " strip " " extract-dir)))])
     :inputs {archive {:from-action (:id fetch-action)}}
     :outputs [extract-dir]
     :depends-on [(:id fetch-action)]
     :env {}}))

(defn plan-verify
  "Emit action to verify the toolchain works."
  [name version provides extract-action]
  (let [extract-dir (first (:outputs extract-action))
        ;; pick the first provided binary for a smoke test
        [tool-name tool-path] (first provides)
        bin-path (str extract-dir "/" tool-path)]
    {:id (keyword (str "verify-" name))
     :command
     (case tool-name
       :go       [bin-path "version"]
       :rustc    [bin-path "--version"]
       :protoc   [bin-path "--version"]
       :cargo    [bin-path "--version"]
       ;; generic fallback
       [bin-path "--version"])
     :inputs {extract-dir {:from-action (:id extract-action)}}
     :outputs [(str name "-" version ".verified")]  ;; marker file
     :depends-on [(:id extract-action)]
     :env {}}))

(defn plan-register
  "Emit action to register toolchain artifacts in the CAS.
   This produces a manifest that other plugins consume."
  [name version provides extract-action verify-action]
  (let [extract-dir (first (:outputs extract-action))
        manifest {:name name
                  :version version
                  :artifacts
                  (into {}
                    (map (fn [[k v]]
                           [k (str extract-dir "/" v)])
                         provides))}]
    {:id (keyword (str "register-" name))
     :command ["forge-register-toolchain"   ;; built-in to coordinator
               "--manifest" (str name "-" version ".manifest.edn")]
     :inputs {extract-dir {:from-action (:id extract-action)}
              (str name ".verified") {:from-action (:id verify-action)}}
     :outputs [(str name "-" version ".manifest.edn")]
     :depends-on [(:id verify-action)]
     :env {}
     :metadata {:manifest manifest}}))     ;; passed directly to coordinator

(defn plan-prebuilt
  "Plan for a prebuilt toolchain download."
  [{:keys [target]}]
  (let [{:keys [name version sources provides]} (:config target)
        platform (current-platform)
        source (get sources platform)]
    (when-not source
      (throw (ex-info (str "no prebuilt toolchain for " (pr-str platform))
                      {:name name :version version :platform platform
                       :available (keys sources)})))
    (let [fetch   (plan-fetch name version source)
          extract (plan-extract name version source fetch)
          verify  (plan-verify name version provides extract)
          register (plan-register name version provides extract verify)]
      {:actions [fetch extract verify register]
       :declared-outputs
       (merge
         {:toolchain-manifest (first (:outputs register))}
         ;; expose each provided tool as a named output
         (into {}
           (map (fn [[k v]]
                  [k (str (first (:outputs extract)) "/" v)])
                provides)))})))

(defn plan-from-source
  "Plan for building a toolchain from source."
  [{:keys [target]}]
  (let [{:keys [name version build-from-source provides]} (:config target)
        {:keys [repository ref build-deps build-command]} build-from-source]
    {:actions
     [{:id (keyword (str "clone-" name))
       :command ["git" "clone" "--depth=1" "--branch" ref repository (str name "-src")]
       :inputs {"__repo" repository "__ref" ref}
       :outputs [(str name "-src")]
       :env {}
       :network true}
      {:id (keyword (str "build-" name))
       :command (into ["sh" "-c"
                       (str "cd " name "-src && "
                            (str/join " " build-command))]
                      [])
       :inputs {(str name "-src") {:from-action (keyword (str "clone-" name))}}
       :outputs [(str name "-" version)]
       :depends-on [(keyword (str "clone-" name))]
       ;; build-deps are resolved by the coordinator and injected into env
       :toolchain-deps build-deps
       :env {}}
      {:id (keyword (str "register-" name))
       :command ["forge-register-toolchain"
                 "--manifest" (str name "-" version ".manifest.edn")]
       :inputs {(str name "-" version)
                {:from-action (keyword (str "build-" name))}}
       :outputs [(str name "-" version ".manifest.edn")]
       :depends-on [(keyword (str "build-" name))]
       :metadata {:manifest {:name name :version version
                             :artifacts provides}}}]
     :declared-outputs
     (merge
       {:toolchain-manifest (str name "-" version ".manifest.edn")}
       (into {}
         (map (fn [[k v]] [k (str name "-" version "/" v)])
              (:provides build-from-source))))}))

(defn plan [{:keys [target] :as request}]
  (let [config (:config target)
        platform (current-platform)
        ;; prefer prebuilt, fall back to source
        has-prebuilt? (get-in config [:sources platform])]
    (if has-prebuilt?
      (plan-prebuilt request)
      (if (:build-from-source config)
        (plan-from-source request)
        (throw (ex-info "no source available for platform"
                        {:name (:name config)
                         :platform platform}))))))

(defn handle [msg]
  (case (:method msg)
    :discover (discover)
    :plan (plan msg)
    {:error "unknown method"}))

(doseq [line (line-seq (java.io.BufferedReader. *in*))]
  (let [msg (edn/read-string line)]
    (println (pr-str (handle msg)))
    (flush)))
```

## How the Coordinator Wires This Up

The bootstrap plugin is special in one important way: it runs **before** all other plugins, because its outputs are inputs to everything else. The coordinator handles this:

```go
package plugin

// Resolution order:
// 1. Parse all build files
// 2. Identify toolchain targets (plugin == "bootstrap")
// 3. Plan and execute bootstrap actions first
// 4. Inject toolchain artifacts into all subsequent plugin Plan requests

type ToolchainRegistry struct {
    mu         sync.RWMutex
    toolchains map[string]*ToolchainManifest
}

type ToolchainManifest struct {
    Name      string            
    Version   string            
    Artifacts map[string]string  // logical name -> CAS hash
}

func (r *ToolchainRegistry) Resolve(name string) (map[string]string, error) {
    r.mu.RLock()
    defer r.mu.RUnlock()
    tc, ok := r.toolchains[name]
    if !ok {
        return nil, fmt.Errorf("toolchain %q not bootstrapped", name)
    }
    return tc.Artifacts, nil
}

// When planning a normal target, the coordinator injects toolchain artifacts:
func (c *Coordinator) PlanTarget(target Target) (*PlanResponse, error) {
    plugin := c.pluginFor(target.Toolchain)
    
    // look up what this toolchain needs
    toolArtifacts, err := c.toolchains.Resolve(target.Toolchain)
    if err != nil {
        return nil, err
    }

    return plugin.Plan(PlanRequest{
        Target:             target,
        Deps:               c.resolvedDeps(target),
        ToolchainArtifacts: toolArtifacts,  // <-- injected
    })
}
```

## The Built-in Commands

The plugin references two coordinator built-ins:

**`forge-fetch`** — a sandboxed downloader. It's one of the few actions allowed network access (most actions are hermetic/offline). It downloads to a temp location, verifies the SHA256, and moves to the output path. This is built into the coordinator binary, not a separate tool, because network access control is a security boundary:

```go
package builtin

func ForgeFetch(url, sha256, output string) error {
    tmp, _ := os.CreateTemp("", "forge-fetch-*")
    defer os.Remove(tmp.Name())

    resp, _ := http.Get(url)
    defer resp.Body.Close()

    hasher := sha256.New()
    io.Copy(io.MultiWriter(tmp, hasher), resp.Body)
    
    actual := hex.EncodeToString(hasher.Sum(nil))
    if actual != expected {
        return fmt.Errorf("hash mismatch: got %s want %s", actual, expected)
    }
    return os.Rename(tmp.Name(), output)
}
```

**`forge-register-toolchain`** — reads the manifest EDN, registers the toolchain's artifacts in the CAS, and writes a manifest file that the coordinator loads on subsequent runs. This is the bridge between the bootstrap plugin's output and the coordinator's toolchain registry.

## The Lifecycle

On a clean machine, first `forge build` ever:

```
$ forge build //services/api:binary

  bootstrapping toolchains...
  ↓ //toolchains:go          fetching go1.22.4.linux-amd64.tar.gz
  ✓ //toolchains:go          verified (go version go1.22.4 linux/amd64)
  ↓ //toolchains:protoc      fetching protoc-26.1-linux-x86_64.zip
  ✓ //toolchains:protoc      verified (libprotoc 26.1)

  building...
  ● //lib/db                  built (0.4s)
  ● //lib/auth                built (0.6s)
  ● //services/api:binary     built (1.8s)
```

Second run, everything is cached — the toolchain hashes haven't changed so bootstrap is a no-op, and if source hasn't changed the build is a no-op too:

```
$ forge build //services/api:binary

  ● //toolchains:go           cached
  ● //services/api:binary     cached (0.01s)
```

Upgrade Go by changing the version and hash in `toolchains.edn`:

```
$ forge build //services/api:binary

  ↓ //toolchains:go          fetching go1.23.0.linux-amd64.tar.gz
  ✓ //toolchains:go          verified (go version go1.23.0 linux/amd64)

  building...
  ● //lib/db                  rebuilt (0.4s)  ← cache miss, toolchain changed
  ● //lib/auth                rebuilt (0.5s)
  ● //services/api:binary     rebuilt (1.6s)
```

Everything downstream of the toolchain invalidates automatically because the toolchain artifacts are inputs to every action. Change the compiler hash, the whole cache key changes. That's the elegance of content-addressing — version upgrades are just hash changes, and the DAG handles the rest.

## Remote Cache / Shared Teams

Because the bootstrapped toolchains are content-addressed, they work with remote caching out of the box. Developer A bootstraps Go 1.22.4, the extracted toolchain gets stored in the remote CAS. Developer B runs the same build, the coordinator checks the CAS, finds the toolchain by hash, and skips the download entirely. Same for CI. Nobody downloads the Go toolchain twice for the same version.

```edn
;; forge.config.edn
{:remote-cache {:type :s3
                :bucket "forge-cache"
                :region "us-east-1"}
 :remote-cache-read true
 :remote-cache-write true}
```

The bootstrap plugin doesn't need to know about any of this — it just emits actions with declared inputs and outputs, and the coordinator's CAS layer handles caching transparently.

## User

i want the system to use OCI registries - probably among other things - as caches. good idea?

## Claude

Great idea. OCI registries are already content-addressed blob stores with authentication, replication, CDN support, and tooling that every org already operates. You're not inventing a cache layer — you're reusing infrastructure that already exists and that people already know how to run, secure, and monitor.

## Why OCI Is a Natural Fit

The CAS model maps almost directly onto OCI's storage model:

| Build system concept | OCI concept |
|---|---|
| Artifact blob | Layer blob (content-addressed by sha256) |
| Action cache key | Manifest tag |
| Toolchain bundle | OCI image (multi-layer) |
| Build output | OCI artifact (with custom media types) |
| Platform variants | Multi-platform manifest index |

OCI registries already solve the hard distributed systems problems — deduplication, garbage collection, replication across regions, access control, pull-through caching. You don't want to reimplement any of that.

## The Cache Interface

The coordinator should be pluggable on cache backends. OCI is one, S3 is another, local disk is the default:

```go
package cas

// The core interface — all cache backends implement this
type Store interface {
    // Content-addressed blob storage
    Has(ctx context.Context, digest digest.Digest) (bool, error)
    Get(ctx context.Context, digest digest.Digest) (io.ReadCloser, error)
    Put(ctx context.Context, r io.Reader) (digest.Digest, error)

    // Action cache: map from action key -> result metadata
    GetActionResult(ctx context.Context, key ActionKey) (*ActionResult, error)
    PutActionResult(ctx context.Context, key ActionKey, result *ActionResult) error
}

// ActionKey is the cache key for a completed action
type ActionKey struct {
    Command    string            `json:"command"`
    InputsHash digest.Digest     `json:"inputs_hash"`
    EnvHash    digest.Digest     `json:"env_hash"`
    Platform   string            `json:"platform"`
}

// ActionResult records what an action produced
type ActionResult struct {
    Outputs    map[string]digest.Digest `json:"outputs"`
    StdoutHash digest.Digest            `json:"stdout_hash,omitempty"`
    Duration   time.Duration            `json:"duration"`
    Cached     bool                     `json:"cached"`
}
```

## The OCI Backend

The mapping is: blobs become OCI blobs (direct content-addressed storage), and action results become OCI manifests tagged by action key hash. This means you can use `crane`, `skopeo`, or any OCI client to inspect the cache.

```go
package oci

import (
    "github.com/opencontainers/image-spec/specs-go/v1"
    "oras.land/oras-go/v2"
    "oras.land/oras-go/v2/registry/remote"
)

const (
    // Custom media types so the registry knows what these are
    MediaTypeForgeBlob     = "application/vnd.forge.blob.v1"
    MediaTypeForgeAction   = "application/vnd.forge.action-result.v1+json"
    MediaTypeForgeToolchain = "application/vnd.forge.toolchain.v1"
    MediaTypeForgeManifest = "application/vnd.forge.manifest.v1+json"
)

type OCIStore struct {
    repo     *remote.Repository
    repoBase string  // e.g. "registry.internal.io/forge-cache"
}

func New(repoBase string, opts ...Option) (*OCIStore, error) {
    repo, err := remote.NewRepository(repoBase)
    if err != nil {
        return nil, err
    }
    // Auth comes from standard OCI credential helpers
    // (~/.docker/config.json, cloud IAM, etc.)
    repo.Client = authClient()
    return &OCIStore{repo: repo, repoBase: repoBase}, nil
}
```

### Storing Blobs

Artifact blobs map directly to OCI blobs. The digest is the same — both use sha256:

```go
func (s *OCIStore) Put(ctx context.Context, r io.Reader) (digest.Digest, error) {
    // Buffer to temp file to get size (OCI requires content-length)
    tmp, _ := os.CreateTemp("", "forge-blob-*")
    defer os.Remove(tmp.Name())

    hasher := sha256.New()
    size, _ := io.Copy(io.MultiWriter(tmp, hasher), r)
    dgst := digest.NewDigestFromEncoded(digest.SHA256, 
        hex.EncodeToString(hasher.Sum(nil)))
    tmp.Seek(0, 0)

    desc := v1.Descriptor{
        MediaType: MediaTypeForgeBlob,
        Digest:    dgst,
        Size:      size,
    }

    // Push blob to registry
    err := s.repo.Push(ctx, desc, tmp)
    if err != nil {
        // Already exists is fine — content-addressed dedup
        if isAlreadyExists(err) {
            return dgst, nil
        }
        return "", err
    }
    return dgst, nil
}

func (s *OCIStore) Get(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
    desc := v1.Descriptor{Digest: dgst}
    return s.repo.Fetch(ctx, desc)
}

func (s *OCIStore) Has(ctx context.Context, dgst digest.Digest) (bool, error) {
    desc := v1.Descriptor{Digest: dgst}
    return s.repo.Exists(ctx, desc)
}
```

### Action Cache as OCI Manifests

This is the clever part. An action result becomes an OCI manifest where each output artifact is a layer, and the manifest is tagged by the action cache key. This means you can literally `crane pull` a cached action result:

```go
func (s *OCIStore) PutActionResult(ctx context.Context, key ActionKey, 
    result *ActionResult) error {
    
    // Each output becomes a layer descriptor pointing to its blob
    layers := make([]v1.Descriptor, 0, len(result.Outputs))
    for name, dgst := range result.Outputs {
        // Blob should already be stored via Put()
        size, _ := s.blobSize(ctx, dgst)
        layers = append(layers, v1.Descriptor{
            MediaType:   MediaTypeForgeBlob,
            Digest:      dgst,
            Size:        size,
            Annotations: map[string]string{
                "forge.output.name": name,
            },
        })
    }

    // Config blob holds the action result metadata
    configBytes, _ := json.Marshal(result)
    configDgst, _ := s.Put(ctx, bytes.NewReader(configBytes))

    manifest := v1.Manifest{
        SchemaVersion: 2,
        MediaType:     v1.MediaTypeImageManifest,
        Config: v1.Descriptor{
            MediaType: MediaTypeForgeAction,
            Digest:    configDgst,
            Size:      int64(len(configBytes)),
        },
        Layers: layers,
        Annotations: map[string]string{
            "forge.action.command":  key.Command,
            "forge.action.platform": key.Platform,
        },
    }

    manifestBytes, _ := json.Marshal(manifest)

    // Tag is the action key hash — deterministic, lookupable
    tag := actionKeyToTag(key)
    // e.g. "registry.internal.io/forge-cache/actions:sha256-abcdef1234..."
    ref := fmt.Sprintf("%s/actions:%s", s.repoBase, tag)

    return s.repo.PushReference(ctx, 
        v1.Descriptor{
            MediaType: v1.MediaTypeImageManifest,
            Size:      int64(len(manifestBytes)),
        },
        bytes.NewReader(manifestBytes), ref)
}

func (s *OCIStore) GetActionResult(ctx context.Context, 
    key ActionKey) (*ActionResult, error) {
    
    tag := actionKeyToTag(key)
    ref := fmt.Sprintf("%s/actions:%s", s.repoBase, tag)

    // Pull the manifest
    desc, manifestReader, err := s.repo.FetchReference(ctx, ref)
    if err != nil {
        if isNotFound(err) {
            return nil, nil // cache miss
        }
        return nil, err
    }

    var manifest v1.Manifest
    json.NewDecoder(manifestReader).Decode(&manifest)

    // Fetch the config to get the ActionResult
    configReader, _ := s.repo.Fetch(ctx, manifest.Config)
    var result ActionResult
    json.NewDecoder(configReader).Decode(&result)
    result.Cached = true

    return &result, nil
}

func actionKeyToTag(key ActionKey) string {
    h := sha256.New()
    json.NewEncoder(h).Encode(key)
    return "action-" + hex.EncodeToString(h.Sum(nil))[:16]
}
```

### Toolchains as OCI Images

This is where it really pays off. Bootstrapped toolchains are stored as actual OCI images. Which means you can pull a toolchain on any machine, including in CI, using standard container tooling:

```go
func (s *OCIStore) PutToolchain(ctx context.Context, name, version string,
    files map[string]string) (digest.Digest, error) {
    
    // Pack the toolchain directory into a single layer tarball
    layerBuf := &bytes.Buffer{}
    tw := tar.NewWriter(layerBuf)
    for relPath, absPath := range files {
        addFileToTar(tw, relPath, absPath)
    }
    tw.Close()

    layerDgst, _ := s.Put(ctx, bytes.NewReader(layerBuf.Bytes()))

    config := v1.ImageConfig{
        Labels: map[string]string{
            "forge.toolchain.name":    name,
            "forge.toolchain.version": version,
        },
    }

    // Build a proper OCI image — this means standard tools can inspect it
    // crane pull registry.internal.io/forge-cache/toolchains/go:1.22.4
    manifest := v1.Manifest{
        SchemaVersion: 2,
        MediaType:     v1.MediaTypeImageManifest,
        Config: v1.Descriptor{
            MediaType: v1.MediaTypeImageConfig,
            // ... config blob
        },
        Layers: []v1.Descriptor{{
            MediaType: MediaTypeForgeToolchain,
            Digest:    layerDgst,
            Size:      int64(layerBuf.Len()),
        }},
    }

    ref := fmt.Sprintf("%s/toolchains/%s:%s", s.repoBase, name, version)
    return s.pushManifest(ctx, manifest, ref)
}
```

## The Cache Topology

Users configure a chain of caches, checked in order:

```edn
;; forge.config.edn
{:cache
 {:backends
  [;; L1: local disk, always present
   {:type :disk
    :path "~/.forge/cache"
    :max-size "10GB"}

   ;; L2: team-shared OCI registry
   {:type :oci
    :registry "registry.internal.io/forge-cache"
    :auth :docker-config     ;; use ~/.docker/config.json
    :read true
    :write true}

   ;; L3: org-wide read-only mirror (CI publishes here)
   {:type :oci
    :registry "us-docker.pkg.dev/myorg/forge-cache"
    :auth :gcloud             ;; use gcloud ADC
    :read true
    :write false}]           ;; devs read, CI writes

  :policy
  {:write-through true        ;; write to all writable backends
   :read-repair true          ;; if found in L3, copy to L1+L2
   :gc {:max-age "30d"        ;; local cache GC
        :keep-toolchains true}}}}
```

The coordinator wraps this as a tiered store:

```go
package cas

type TieredStore struct {
    backends []Store
    writable []bool
    repair   bool
}

func (t *TieredStore) Get(ctx context.Context, dgst digest.Digest) (io.ReadCloser, error) {
    for i, backend := range t.backends {
        r, err := backend.Get(ctx, dgst)
        if err == nil {
            // Read-repair: copy to faster tiers
            if t.repair && i > 0 {
                go t.backfill(context.Background(), dgst, i)
            }
            return r, nil
        }
    }
    return nil, ErrNotFound
}

func (t *TieredStore) Put(ctx context.Context, r io.Reader) (digest.Digest, error) {
    // Buffer once, write to all writable backends
    data, _ := io.ReadAll(r)
    var dgst digest.Digest
    var firstErr error

    for i, backend := range t.backends {
        if !t.writable[i] {
            continue
        }
        d, err := backend.Put(ctx, bytes.NewReader(data))
        if err != nil && firstErr == nil {
            firstErr = err
            continue
        }
        dgst = d
    }
    return dgst, firstErr
}
```

## Why OCI Over Raw S3/GCS

You could use S3 as a blob store (and Bazel does this), but OCI gives you several things for free:

**Auth you already have.** Every org has container registry auth set up — Docker Hub, ECR, GCR, ACR, Harbor, Artifactory. You don't need a new IAM policy for a new S3 bucket. `docker login` and you're done.

**Manifest semantics.** An OCI manifest is a structured object that groups blobs with metadata. An S3 key is a flat string. The manifest structure means action results naturally group their output blobs, and you can attach annotations (build metadata, timestamps, provenance) without a sidecar database.

**Multi-platform support.** OCI manifest indexes let you store the same toolchain for linux/amd64 and darwin/arm64 under the same tag. The coordinator pulls the right one for the current platform. You'd have to invent this for S3.

**Replication and distribution.** Registry mirroring, geo-replication, pull-through caches — all built into the ecosystem. Want your CI cache available in three regions? That's a registry config, not something you build.

**Inspection.** `crane ls registry.internal.io/forge-cache/toolchains` shows you all cached toolchains. `crane manifest registry.internal.io/forge-cache/actions:sha256-abc...` shows you exactly what a cached action produced. You can debug the cache with standard tools.

**SBOM and provenance.** OCI supports referrers — you can attach SBOM, SLSA provenance, and vulnerability scan results as artifacts that reference a build output. This connects directly to your compliance / paved road interests. The bootstrap plugin stores a toolchain, then a separate action attaches a provenance attestation:

```edn
{:toolchain "//toolchains:go"
 :config {;; ...
          :provenance {:require-slsa-l2 true
                       :attest-to-registry true}}}
```

## What the Registry Namespace Looks Like

```
registry.internal.io/forge-cache/
├── blobs/                          # raw CAS blobs (shared)
├── actions/                        # action cache manifests
│   ├── :action-a1b2c3d4e5f6...    # tagged by action key hash
│   ├── :action-f7e8d9c0b1a2...
│   └── ...
├── toolchains/
│   ├── go:1.22.4                   # multi-platform manifest index
│   ├── rust:1.78.0
│   └── protoc:26.1
└── outputs/                        # final build outputs (optional)
    ├── api-server:abc123           # tagged by content hash
    └── api-server:latest           # convenience tag
```

The nice thing is it's all just OCI. `crane catalog registry.internal.io/forge-cache` lists everything. Your existing registry GC, monitoring, and access policies all just work.

## User

one more question - is there any reason to use cue as part of the api for this tool? would it provide any benefit?

## Claude

Honestly, yes — but not everywhere. CUE's value is concentrated in a specific part of this system, and using it beyond that would be fighting the tool.

## Where CUE Helps: The Configuration Surface

Build files are where CUE shines. Right now we have this:

```edn
{:target "//services/api:binary"
 :toolchain :go
 :sources ["cmd/api/**/*.go" "internal/**/*.go"]
 :deps ["//lib/auth" "//lib/db"]
 :config {:output "api-server"
          :gcflags "all=-N -l"
          :tags ["dev"]}}
```

The problem that emerges at scale isn't writing these — it's keeping them consistent across hundreds of targets. "Every Go binary must have race detection in test configs." "Every Docker image must use our base image." "Every service in the payments team must have the compliance toolchain." This is constraint enforcement over configuration, and that's exactly what CUE was built for.

```cue
package forge

// Schema: what a target looks like
#Target: {
    target:    string & =~"^//[a-z]"
    toolchain: #Toolchain
    sources:   [...string]
    deps:      [...string]
    config:    {...}  // toolchain-specific, validated elsewhere
}

#Toolchain: "go" | "rust" | "docker" | "protobuf" | "bootstrap"

// Policy: org-wide constraints
#GoTarget: #Target & {
    toolchain: "go"
    config: {
        // Every Go target must specify a module
        module: string
        // Race detector on by default
        race: *true | bool
        // Must use at least 1.21
        goversion: string & =~"^1\\.2[1-9]"
    }
}

#DockerTarget: #Target & {
    toolchain: "docker"
    config: {
        // Must use internal registry
        registry: string & =~"\\.internal\\.io$"
        // Must target a specific stage (no implicit last-stage)
        target: string
        // Platform is required, no implicit defaults
        platform: string
    }
}

// Concrete target — CUE validates this against the schema + policy
"//services/api:binary": #GoTarget & {
    target:    "//services/api:binary"
    sources:   ["cmd/api/**/*.go", "internal/**/*.go"]
    deps:      ["//lib/auth", "//lib/db"]
    config: {
        module:    "github.com/myorg/api"
        output:    "api-server"
        goversion: "1.22"
        // race is defaulted to true by #GoTarget
    }
}
```

This gives you something that EDN alone can't: **the schema, the policy, and the data are all in the same language, and validation is structural unification, not imperative checks.** You don't write a validator — you write constraints, and CUE tells you whether your config satisfies them. That's much more powerful than JSON Schema or hand-rolled validation in the coordinator.

## Where CUE Also Helps: Toolchain Definitions

The `toolchains.edn` file is another natural fit. Toolchain definitions are inherently cross-cutting — you want to express things like "every toolchain must have a sha256 for every platform we support" or "all toolchains for production must have SLSA provenance":

```cue
#ToolchainSource: {
    url:          string & =~"^https://"
    sha256:       string & =~"^[a-f0-9]{64}$"
    strip_prefix: *"" | string
}

#Toolchain: {
    toolchain: string
    plugin:    "bootstrap"
    config: {
        name:    string
        version: string & =~"^[0-9]+\\.[0-9]+"
        sources: {
            // Require both platforms for production
            "linux/amd64":  #ToolchainSource
            "darwin/arm64": #ToolchainSource
            // Others optional
            [string]:       #ToolchainSource
        }
        provides: {[string]: string}
    }
}
```

## Where CUE Does NOT Help

**The plugin protocol.** The request/response between coordinator and plugin is runtime data — specific hashes, file lists, action graphs. CUE is a configuration language, not a wire format. You'd be serializing CUE to send over stdin, which is pointlessly awkward compared to EDN or JSON. The plugin protocol should stay as simple structured data.

**Action definitions.** The output of a `plan` call — the action subgraph — is computed, dynamic, and specific to each invocation. CUE's strength is constraining and validating static(ish) configuration. An action graph is a runtime artifact.

**Service definitions for dev.** This one's debatable. You *could* use CUE for the docker-compose-like service definitions, and you'd get schema validation. But services have a lot of runtime interpolation (`${DB_USER}`, artifact path substitution), and CUE's string interpolation is intentionally limited. EDN with simple variable substitution is more practical here.

## The Architecture

CUE becomes the **front-end** — the human-facing configuration layer. The coordinator evaluates CUE into plain data structures, then everything downstream speaks JSON/EDN:

```
                    ┌──────────────┐
  BUILD.cue ───────►│              │
  policy.cue ──────►│  CUE eval    │──── validated JSON ────► Coordinator
  toolchains.cue ──►│              │                          │
                    └──────────────┘                          │
                                                              ▼
                                                     Plugin protocol
                                                     (JSON/EDN/msgpack)
```

In Go, this is clean — the `cuelang.org/go/cue` package lets you load, unify, and validate CUE, then export to Go structs:

```go
package config

import (
    "cuelang.org/go/cue"
    "cuelang.org/go/cue/cuecontext"
    "cuelang.org/go/cue/load"
)

type BuildConfig struct {
    Targets    []Target
    Toolchains []Toolchain
    Services   []Service
}

func LoadBuildFiles(dir string) (*BuildConfig, error) {
    ctx := cuecontext.New()
    
    // Load all .cue files in the directory tree
    instances := load.Instances([]string{"."}, &load.Config{Dir: dir})
    
    val := ctx.BuildInstance(instances[0])
    if err := val.Err(); err != nil {
        return nil, fmt.Errorf("cue schema error: %w", err)
    }

    // Validate — this is where policy constraints fire
    if err := val.Validate(cue.Concrete(true)); err != nil {
        return nil, fmt.Errorf("policy violation: %w", err)
    }

    var config BuildConfig
    if err := val.Decode(&config); err != nil {
        return nil, err
    }
    return &config, nil
}
```

## The Real Win: Policy as Code

The killer feature for your IDP context is that CUE lets you distribute policy separately from build definitions. A platform team publishes a CUE module with schemas and constraints:

```
forge-policy/
├── cue.mod/
├── go.cue          # #GoTarget schema + org constraints
├── docker.cue      # #DockerTarget schema + org constraints
├── security.cue    # cross-cutting security requirements
└── compliance.cue  # regulatory constraints
```

Teams import it:

```cue
package myservice

import "github.com/myorg/forge-policy"

// This is now validated against org policy automatically
"//services/payments:binary": forge_policy.#GoTarget & {
    // ...
}
```

If the policy says "all Docker images must use our base image" and someone tries to use `ubuntu:latest`, CUE fails at eval time, before anything builds, with a structural error message that says exactly which constraint was violated. No linter, no CI check, no runtime error — it's just not a valid configuration.

That's much harder to do in EDN (you'd need custom validation logic) or in Starlark (Bazel's approach, which mixes policy with imperative code and makes it hard to reason about what's actually enforced).

## The Tradeoff

CUE adds a dependency and a learning curve. The mental model of lattice-based unification isn't intuitive for everyone. If the team is small and the build configs are simple, CUE is overhead. But if you're building an IDP where the whole point is "teams can self-serve but can't violate guardrails" — CUE is probably the best tool that exists for expressing those guardrails declaratively.

The pragmatic path: start with EDN build files, keep the coordinator's config ingestion behind an interface, and add CUE as an alternative frontend when you need the policy layer. The plugin protocol and everything downstream never changes.

## User

also, i want the tool to be called mu - for emptiness

## Claude

Perfect name. A build tool that knows nothing — no languages, no compilers, no opinions. It just coordinates. The emptiness is the point: `mu` has no built-in semantics, so it can be anything.

There's a nice recursive quality to it too. In the koan sense, "mu" is the answer to a question that rejects the premise. "Does your build tool support Go?" Mu. It doesn't support anything. It doesn't *not* support anything. It just runs actions on a graph.

It also works well practically — two characters, easy to type, distinctive in a terminal:

```
$ mu build //services/api:binary
$ mu dev
$ mu test //...
$ mu bootstrap
```

And the plugin interface becomes:

```
$ mu plugin list
  bootstrap    0.1.0  (built-in)
  go           0.3.1  bb
  rust         0.2.0  bb
  docker       0.1.0  bb
  protobuf     0.1.4  bb

$ mu plugin discover rust
  consumes: source:rust, native-library, protobuf-schema
  produces: native-library, executable, rust-metadata
```

The config files read naturally too — `mu.cue` for the project root, `mu.lock` for pinned toolchains, `mu.plugins.edn` for plugin registration. Or just `mu.edn` if you're not using CUE yet.

```
my-project/
├── mu.cue              # root config, cache settings, policy imports
├── mu.lock             # pinned toolchain hashes
├── mu.plugins.edn      # plugin registry
├── toolchains.cue      # toolchain definitions
├── plugins/
│   ├── go/plugin.bb
│   ├── rust/plugin.bb
│   └── docker/plugin.bb
├── lib/
│   ├── auth/BUILD.cue
│   └── db/BUILD.cue
└── services/
    └── api/
        ├── BUILD.cue
        └── dev.cue     # service + trigger definitions
```

The emptiness extends to error messages too. When something fails, `mu` can only tell you what it knows — the action failed, here are the inputs, here's the output. It doesn't know *why* your Rust code didn't compile. That's the plugin's job to surface. `mu` just faithfully reports what the action returned:

```
$ mu build //lib/crypto

  ✗ //lib/crypto (action: compile-aes)
    plugin: rust
    exit:   1
    stderr: ← from rustc, not from mu
      error[E0308]: mismatched types
        --> aes.rs:42:5
```

`mu` is the empty vessel. The plugins fill it with meaning.

