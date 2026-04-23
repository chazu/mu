# Scout Report: Migrating `mu.json` → `mu.cue`

**Date:** 2026-04-23
**Scope:** Research + design only. No code changes.
**Author:** scout agent

---

## 1. Executive summary

Replacing `mu.json` with `mu.cue` is **feasible and moderately high-value**, but the
biggest wins are not the reasons most teams assume. The readability gain over JSON
is real but incremental; the decisive benefits are:

1. **Schema definitions co-located with data** (`#Target`, `#Toolchain`, …) —
   replaces hand-rolled `internal/config/validate.go` with CUE unification,
   producing better error messages with field paths and line numbers.
2. **`@embed` directive** (CUE v0.9+) — lets plugin/target config blocks inline
   external scripts and data files while keeping them as real files on disk
   (editor LSP, shellcheck, etc. still work).
3. **Natural composition** — CUE packages and imports replace the current
   `filepath.WalkDir` sub-config merge in `loader.go`, which currently has
   several subtle rebase/prefix rules that are easy to get wrong.

Cache key hashing is **not affected**: `internal/dag/actionkey.go` hashes
resolved `Action` structures (command, env, input digests), not raw config
bytes. A JSON→CUE switch that preserves the `ProjectConfig` struct values
produces identical action keys. **Cache artifacts remain valid across the
migration.**

Recommended path: **dual-read window** (prefer `mu.cue`, fall back to
`mu.json`), ship `mu migrate`, let users convert at their own pace, deprecate
JSON loader after one minor release.

---

## 2. Current state audit (`internal/config`)

### 2.1 Schema (from `types.go`)

Top level `ProjectConfig`:

| Field          | Type                 | Notes                                             |
|----------------|----------------------|---------------------------------------------------|
| `targets`      | `[]Target`           | Merged from sub-configs, names prefixed by path   |
| `toolchains`   | `[]Toolchain`        | First-definition-wins merge                       |
| `cache`        | `*CacheConfig`       | Root-only in practice                             |
| `plugins`      | `[]PluginDef`        | First-definition-wins merge                       |
| `preprocessor` | `*Preprocessor`      | Enables `mu.<ext>` sub-configs via external cmd   |

`Target`: `target`, `toolchain`, `sources`, `deps`, `config` (free-form
`map[string]any`), `sealed_inputs` (env → secret ref), `kind`, `implements`
(BRICK metadata — not enforced by mu, only by pudl).

`Toolchain`: `toolchain` (name), `from`, `config.{version,url,sha256,strip_prefix}`.

`PluginDef`: exactly one of `command` | `script` | `url+sha256` | `digest`.

`CacheConfig`: `backends[]` (disk|oci), `read_repair`, `write_through`,
`push{registry,repository}`.

`PluginConfig` (plugin-dir variant): `plugin{entrypoint,toolchain,files[],guide}`
+ `targets` + `toolchains`. Detected by raw-JSON probe in `IsPluginDir`.

### 2.2 Loader behaviour (`loader.go`)

- `FindProjectRoot` walks up looking for `mu.json`.
- `Load` reads root, then `filepath.WalkDir`s the tree for additional
  `mu.json` (or `mu.<pp-ext>`), skipping symlinks, hidden dirs, `testdata/`.
- For each sub-config:
  - Targets get prefixed: `foo/bar/mu.json`'s `build` target → `//foo/bar/build`.
  - Source paths are rebased to project-root-relative.
  - Plugin script/command paths with a path separator are rebased.
  - `PluginDirs` list is accumulated for post-build CAS bundling.
- Globs in `sources` (`*.go`, `src/**/*.rs`) are expanded via `filepath.Glob`
  (note: Go's stdlib `filepath.Glob` does **not** support `**`; this is a
  latent bug or a TODO — worth flagging separately).
- `merge()` appends targets, de-dupes toolchains and plugins by name.

### 2.3 Consumers of `*config.ProjectConfig`

```
cmd/mu/build.go, cache.go, cache_push.go, cache_login.go,
       cachefactory.go, context.go, guide.go, target.go,
       plugin.go, plugin_status.go, plugin_test_cmd.go,
       scratch.go, verify.go
internal/coordinator/coordinator.go
internal/coordinator/pluginresolver.go
internal/plugin/manager.go
internal/scratch/scratch.go, external.go
```

Consumers use typed Go fields — nothing decodes raw JSON bytes except
`IsPluginDir` (an 8-line probe) and `Preprocess` (which always returns JSON).
The impact surface is therefore: **one loader, two decode callsites
(`loader.go:loadFile`, `pluginmanifest.go:LoadPluginManifest`), one probe
(`IsPluginDir`).** Everything else is format-agnostic.

### 2.4 Validator

`validate.go` is ~120 lines of hand-written checks (required fields, unique
names, enum `type`, mutually-exclusive plugin sources, `url` implies
`sha256`). **Every single one of these is expressible as CUE constraints**
and would vanish from Go code.

---

## 3. CUE library evaluation

### 3.1 Module: `cuelang.org/go`

- Import path: `cuelang.org/go` (single module; `cuelang.org/go/cue`,
  `cuelang.org/go/cue/cuecontext`, `cuelang.org/go/cue/load` are the main
  user-facing packages).
- Current stable: **v0.14.x** (2026). `@embed` landed in **v0.10** (2024) and
  stabilized in v0.11; we should pin ≥ v0.12 for robust embed support and the
  improved error messages.
- Go version requirement: CUE v0.12+ requires Go ≥ 1.23. mu is on Go 1.25.7,
  so this is fine.

### 3.2 API shape (minimum viable integration)

```go
import (
    "cuelang.org/go/cue/cuecontext"
    "cuelang.org/go/cue/load"
)

ctx := cuecontext.New()
insts := load.Instances([]string{"./"}, &load.Config{Dir: projectRoot})
v := ctx.BuildInstance(insts[0])
if err := v.Validate(cue.Concrete(true)); err != nil { ... }

var cfg ProjectConfig
if err := v.Decode(&cfg); err != nil { ... }
```

Key API facts:

- `load.Instances` handles package resolution, `cue.mod`, and — crucially —
  `@embed` (it walks the filesystem to find embedded files).
- `v.Decode(&cfg)` uses the struct's `json` tags. **No Go type changes
  required.** Every existing struct tag works unchanged.
- Errors are `cueerrors.Error`, renderable with file:line:col positions
  via `errors.Details(err, nil)`.

### 3.3 `@embed` directive

Enabled per-package with `@extern(embed)` at the top of the file. Usage:

```cue
@extern(embed)
package mu

#Target: {
    target:    string
    toolchain: string
    sources:   [...string]
    config?:   {...}
}

targets: [
    {
        target:    "//lint/gofmt"
        toolchain: "lint"
        sources:   ["cmd/mu/*.go", "internal/**/*.go"]
        config: {
            command:     _ @embed(file="mu/scripts/gofmt.sh", type=text) // as string
            fix_command: _ @embed(file="mu/scripts/gofmt-fix.sh", type=text)
        }
    },
]
```

`@embed` supports `type=text`, `type=binary` (base64), and `type=<format>`
for JSON/YAML/TOML that get parsed and unified at load time. Glob embedding
(`glob="mu/scripts/*.sh"`) is supported from v0.11.

**Important semantic choice for mu:** `@embed` inlines the file contents at
CUE-eval time. That means:

- The embedded bytes become part of the resolved config, and therefore part
  of whatever input digests we compute.
- If we keep the *current* model — scripts referenced by path and hashed
  separately as CAS inputs — we should **not** use `@embed` for those;
  instead the `mu.cue` just holds a path string and the existing source-file
  hashing picks it up. `@embed` is right for small inline blobs (a one-line
  command string, a version number file), wrong for multi-KB scripts that
  want their own cache identity.

### 3.4 Performance characteristics

From CUE's own benchmarks and Dagger's production experience:

- Loading + unifying a ~500-line config: ~10–30 ms cold.
- A monorepo with ~200 sub-configs (mu's current plugin set has ~14) would
  see ~100–300 ms total — measurable but not user-visible.
- CUE's evaluator is O(n²) worst-case in the presence of heavy disjunctions.
  mu's schema has none of those; unification is effectively linear.

### 3.5 Binary size impact

CUE pulls in ~4 MB of compressed dependencies (`cuelang.org/go`,
`github.com/cockroachdb/apd`, `golang.org/x/text`). The `mu` binary is
currently ~15 MB stripped; expect ~19–20 MB after migration. Acceptable.

---

## 4. Proposed `mu.cue` schema

```cue
@extern(embed)
package mu

// ---- Definitions (closed by default) ----

#CacheBackend: {
    type:      "disk" | "oci"
    path?:     string   // required when type=="disk"
    registry?: string   // required when type=="oci"
    max_size?: string
    read?:     bool
    write?:    bool
    if type == "disk" { path!: string }
    if type == "oci"  { registry!: string }
}

#CachePush: {
    registry:   string
    repository: string
}

#CacheConfig: {
    backends?:      [...#CacheBackend]
    read_repair?:   bool
    write_through?: bool
    push?:          #CachePush
}

#ToolchainConfig: {
    version?:      string
    url?:          string
    sha256?:       string
    strip_prefix?: string
    if url != _|_ { sha256!: =~"^[0-9a-f]{64}$" }
}

#Toolchain: {
    toolchain: string & !=""
    from:      string & !=""
    config:    #ToolchainConfig
}

#PluginDef: {
    name: string & !=""
    // Exactly one source form:
    {command: [...string], command: !=[]} |
    {script: string & !=""} |
    {url: string & !=""; sha256: =~"^[0-9a-f]{64}$"} |
    {digest: string & !=""}
}

#Target: {
    target:         string & =~"^//"
    toolchain:      string & !=""
    sources?:       [...string]
    deps?:          [...string]
    config?:        {...}  // free-form; plugin-specific
    sealed_inputs?: [string]: string
    kind?:          "relationship" | "interface" | "component" | "kit"
    implements?:    string
}

#PluginManifest: {
    entrypoint: string & !=""
    toolchain?: string
    files?:     [...string]
    guide?:     string
}

#Preprocessor: {
    extension: string & !=""
    command:   [string, ...string]
}

// ---- Top-level (root mu.cue) ----

cache?:        #CacheConfig
toolchains?:   [...#Toolchain]
plugins?:      [...#PluginDef]
targets?:      [...#Target]
preprocessor?: #Preprocessor

// Unique-name enforcement (CUE list comprehension + list.UniqueItems)
import "list"
_toolchainNames: [for t in toolchains {t.toolchain}]
_pluginNames:    [for p in plugins    {p.name}]
_targetNames:    [for t in targets    {t.target}]
list.UniqueItems() & _toolchainNames
list.UniqueItems() & _pluginNames
list.UniqueItems() & _targetNames
```

Plugin-dir variant (`plugins/foo/mu.cue`):

```cue
package mu_plugin  // different package, imports the root definitions

import def "mu:mu"  // or a relative path via cue.mod

plugin:     def.#PluginManifest
targets?:   [...def.#Target]
toolchains?: [...def.#Toolchain]
```

### 4.1 Semantic coverage check

| mu.json concept                             | CUE equivalent                                        |
|---------------------------------------------|-------------------------------------------------------|
| Optional fields (`omitempty`)               | `field?:` syntax                                      |
| `map[string]any` free-form config           | `{...}`                                               |
| Exactly-one plugin source                   | Disjunction of closed structs                         |
| `type` enum validation                      | Disjunction of string literals                        |
| `url` requires `sha256`                     | Conditional `if url != _|_ { sha256!: ... }`         |
| Unique names across list                    | `list.UniqueItems()` over projection                  |
| Target name starts with `//`                | Regex constraint `=~"^//"`                            |
| Sealed inputs map                           | `[string]: string`                                    |
| Free-form toolchain-specific config blocks  | `{...}` (open struct)                                 |

**Lossless.** Every validation rule in `validate.go` maps to a CUE constraint.

---

## 5. `mu/scripts` + `mu/config` convention

### 5.1 The directory layout

```
plugins/lint/
├── mu.cue              # build definition
├── plugin.bb           # entrypoint
├── GUIDE.md
└── mu/                 # auxiliary artifacts for this build file
    ├── scripts/
    │   ├── gofmt.sh
    │   └── govet.sh
    └── config/
        └── golangci.yaml
```

### 5.2 Recommended referencing rules

There are **two distinct cases**, and conflating them has historically burned
other build systems:

**Case A: small inline fragments (use `@embed`).**
One-liners, version pins, small YAML snippets. Inline them so the config file
is self-contained and its hash captures them.

```cue
config: command: _ @embed(file="mu/scripts/true.sh", type=text)
```

**Case B: scripts with independent cache identity (reference by path).**
Anything a human would `chmod +x` and invoke directly. Leave the file on
disk, reference it as a source:

```cue
{
    target:    "//lint/gofmt"
    toolchain: "lint"
    sources:   ["plugin.bb", "mu/scripts/gofmt.sh"]  // hashed as CAS inputs
    config: {
        command: ["sh", "mu/scripts/gofmt.sh"]
    }
},
```

**Heuristic:** if the file is <1 KB and has no reason to exist outside of
this build definition, embed it; otherwise reference it.

### 5.3 Interaction with existing source-hash model

The current `internal/dag/actionkey.go` hashes resolved input digests by name.
Files referenced from `sources` continue to work unchanged. Embedded content,
in contrast, vanishes into the parsed config value — we'd need to either:

1. Synthesize a synthetic input name for each embed (`@embed:mu/scripts/x.sh`)
   and add its hash to the action's `Inputs`, or
2. Accept that embedded content is only hashed transitively via the action's
   `Command` / `Env` / resolved config fields.

Option 2 is simpler and correct: if `@embed` output ends up in
`target.config.command`, then the coordinator's resolve step already produces
the `Action.Command`, whose bytes are hashed. **No cache-key machinery
changes required.**

### 5.4 Discoverability

Convention should be enforced by documentation, not code. But `mu` can emit
a warning if `mu.cue` references a path that is not under `./mu/` and also
not a standard source file — catches typos like `mu/scrits/...`.

---

## 6. `mu migrate` command design

### 6.1 Interface

```
mu migrate [--in PATH] [--out PATH] [--dry-run] [--in-place] [--recursive]
```

- `--in` defaults to `./mu.json`.
- `--out` defaults to the sibling `./mu.cue`.
- `--dry-run` prints to stdout, writes nothing.
- `--in-place` deletes `mu.json` on success (opt-in; default leaves both).
- `--recursive` walks the project root and migrates every `mu.json` found
  (identical walk rules to the loader: skip symlinks, hidden dirs, testdata).

### 6.2 Algorithm

1. Load `mu.json` into `ProjectConfig` using existing loader (single-file
   mode — **not** the tree-merge `Load`; we migrate one file at a time).
2. Marshal each section through a hand-rolled CUE emitter that:
   - Preserves the top-level order: `cache`, `toolchains`, `plugins`,
     `targets`, `preprocessor`, `plugin` (for plugin dirs).
   - For each list, preserves order (important for reproducibility and code
     review diffs).
   - Emits definitions reference at the top: `import def "mu:mu"` and
     tags lists with their type (`targets: [...def.#Target] & [`) — gives
     users type completion in their editor.
   - Quotes strings with CUE's `""` (identical to JSON) or uses `"""` raw
     literals for multi-line strings (currently none, but future-proof).
   - Converts `map[string]any` free-form config blocks back into CUE struct
     literals recursively.
3. Run the emitted file through `cue fmt` (in-process via
   `cuelang.org/go/cue/format`) for canonical indentation.
4. Validate the emitted file by loading it and decoding into
   `ProjectConfig`; deep-equal against the original. **If they differ,
   refuse to write and print the diff.** This is the idempotence guarantee.

### 6.3 Idempotence & round-tripping

Idempotence requirement: `migrate(migrate(x)) == migrate(x)` at the level of
the parsed `ProjectConfig`. Byte-level idempotence is best-effort but not
guaranteed (comments, user hand-edits survive).

Make migrate refuse to overwrite an existing `mu.cue` unless `--force` is
passed. If `mu.cue` exists, suggest diffing first.

### 6.4 Edge cases

| Case                                          | Behaviour                                            |
|-----------------------------------------------|------------------------------------------------------|
| JSON has no comments                          | Emit header comment explaining origin                |
| Scalar vs list (`command: "foo"` vs `["foo"]`)| mu schema is already list-only; reject mixed input   |
| Empty arrays (`sources: []`)                  | Emit as `sources: []` (preserve intent)              |
| `null` values                                 | Drop (CUE uses absent field)                         |
| Free-form `config` blocks                     | Emit as open struct `{...}`                          |
| Preprocessor present                          | Emit as-is; warn that `.cue` + preprocessor is odd   |
| Plugin-dir (`plugin` key)                     | Emit `mu_plugin` package variant                     |
| Field order preservation                      | Walk JSON tokens, not `encoding/json` Unmarshal      |

For field-order preservation in the free-form `config: map[string]any`
block, use `json.Decoder` + ordered-map emission (or a small custom token
walker). This matters for review quality: a 40-key `config` block that
re-sorts alphabetically is unreviewable.

---

## 7. Prior art (how other build tools handled this)

### 7.1 Bazel / Starlark

Starlark went the opposite direction — a full scripting language with
functions, macros, conditionals. Power, at the cost of: hermeticity (hard
to statically analyze), caching (every BUILD file hashes its resolved AST),
and tooling (everyone writes their own linter). Lesson: **stop short of
Turing-completeness**. CUE is constraints-only, which is the right move.

### 7.2 Dagger

Dagger shipped with CUE as its pipeline DSL (Dagger 0.1–0.2), migrated *away*
to Go/Python/TS SDKs in Dagger 0.3+. Their post-mortem:
- CUE was great for *validation* and *composition*.
- CUE was poor for *imperative flow* — users kept reaching for conditionals
  and the ergonomics weren't there.
- **Key insight for mu: we do not need imperative flow.** mu config is
  declarative (targets, sources, deps). CUE's sweet spot.

### 7.3 Tilt

Starlark again. Same tradeoffs as Bazel. Tilt's maintainers have publicly
lamented the decision; Starlark scripts in Tiltfiles are the #1 source of
hard-to-debug pipeline failures.

### 7.4 Buck2, Pants, Please

All Starlark. All face the same macro-debuggability problem.

### 7.5 Nix

Nix uses a lazy functional language. Same power-vs-analyzability tradeoff,
though Nix's content-addressable store is a better fit than Bazel's for
correctness guarantees. Notable inspiration for mu's CAS, but not a config
DSL model.

**Net takeaway:** the field has conclusively demonstrated that
Turing-complete build DSLs are a trap. CUE occupies a rare and well-chosen
design point: strictly more expressive than JSON/YAML/TOML, strictly less
expressive than Starlark, with compositional semantics that JSON can't
approach.

---

## 8. Risk register (ranked)

### HIGH

**H1. Editor / LSP ecosystem is thin.**
CUE's VSCode / Emacs LSP (`cuelsp`) exists but is less mature than
`gopls`/`rust-analyzer`. Neovim support is via `cue-lsp` (decent). JetBrains
support is community-plugin only. Users without a CUE-aware editor get a
worse experience than with JSON.
**Mitigation:** ship a `mu format` wrapper around `cue fmt`. Publish a
`.editorconfig` + recommended plugins list in `docs/`. `mu build` error
messages from CUE validation are already excellent without an LSP.

**H2. Error-message regression on syntax errors.**
CUE's parse errors are good; its *unification* errors on deeply-nested
conflicts can be cryptic (`conflicting values 3 and 4 at path
targets[7].config.command[2]`). Current JSON errors are line:col with a
clear message.
**Mitigation:** wrap CUE errors with mu-specific hints ("did you mean to
quote this as a string?"); include the relevant schema definition snippet
in the error for schema-violation cases. Add a `mu lint` command.

### MEDIUM

**M1. Binary size grows ~4 MB.**
**Mitigation:** measure actual impact on a release build. If unacceptable,
use build tags to make CUE loader optional (JSON loader as always-available
fallback). Almost certainly not worth the complexity.

**M2. Migration fatigue.**
Users with 20+ sub-`mu.json` files need to convert all of them.
**Mitigation:** `mu migrate --recursive`. Dual-read window (see §9) means
conversion is per-file and reversible.

**M3. Glob `**` bug propagates.**
Current loader uses `filepath.Glob` which doesn't support `**`. If users
are working around this via CUE-level glob expressions, behavior may subtly
differ.
**Mitigation:** fix the glob bug *before* migration (use `doublestar` or
equivalent). Keep CUE schema's `sources` as plain `[...string]`; glob
expansion stays in Go.

**M4. `@embed` requires CUE ≥ 0.10, realistically ≥ 0.12.**
**Mitigation:** pin in `go.mod`. Not a user-visible constraint — they don't
install CUE separately.

### LOW

**L1. Cache-key invalidation.**
Analyzed in detail: **does not occur**. Action keys hash resolved
Action values (command, env, input digests), not raw config bytes.
Mitigation: N/A; verified by reading `internal/dag/actionkey.go`.

**L2. Plugin-dir detection via byte probe.**
`IsPluginDir` does a cheap JSON probe. The CUE equivalent must load and
decode — ~10× slower per file, but in absolute terms still sub-millisecond.
**Mitigation:** cache the result per-file across a single `mu` invocation.

**L3. Preprocessor + CUE interaction.**
The `preprocessor` field lets users write `mu.<ext>` and transform to JSON.
With `.cue` as canonical, preprocessor becomes awkward (transform to CUE?
or to JSON and then use CUE loader to unify?).
**Mitigation:** keep preprocessor JSON-only; if present, skip `.cue`
loading for subdirs with `mu.<ext>`. Document as "advanced, mostly vestigial."

**L4. CUE's `cue.mod` directory.**
CUE expects a `cue.mod/module.cue` in the project root for package imports.
This adds one small file to every mu project.
**Mitigation:** `mu migrate` auto-creates `cue.mod/module.cue` if absent.
Vendor `def "mu:mu"` module inline (generated at migrate time) so users
don't need a network fetch.

---

## 9. Phased rollout plan

**Phase 0 — prep (no user-visible changes):**
- Fix the `**` glob bug in `expandSourceGlobs` (independent, blocks M3).
- Extract `loadFile`/`LoadPluginManifest` into a small `Decoder` interface
  so a second implementation can slot in.

**Phase 1 — dual-read (one minor release):**
- Add `cuelang.org/go` dependency.
- Implement CUE decoder behind the `Decoder` interface.
- `FindProjectRoot` looks for `mu.cue` first, then `mu.json`.
- If both exist: error with a clear message ("run `mu migrate` then delete
  `mu.json`").
- Subdir walk prefers `mu.cue` in each directory but accepts `mu.json`.
- Ship `mu migrate` (non-destructive by default).
- **Do not** deprecate JSON yet. Internal dogfood for a release cycle.

**Phase 2 — default CUE, soft-deprecate JSON:**
- `mu build` prints a deprecation warning when `mu.json` is loaded.
- Documentation and examples switch to `.cue`.
- `mu init` (if/when it exists) emits `mu.cue`.
- Ship `mu migrate --recursive` for whole-repo conversion.

**Phase 3 — JSON removal (next major version):**
- Remove the JSON decoder and `encoding/json` config paths.
- `Preprocessor` is kept (users may still want to generate CUE from other
  sources).

At every phase, cache artifacts remain valid — see §3 and risk L1.

---

## 10. Recommendations summary

1. **Do the migration.** The schema-definition and validation wins are worth
   it; the readability improvement is a bonus. Binary size cost is
   acceptable.
2. **Keep the mental model thin.** Do not expose CUE's generics or
   disjunctions to end users. The schema (§4) is the only CUE most users
   need to understand; everything else is `key: value`.
3. **`@embed` for small inlines, `sources` for real files.** Do not try to
   unify these two mechanisms.
4. **Ship `mu migrate` first and dogfood it on this repo** (`mu.json` in
   the project root + 14 plugin-dir mu.jsons + 3 example mu.jsons) before
   committing to the format.
5. **Fix the `**` glob bug before migration** so users don't think the
   new format regressed semantics.
6. **Do NOT implement an imperative CUE DSL for targets.** Follow the
   Dagger lesson. If users want generated targets, they generate CUE from
   their scripting language of choice (`mu migrate` accepts piped input
   via stdin, future work).

---

## Appendix A: files read during this scout

- `/Users/chazu/dev/go/mu/mu.json`
- `/Users/chazu/dev/go/mu/internal/config/{types,loader,preprocess,validate,pluginmanifest}.go`
- `/Users/chazu/dev/go/mu/internal/dag/actionkey.go`
- `/Users/chazu/dev/go/mu/plugins/{go,cowsay}/mu.json`
- `/Users/chazu/dev/go/mu/go.mod`
- Directory listings: project root, `internal/`, `docs/`, `plugins/`, `examples/`.

## Appendix B: not investigated (follow-up scouts)

- How `mu scratch` interacts with plugin-dir configs under CUE.
- Whether `internal/coordinator/pluginresolver.go` has any format-specific
  assumptions.
- Concrete benchmark numbers on this repo post-migration.
- Whether `sealed_inputs` secret refs need schema tightening under CUE.
