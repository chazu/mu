# CUE Conventions for `mu` Build Configs

This document defines the authoring conventions for `mu.cue` project
configurations. It complements the migration scout report at
[`scout-cue-migration.md`](scout-cue-migration.md) and is the canonical reference
for plugin and project authors writing CUE today.

---

## 1. Minimum tooling

- **CUE language version: `>= v0.11.0`** (required for stable `@embed`).
- **Recommended: `>= v0.13.0`** for bug fixes around `@embed` glob patterns and
  sibling-module resolution.
- **Go toolchain: `>= 1.22`** (the `mu` module itself is on Go 1.25).
- Pin the exact version in `go.mod` via `cuelang.org/go`.

If you are using `CUE_EXPERIMENT=embed=1`, upgrade — `@embed` is default-on from
v0.11 and stable from v0.12.

---

## 2. Directory layout

Every project rooted at a `mu.cue` (or plugin directory containing a
`plugin.bb` + `mu.cue`) follows the layout below. Paths are relative to the
project root.

```
<project-root>/
├── mu.cue                  # the project config, package mu
├── cue.mod/
│   └── module.cue          # REQUIRED — see §3
├── mu/
│   ├── scripts/            # shell/tool scripts referenced by targets
│   │   ├── lint.sh
│   │   └── build.sh
│   └── config/             # shared CUE packages (imported, not embedded)
│       ├── toolchains.cue
│       └── defaults.cue
└── ... (the rest of the project)
```

### `mu/scripts/`

Sibling scripts referenced by path from `mu.cue`. The loader hashes each
referenced script into the containing target's action key, so edits to a script
invalidate only the targets that reference it (preserving cache locality).

Prefer this layout for anything that is fundamentally a script — shell, python,
awk, jq, etc. Users edit these files in their native language mode, not as
strings inside a CUE file.

### `mu/config/`

Shared CUE fragments that multiple targets or plugins need. Promote them to a
CUE package and `import "mu/config"` — do **not** `@embed` CUE files. This
preserves type checking, schema unification, and readable error locations.

---

## 3. `cue.mod/module.cue` expectation

Every project that uses `@embed` or cross-file imports **must** have a
`cue.mod/module.cue` file at the project root. Without it, `load.Instances`
refuses to resolve `@embed` and imports.

Minimum contents:

```cue
module: "example.com/myproject"
language: version: "v0.11.0"
```

- `module:` is a stable module path used for internal imports. It does **not**
  have to resolve on the public internet; it is a naming key.
- `language.version:` pins the CUE syntax version used for evaluation.
  Use `>= v0.11.0`.

The `mu migrate` command creates `cue.mod/module.cue` automatically if absent.

---

## 4. `@embed` vs sibling file reference

**Rule of thumb: embed if the content is under 1 KB and fundamentally part of
the config; reference as a sibling file otherwise.**

### Use `@embed` when

- The content is **< 1 KB** and has no independent editor ergonomics (e.g. a
  short license header, a fixed JSON fragment, a tiny template).
- The content is literally part of the evaluated `ProjectConfig` struct — it
  *is* configuration, not an executable artifact.
- You accept that changes invalidate the entire containing target's cache.

```cue
import "example.com/myproject/mu/config"

#Target & {
    target:    "//release/notice"
    toolchain: "lint"
    config: {
        // Tiny literal — fine to embed.
        header: _ @embed(file="scripts/license-header.txt")
    }
}
```

`@embed` on non-CUE blobs returns `bytes`. Convert explicitly with
`string(_header)` if a string is required downstream.

### Use a sibling file reference when

- The content is **≥ 1 KB**, or is an executable script, or has its own editor
  mode (shell, python, dockerfile, etc.).
- You want cache-digest locality — a change to one script should not rewrite the
  entire evaluated config digest.
- The content is shared across targets (reference the same path from each).

```cue
#Target & {
    target:    "//lint/gofmt"
    toolchain: "lint"
    sources:   ["cmd/mu/*.go"]
    config: {
        command_file: "mu/scripts/lint.sh"   // hashed into action key by loader
    }
}
```

The `mu` loader enumerates referenced files and fingerprints them the same way
it hashes `sources`. `mu verify` and cache-replay surface *which* sibling file
changed.

### Why the 1 KB heuristic

- Below ~1 KB, embed cost (memory duplication × target count) is negligible and
  the convenience of a single-file-in/single-struct-out loader wins.
- Above ~1 KB, duplication adds up fast: a 50 KB script × 40 targets is 2 MB of
  duplicated evaluated struct, and every edit re-digests every target.
- The threshold is advisory, not enforced. Reviewer judgement applies.

---

## 5. Summary (for reviewers)

When reviewing a new `mu.cue`, check:

1. `cue.mod/module.cue` exists and pins `language.version >= v0.11.0`.
2. Scripts live under `mu/scripts/` and are referenced as sibling paths.
3. Shared CUE fragments live under `mu/config/` and are imported (not embedded).
4. `@embed` is used only for tiny literal blobs (< 1 KB, no separate editor
   ergonomics).
5. The file passes `cue vet` against the project schema and `mu validate`.

---

## See also

- [`scout-cue-migration.md`](scout-cue-migration.md) — full design rationale and
  migration plan.
- [CUE `@embed` reference](https://cuelang.org/docs/reference/embed/) —
  upstream documentation.
