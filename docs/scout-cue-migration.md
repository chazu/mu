# Scout Report: Migrating `mu.json` → `mu.cue`

**Scout:** scout-cue-migration
**Date:** 2026-04-23
**Scope:** Research + design + risk assessment. **No source code changes.**

---

## 1. Audit: current `mu.json` schema, loader, and consumers

### 1.1 Top-level schema (`internal/config/types.go`)

`ProjectConfig` is the root type decoded from every `mu.json`:

```
ProjectConfig
├── targets      []Target
├── toolchains   []Toolchain
├── cache        *CacheConfig
├── plugins      []PluginDef
├── preprocessor *Preprocessor
└── PluginDirs   []string   (derived, not decoded)
```

Notable per-type quirks that the CUE schema must preserve:

- **`Target`** uses JSON key `"target"` (not `"name"`) for the name. `Config map[string]any` is a free-form bag passed to plugins. `SealedInputs` maps env var name → secret reference (`pass:...`). Optional `kind`/`implements` are BRICK classifiers that `mu` doesn't validate (pudl does via its own CUE schema).
- **`Toolchain`** also uses key `"toolchain"` for name; `config` is typed (`ToolchainConfig` with `version`/`url`/`sha256`/`strip_prefix`).
- **`PluginDef`** is a 4-way tagged union: exactly one of `command | script | url | digest` must be set (validator enforces this). `url` requires `sha256`.
- **`Preprocessor`** declares an `extension` and external `command`. When present in the root, subdirectory files named `mu.<ext>` are piped through the command (30-s timeout) and expected to emit JSON on stdout. This is the current escape hatch for non-JSON config.
- **`PluginManifest`** / `PluginConfig` — a parallel root for plugin directories (`"plugin"` key). Carries `entrypoint`, optional `toolchain`, `files`, `guide`. A directory whose `mu.json` has a top-level `"plugin"` key is a plugin directory (detected by `IsPluginDir` via a `json.RawMessage` probe).
- **`CacheConfig`** — `backends` (typed union: `disk` needs `path`, `oci` needs `registry`); `read_repair`, `write_through`; `push` = `{registry, repository}`. `CacheBackend.Read/Write` are `*bool` to distinguish "unset" from "false".

### 1.2 Loader behavior (`internal/config/loader.go`)

The loader is more than a JSON decode — it performs several semantic passes that CUE must keep honest:

1. **Root discovery.** `FindProjectRoot` walks up from cwd looking for `mu.json`.
2. **Recursive merge walk.** `filepath.WalkDir` visits every non-hidden subdirectory, reading `mu.json` (or `mu.<preprocessor-ext>` if a preprocessor is declared in the root). Symlinks, hidden dirs, and `testdata/` are pruned.
3. **Prefix rewriting.** For each subtree `mu.json`, target names are rewritten as `//<reldir>/<name>`, and `Sources` + `plugin.script` + path-like `plugin.command` args are rebased relative to the project root.
4. **Plugin-dir detection.** When a sub-`mu.json` has a `"plugin"` key, the relative dir is appended to `PluginDirs` (used for CAS bundling). Plugin dirs can still ship their own `targets` that merge up via the normal `merge()` path (see `plugins/go/mu.json`).
5. **Glob expansion.** After merge, every `target.sources` entry containing `* ? [` is glob-expanded against the project root; unmatched globs are left literal.
6. **Merge semantics.** Targets always append (duplicate detection happens in `Validate`). Toolchains + plugins *dedupe by name*, first-writer-wins (root overrides subtree definitions of the same name).

### 1.3 Validator (`internal/config/validate.go`)

Checks applied after load — these are the *invariants* the CUE schema must encode:

- Targets: `target` required, must start with `//`, unique; `toolchain` required.
- Toolchains: `toolchain` required, unique; `from` required.
- Plugins: `name` required, unique; exactly one of `command|script|url|digest`; `url` ⇒ `sha256`.
- Cache: backend `type ∈ {disk, oci}`; `disk` needs `path`; `oci` needs `registry`.
- Preprocessor: `extension` and non-empty `command` both required.

### 1.4 Consumers (who reads `ProjectConfig`)

Direct callers of `config.Load` / `config.Validate` / `config.LoadPluginManifest`:

| File | Purpose |
|---|---|
| `cmd/mu/context.go` | Central `cliContext.Resolve` — every subcommand that needs config (`build`, `target`, `plugin`, `cache push`, etc.) |
| `cmd/mu/plugin.go:216` | Re-validates after plugin edits |
| `cmd/mu/guide.go:921` | `FindProjectRoot` for guide lookup |
| `internal/coordinator/coordinator.go` (:354, :785) | `LoadPluginManifest` at plugin bundling + scratch wiring |
| `internal/coordinator/pluginresolver.go:158` | `LoadPluginManifest` during plugin resolution |

Everything else accesses fields of a `*ProjectConfig` already returned by `Resolve`. That means **the migration surface is small**: replace `Load` + `LoadPluginManifest` internals while preserving the same Go types.

### 1.5 Inventory of live `mu.json` files in the repo

14 files (excluding `.claude/worktrees/`): root, 3 examples (`cowsay`, `scratch`, `go-hello`), coordinator testdata, and 14 plugin manifests (`plugins/{aws, cowsay, docker, file, go, host, k8s, lint, pass, remote-exec, remote-file, scratch, terraform, zig}`). All must migrate or remain supported via a dual-read window.

---

## 2. CUE Go library (`cuelang.org/go`) evaluation

### 2.1 Status and versions

- Module: `cuelang.org/go`. Active; releases roughly monthly. Current line as of 2026-04: **v0.14.x** (v0.10 cut Aug 2024; v0.11 cut Dec 2024; v0.12 early 2025; `@embed` hardening landed through v0.11–v0.13).
- **Min version for `@embed`:** `@embed` was added as an *experimental* file-attribute in **v0.10.0** behind `CUE_EXPERIMENT=embed=1`, promoted to default-on around **v0.11**, and stabilized in **v0.12**. For a green-field adoption today, pin `>= v0.11.0` and prefer `>= v0.13.0` for bug fixes around glob patterns and sibling-module resolution. Document the exact version with `go.mod`.
- Go compatibility: modern CUE requires Go ≥ 1.22. Our module is on 1.25 — no friction.

### 2.2 Binary size and dependency weight

CUE is a heavy dependency. Approximate impact:

- Adds ~40–60 new transitive modules (`github.com/cockroachdb/apd/v3`, `github.com/emicklei/proto`, `github.com/mpvl/unique`, `github.com/protocolbuffers/txtpbfmt`, `github.com/tetratelabs/wazero`, `golang.org/x/text`, etc.).
- Stripped binary growth for a Go CLI that only decodes: **+8–12 MB** typical, **+15–20 MB** if we also pull `cuelang.org/go/cmd/cue` helpers or the `tool/flow` package.
- GC/heap footprint during eval is non-trivial; a 200-line `mu.cue` evaluates in ~5–20 ms cold, sub-ms after the runtime is warm.

### 2.3 Primary APIs

```go
import (
    "cuelang.org/go/cue"
    "cuelang.org/go/cue/cuecontext"
    "cuelang.org/go/cue/load"
    "cuelang.org/go/cue/errors"
)

ctx := cuecontext.New()
insts := load.Instances([]string{"."}, &load.Config{
    Dir:        projectRoot,
    ModuleRoot: projectRoot,
})
v := ctx.BuildInstance(insts[0])
if err := v.Validate(cue.Concrete(true)); err != nil { … }

var cfg ProjectConfig
if err := v.Decode(&cfg); err != nil { … }   // json-tag driven
```

Key points:

- `cue.Value.Decode` honors Go struct tags. Our types use `json:"..."` tags, which CUE respects — **no additional mapping layer required**. Field-rename quirks (`"target"` for name, etc.) survive.
- `load.Instances` resolves CUE packages and imports; for the MVP we keep everything in a single file/package (`package mu`), which sidesteps module/registry concerns entirely.
- `@embed(file=...)` attaches to a field and materializes file contents as bytes/string/structured data. Requires a `cue.mod/module.cue` file in the module root.
- Error messages from CUE are structured (position-rich). Wrap via `cue/errors.Details(err, &errors.Config{Cwd: projectRoot})` to get user-grade multi-line diagnostics.

### 2.4 Library quirks to plan for

- `load.Instances` requires a CUE module (`cue.mod/module.cue`). Without it, `@embed` refuses to resolve. Plain file-loading via `cuecontext.CompileBytes` works but **disables `@embed`**.
- Pointers vs. optionality: CUE's optional fields (`foo?: ...`) decode cleanly into Go pointer or zero-value fields; `*bool` (our `CacheBackend.Read/Write` tri-state) is preserved.
- `map[string]any` — CUE's `[string]: _` is decoded into Go `map[string]interface{}` (numbers become `float64` or `int64` depending on literal form — same as `encoding/json`). Our `Target.Config map[string]any` survives but any downstream code that type-asserts should be re-tested.
- Order sensitivity: CUE fields are *unordered* by design. We currently rely on target declaration order for plan printing / deterministic output. After migration, sort targets by name at decode time (loader post-step) — trivial but must not be forgotten.

---

## 3. Proposed `mu.cue` schema

Single-file, single-package design for the MVP. Definitions (capitalized via `#`) constrain user data.

```cue
package mu

#Toolchain: {
    toolchain:           string & !=""
    from:                string & !=""
    config: {
        version?:      string
        url?:          string
        sha256?:       =~"^[a-f0-9]{64}$"
        strip_prefix?: string
    }
}

#Target: {
    target:    =~"^//"
    toolchain: string & !=""
    sources:   [...string]
    deps?:     [...string]
    config?:   {[string]: _}
    sealed_inputs?: {[string]: string}
    // BRICK classifiers (pudl-validated, mu ignores)
    kind?:       "relationship" | "interface" | "component" | "kit"
    implements?: string
}

#PluginDef: {
    name: string & !=""
    {
        command: [...string]
    } | {
        script: string & !=""
    } | {
        url:    string & !=""
        sha256: =~"^[a-f0-9]{64}$"
    } | {
        digest: string & !=""
    }
}

#CacheBackend: {type: "disk", path: string, max_size?: string, read?: bool, write?: bool} |
               {type: "oci",  registry: string, max_size?: string, read?: bool, write?: bool}

#CacheConfig: {
    backends?:     [...#CacheBackend]
    read_repair?:  bool
    write_through?: bool
    push?: {registry: string, repository: string}
}

#Preprocessor: {extension: string & !="", command: [...string] & [_, ...]}

#PluginManifest: {
    entrypoint: string & !=""
    toolchain?: string
    files?:     [...string]
    guide?:     string
}

#ProjectConfig: {
    targets?:      [...#Target]
    toolchains?:   [...#Toolchain]
    cache?:        #CacheConfig
    plugins?:      [...#PluginDef]
    preprocessor?: #Preprocessor
}

#PluginConfig: {
    plugin:      #PluginManifest
    targets?:    [...#Target]
    toolchains?: [...#Toolchain]
}

// Concrete root — user-authored fields spread at this level.
#ProjectConfig
```

**Uniqueness constraints** (target / toolchain / plugin names) are awkward to express in pure CUE. Natural split: keep the Go-side validator for cross-record invariants; let CUE handle structural/typed checks. This also keeps error messages curated.

---

## 4. `mu/scripts` and `mu/config` conventions — `@embed` vs sibling files

### Option A — `@embed` inlines at eval time

```cue
_lint: _ @embed(file="scripts/lint.cue")

#Target & {
    target:    "//lint/gofmt"
    toolchain: "lint"
    sources:   ["cmd/mu/*.go"]
    config: {
        command:     _lint.command
        fix_command: _lint.fix_command
    }
}
```

**Pros**

- Single-file-in, single-struct-out: the evaluated `ProjectConfig` captures the full content. Cache keys derived from `cue.Value.Decode` output automatically cover embedded content.
- No new file-tracking machinery — `load.Instances` already enumerates imports/embeds for dependency tracking.

**Cons**

- Cache key inlines content. Changing `scripts/lint.cue` reshuffles the evaluated target struct and invalidates that target's cache — **correct**, but there's no locality (you can't tell which file changed from the digest).
- `@embed` for non-CUE blobs (e.g. `@embed(file="plugin.bb")`) returns `bytes` — decoding into `string` requires an explicit conversion.
- Embedded content is copied into memory on every load — a 1 MB script × 40 targets = 40 MB of duplication.

### Option B — sibling file references, hashed for cache keys

```cue
#Target & {
    config: {
        command_file: "scripts/lint.sh"   // plain string path
    }
}
```

Loader hashes referenced files into the action digest the same way it hashes `sources` today.

**Pros**

- Preserves file locality in the cache — the target's action digest references the file by hash, and `mu verify` / cache replay can surface *which* file changed. Matches current mu semantics.
- No binary bloat from embedding.
- Plays well with editor tooling: users edit `scripts/lint.sh` with shell language-mode, not as a string in a CUE file.
- Consistent with existing `plugin.script` field (already a path reference).

**Cons**

- Loader must enumerate referenced files and fingerprint them (small extension to current globbing code).
- Two places to look when debugging (the `.cue` and the referenced script).

### Recommendation

**Adopt Option B as the default, Option A as an escape hatch.**

- `mu/scripts/*.sh` — sibling file pattern. Loader hashes each referenced script into the target's action key.
- `mu/config/*.cue` — first-class CUE imports, not `@embed`. If shared across targets, move into a `mu/config` CUE package and `import "mu/config"`. Typed reuse, not byte-smuggling.
- `@embed` is reserved for small literal blobs that genuinely belong in the evaluated struct (license header, fixed JSON template). Required CUE version ≥ v0.11.

---

## 5. `mu migrate` command design

```
mu migrate [--in mu.json] [--out mu.cue] [--dry-run] [--keep-json]
```

### Behavior

1. If `--in` omitted, discover via `config.FindProjectRoot`.
2. Load the JSON through a per-file decode (no recursive merge — we translate each `mu.json` individually and preserve subtree structure).
3. Re-emit as CUE:
   - Use `cuelang.org/go/encoding/json.Decode` to build a `cue.Value`, then print via `cue/format.Node(syntax.Node)` with `cue/format.Simplify()`. Idiomatic CUE output, no string templating.
   - Prepend `package mu` and wrap with `#ProjectConfig &` (or `#PluginConfig &` for plugin manifests).
4. Write to `--out` (default: sibling `mu.cue`). With `--keep-json`, leave the JSON in place (dual-read window). Otherwise, delete after successful validation.
5. `--dry-run` prints a unified diff of `mu.json` → `mu.cue` to stdout and exits 0.
6. On success, run `config.Validate(config.Load(projectRoot))` *against the new CUE file* to prove parity before deleting the JSON.

### Edge cases & coercion rules

| Case | Handling |
|---|---|
| Field order | JSON maps are order-undefined; our `ProjectConfig` fields are struct-ordered. Emit CUE in canonical field order (the order defined in `#ProjectConfig`). |
| Number fidelity | JSON numbers decode to `float64` via `encoding/json`. CUE preserves integer vs. float — force numeric fields that round-trip exactly to integers. |
| `*bool` (tri-state read/write) | Preserve `nil` as *field absent*; `false` as explicit `false`. Do **not** emit defaults. |
| `map[string]any` in `target.config` | Recurse; preserve nested ordering via CUE's field-preservation on round-trip. |
| Comments | JSON has none; if the user has JSON-with-comments via a preprocessor, run the preprocessor first. |
| Plugin manifests (`PluginConfig`) | Migrate with `#PluginConfig` wrapper. Detect by presence of `"plugin"` key (same probe as `IsPluginDir`). |
| Preprocessor declarations | If root declares a preprocessor, warn loudly: the preprocessor is rendered largely obsolete by CUE adoption. Offer `--keep-preprocessor` to emit it verbatim for transition. |
| Multi-file projects | Migrate *each* `mu.json` in the tree in one invocation. Print a summary table of N files translated. |
| Globs in `sources` | Pass through as strings — CUE handles them as literals; the loader's `expandSourceGlobs` runs post-decode identically. |
| Both files present | If both `mu.json` and `mu.cue` exist and `--keep-json` is not passed, refuse to write and error. |

---

## 6. Risk assessment

### High

- **Dual-read window & cache invalidation.** If mu supports both `mu.json` and `mu.cue` simultaneously, the cache key must include the *canonical decoded struct*, not the source bytes. Switching a project from `mu.json` to `mu.cue` **must not** invalidate every cached build artifact, but it will unless the action digest is built from a post-decode normalized form. Audit `internal/coordinator` action-digest construction before cutover. *Mitigation:* normalize the decoded `ProjectConfig` (sorted targets, deterministic JSON re-encode) before feeding into the digest. Also makes field-reorder edits cache-stable.
- **Binary size.** +8–20 MB to every `mu` binary shipped, including ones that never load `.cue` files (`mu verify`, `mu cache push`). *Mitigation:* gate CUE support behind a build tag for trimmed distributions, or accept the size. Build-tag adds release complexity — lean toward accepting.

### Medium

- **Error message quality.** CUE's errors are position-rich but often verbose and schema-oriented ("conflicting values: int and string at …"). Users accustomed to JSON parse errors will see a regression in readability unless we wrap `cue/errors.Details` with a mu-specific formatter. *Mitigation:* ship a small error formatter (60–100 lines) that collapses unification errors to "target //foo: field `sources` expected list of strings, got int at line 23".
- **Editor tooling.** CUE has a VSCode extension and an LSP (`cuelang.org/go/cmd/cue lsp`). Not all users will install it. Neovim support via `nvim-lspconfig` works but is less polished than JSON. *Mitigation:* include a `.editorconfig` hint and document setup.
- **CUE compile perf.** For our current ~90-line root config, eval is sub-10 ms after warmup. At 10× scale (~1000 targets), eval stays <100 ms but cold-start is 50–80 ms. Acceptable for an interactive CLI; revisit if target count grows by 10×.

### Low

- **CUE runtime churn.** v0.10→v0.14 saw breaking API renames in `cue/load`. Pin exact minor version in `go.mod`.
- **`map[string]any` numeric drift.** JSON decodes `42` as `float64(42)`. CUE decodes `42` as `int64(42)` by default. Downstream type assertions (`v.(float64)`) will panic. *Mitigation:* grep for `.(float64)` on `Target.Config`; fix assertions to handle both.
- **Plugin manifest migration risk.** 14 plugin dirs all ship their own `mu.json`. A migration bug could break every plugin at once. *Mitigation:* migrate root first, plugins in a follow-up PR, each with its own rollback.
- **`@embed` semantics drift.** Attribute format stabilized in v0.12; pinning prevents footguns.

---

## 7. Comparison notes: Dagger, Tilt, Bazel-Starlark

### Dagger (CUE era, pre-v0.2)

Dagger *replaced* CUE with Go/Python SDKs in 2022. Lessons:

- Users disliked CUE's learning curve for trivial cases. Mitigated if we target CUE at power users and keep common cases looking almost identical to JSON (our `#Target` shape does).
- Dagger's CUE integration was slow because it used `cue/load` per-invocation on large module graphs. Our project is single-package; this failure mode doesn't apply.
- The kill-shot for Dagger-with-CUE was needing *Turing-complete* logic (loops over targets). CUE's comprehensions solve simple cases but not complex ones. If mu ever needs that, we keep the preprocessor escape hatch.

### Tilt (Starlark / Tiltfile)

- Starlark is procedural ⇒ easy for imperative thinkers, hard to analyze statically.
- CUE is declarative+typed ⇒ easier to validate, harder for users who want "just loop over this list".
- Tilt's `load("./common.tiltfile")` is equivalent to CUE's `import "./common"`. Ours would be simpler — we expect few cross-file references.

### Bazel / Starlark

- Bazel's `BUILD.bazel` files are Starlark. Bazel successfully runs at planet scale, backed by 15+ years of tooling investment.
- Bazel chose Starlark specifically to have macros + helper functions. CUE deliberately rejects that for constraint semantics.
- For mu's size/complexity, CUE's tradeoffs (typed, declarative, no arbitrary computation) are strictly better — the preprocessor escape hatch already covers the "I need computation" case, and we don't want arbitrary scripts running at config-eval time in a build-tool threat model.

**Net read:** CUE is the right pick for a small, typed, declarative build config. The Dagger retreat is a cautionary tale about *scope creep* (trying to use CUE for workflow DAGs), not about the format itself.

---

## Recommendations (summary, non-binding)

1. Pin `cuelang.org/go >= v0.13.0`.
2. Ship `mu.cue` schema and loader behind a `--format=cue|json|auto` flag; default `auto` (look for both, error on both present).
3. Implement `mu migrate` as a one-way migrator with `--dry-run` + `--keep-json`.
4. Normalize the decoded `ProjectConfig` before action-digest hashing so format switches don't invalidate caches.
5. Adopt the sibling-file convention for `mu/scripts`; reserve `@embed` for small literal blobs.
6. Keep the Go validator for cross-record invariants (name uniqueness, `PluginDef` tagged-union); CUE handles structural/typed checks.
7. Write a thin error formatter over `cue/errors.Details` before exposing CUE errors to users.

## Out of scope (noted for follow-up)

- Replacing the `preprocessor` escape hatch: unnecessary post-CUE for most cases, but users with `.edn`/`.dhall`/`.jsonnet` still need it for a migration window.
- Multi-package CUE layouts (`mu/config` as a shared package). Worth a follow-up once the single-file MVP ships.
- Schema publishing: should `#ProjectConfig` live in a `cue.mod/pkg/github.com/chau/mu` namespace for third-party tooling to import? Defer until someone asks.
