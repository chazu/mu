# Epic: Real JSON-Schema Validation for Target Configs

**Status:** Draft
**Date:** 2026-04-19
**Leverage:** 5 — the single biggest plugin-UX improvement available.
**Type:** Feature (DX / correctness)

---

## Summary

Today, mu validates a target's `config:` block against a plugin's declared
`config_schema` using a hand-rolled ~120-line matcher in
`internal/coordinator/validate.go:ValidateTargetConfig`. That matcher walks
the schema as a flat `map[string]any` and supports only `type`, a custom
non-standard `required: true` boolean-at-field-level flag, and `enum`. It
does not support:

- JSON Schema's real `required` (array at the object level)
- `pattern`, `minLength`, `maxLength`, `minimum`, `maximum`, `format`
- Nested object shapes (`properties`, `additionalProperties`)
- Array `items` validation
- `oneOf`/`anyOf`/`allOf`
- `default` values being *applied* (plugin-side only today)
- Draft-7 / Draft 2020-12 dialects
- Useful error messages (single `fmt.Errorf` joined by `;`)

Practically this means a config like `goos: "linx"` passes validation and
dies deep inside `go build` with a cryptic error referencing `GOOS`. We
want the failure to surface *at config load time* with a message like:

```
mu.json: target //cmd/server: config.goos: "linx" is not one of
[linux, darwin, windows, freebsd, ...] — did you mean "linux"?
```

This epic replaces the hand-rolled matcher with a real JSON-Schema
validator, compiles each plugin's `config_schema` once at plugin
discovery, and runs the validator both at load time (fast-fail) and
before `mgr.Plan` (defense-in-depth). Schema `default` values flow into
`Target.Config` before plan. Plugin authors keep writing schemas exactly
the way they already do — the existing fixtures continue to work under
Draft-7.

---

## User Stories

1. **As a mu end user**, I want typos and type errors in `config:` blocks
   to fail at load time with a precise path and a "did you mean" hint,
   so that I don't waste a 30-second `go build` round-trip to discover
   them.

2. **As a plugin author**, I want the full expressive power of JSON
   Schema (`pattern`, `enum`, nested `properties`, `oneOf`, `items`,
   `minimum`/`maximum`) so I can declare richer contracts without
   re-implementing validation in Clojure inside `validate-config`.

3. **As a plugin author**, I want `default` values in my schema to be
   applied automatically to `Target.Config` before `plan` is called, so
   I don't write the same `(get config "x" "default")` expression in
   five places per plugin.

4. **As a mu contributor**, I want `ValidateTargetConfig` backed by a
   battle-tested library so I don't own 120 lines of subtly-wrong
   type-coercion logic.

5. **As a CI operator**, I want every bundled plugin's `config_schema`
   to be meta-validated at test time — so a plugin that ships an
   invalid schema breaks the build, not user configs.

6. **As a user migrating from the old matcher**, I want my existing
   `mu.json` files to continue working unchanged; any schema-level
   breakage should surface from the plugin's `config_schema`, not from
   my config.

---

## Acceptance Criteria

### Validation behavior
- [ ] `ValidateTargetConfig` uses a real JSON-Schema library (see
  Library Choice below) — the hand-rolled `checkType`/`enumContains` are
  deleted.
- [ ] Validation runs **at config load time** (new call site in the
  loader, once plugins are discovered) **and** before `mgr.Plan` (kept
  for defense-in-depth; identical result expected).
- [ ] All existing tests in `internal/coordinator/validate_test.go`
  continue to pass, updated where the error *wording* changes but
  preserving the *semantics*.
- [ ] New tests cover: `pattern`, object `properties` with nested
  `required`, array `items`, `oneOf`, `minimum`/`maximum`,
  `additionalProperties: false`.

### Compilation + caching
- [ ] Each plugin's `config_schema` is compiled into a
  `*jsonschema.Schema` exactly once, at plugin discovery time, and
  cached on the coordinator / plugin manager (keyed by toolchain name).
- [ ] A plugin that ships an unparseable schema fails discovery with a
  clear error naming the plugin (`plugin "docker": invalid
  config_schema: ...`) — it does not silently degrade to "no
  validation".

### Error formatting
- [ ] Error messages include, at minimum: target name, JSON-pointer
  path into the config (e.g. `config.build_args.FOO`), the offending
  value, the expected constraint.
- [ ] For `enum` violations, the error includes a "did you mean"
  suggestion computed via Levenshtein distance against the enum values
  (threshold ≤ 2 edits, or ≤ 33% of the shorter length).
- [ ] Multiple errors are reported together (not just the first), one
  per line, sorted by path.
- [ ] Unknown fields are still a warning (not an error) **unless** the
  plugin's schema declares `additionalProperties: false`.

### Defaults application
- [ ] A new `ApplyDefaults(schema, cfg)` step runs after successful
  validation and mutates (or returns a new) `Target.Config` so that
  schema `default` values are materialized before the `plan` request
  is sent.
- [ ] Defaults are applied at every level of nesting supported by the
  library (top-level only is acceptable for v1 if documented).
- [ ] Plugin-side defaults (`(get config "x" "default")` idioms) remain
  a valid redundant fallback — removing them is a separate cleanup
  epic.

### Migration & compatibility
- [ ] The existing non-standard `required: true` boolean-at-field level
  is supported in a compatibility shim: the loader rewrites it into
  canonical `required: [...]` at the object level before handing the
  schema to the library. This shim is logged at DEBUG level.
- [ ] No bundled plugin (`plugins/*/plugin.bb`) needs source changes to
  pass validation under the new validator; any that do are listed in
  the plan's "migration steps" section and updated in-epic.
- [ ] Docs (`docs/plugin-development.md` or equivalent) are updated to
  document Draft-7 as the canonical dialect and recommend standard
  `required: [...]`.

### Tests
- [ ] A `TestAllBundledPluginSchemas` test in
  `internal/coordinator/validate_test.go` iterates `plugins/*/plugin.bb`,
  invokes each via the real plugin manager in discover-only mode, and
  asserts each returned `config_schema` compiles cleanly.
- [ ] Each bundled plugin gets at least one golden "valid config" and
  one "invalid config" fixture in `internal/coordinator/testdata/`.
- [ ] Fuzz / property test: random well-typed configs against each
  bundled schema must never return an error; random *mistyped* configs
  must always return one.

### Non-functional
- [ ] Added dependency weight < 2 MB of Go deps (should be comfortably
  under).
- [ ] Validation latency for a 100-target `mu.json` remains under
  50 ms on a dev laptop (schemas are compiled once, re-used per
  target).

---

## Design

### 1. Library Choice

**Recommended: `github.com/santhosh-tekuri/jsonschema/v5`**

Evaluation:

| Library | Pros | Cons |
|---|---|---|
| `santhosh-tekuri/jsonschema/v5` | Full Draft 4/6/7/2019-09/2020-12 support. Zero non-stdlib runtime deps. Exposes a detailed `*ValidationError` tree with `InstanceLocation` (JSON pointer) and `KeywordLocation` — perfect for our error formatter. Permits loading schemas directly from `map[string]any` via `Compiler.AddResource`. Actively maintained. | API surface slightly larger than needed. |
| `xeipuuv/gojsonschema` | Popular, stable. | Draft 4/6/7 only (no 2020-12). Less structured errors — we'd hand-format. Heavier dep graph. |
| `qri-io/jsonschema` | Good 2019-09 support. | Semi-abandoned (last meaningful release 2022). |
| `kaptinlin/jsonschema` | Newer, 2020-12-focused. | Immature error surface; smaller community. |
| hand-rolled extension | Zero deps. | We're explicitly moving *away* from this. |

Pick `santhosh-tekuri/jsonschema/v5`. It has the best
error-introspection for building our "path, expected, got, suggestion"
formatter, and its zero-runtime-dep profile matches mu's minimalism.

### 2. Compilation Lifecycle

```
plugin discover  ──►  DiscoverResponse.ConfigSchema (map[string]any)
                       │
                       ▼
              compiler.AddResource(pluginID, schemaJSON)
                       │
                       ▼
              compiler.Compile(pluginID)  ──►  *jsonschema.Schema
                       │
                       ▼
              cached on PluginManager keyed by toolchain name
```

Location: `internal/plugin/manager.go` (or similar) gains a
`compiledSchemas map[string]*jsonschema.Schema` field populated in
`Start` / `Discover` after the `DiscoverResponse` returns. The
coordinator reaches it through a new
`mgr.CompiledConfigSchema(toolchain string) *jsonschema.Schema`.

### 3. Validation Call Sites

1. **Load time** (`internal/config/loader.go`, new): after plugins are
   discovered — before the coordinator begins graph resolution — loop
   all targets, validate each against its plugin's compiled schema,
   collect *all* errors, return them together. This is the new
   fast-fail path.
2. **Pre-plan** (`internal/coordinator/coordinator.go:114-124`, kept):
   same validator, same schemas, defense-in-depth in case a programmatic
   caller bypasses the loader.
3. **Meta-validation** (test-only): at startup in dev mode, every
   plugin's schema is validated against the JSON-Schema meta-schema so a
   broken plugin schema fails fast at test time.

### 4. Error Format

```go
type ConfigError struct {
    Target   string
    Path     string // JSON pointer: "/goos" or "/build_args/FOO"
    Value    any
    Expected string // human-readable constraint
    Hint     string // optional "did you mean X"
}
```

Rendered as:

```
mu.json: target //cmd/server: config.goos: value "linx" is not one of
  ["linux", "darwin", "windows", "freebsd", "openbsd", "netbsd"]
  — did you mean "linux"?
```

Suggestion algorithm: for `enum` violations when the value is a
string, compute Levenshtein distance against each enum value, return
the single closest within threshold (≤2 edits or ≤⅓ of shorter
length). No suggestion if none qualify. A small utility already
plausibly fits in `internal/coordinator/suggest.go`; otherwise write
it (≈30 lines).

### 5. Migration

- `config_schema` stays a `map[string]any` on the wire — plugins keep
  emitting their schemas as EDN → JSON maps, nothing changes plugin-side.
- Declare Draft-7 as canonical (the library auto-detects via `$schema`
  if present, else we inject `"$schema": "http://json-schema.org/draft-07/schema#"`).
- A compatibility shim in the coordinator rewrites the legacy
  per-field `required: true` flag into canonical object-level
  `required: [...]` *before* passing to the compiler. This keeps every
  bundled plugin's current schema working verbatim. Shim behavior is
  covered by a dedicated test and is DEBUG-logged so plugin authors can
  migrate at leisure.
- Bundled plugins to audit: `aws`, `docker`, `file`, `go`, `host`,
  `k8s`, `lint`, `terraform`, `zig`. (`cowsay`, `pass`, `scratch` have
  no `config_schema` — confirmed via grep.)

### 6. Defaults Application

After validation succeeds, walk the compiled schema's resolved
`properties` tree and, for every property that has a `default` and
whose instance value is `absent` in `target.Config`, insert the
default. Two implementation options:

- (a) Use the library's own default-walker if available; otherwise
- (b) Walk `schema.Properties` ourselves — this is straightforward for
  our shapes (one level of objects, plus `items` for arrays).

v1 ships (b) handling the top-level-properties case, which covers every
bundled plugin today. Nested-default support is a documented follow-up.

### 7. Testing

- Unit tests: a table-driven suite per schema construct (`type`, `enum`,
  `pattern`, `required`, nested `properties`, `items`, `oneOf`,
  `additionalProperties`), targeting `ValidateTargetConfig` directly.
- Integration: `TestAllBundledPluginSchemas` boots each plugin via the
  real plugin manager's `Discover`, asserts the schema compiles, and
  runs a valid-fixture + invalid-fixture case per plugin.
- Golden error-format test: one snapshot per representative error
  (enum-with-suggestion, missing-required, nested-path, type-mismatch)
  to pin the UX.
- Performance: micro-benchmark `BenchmarkValidate_100Targets` to lock
  in the <50 ms budget.

---

## Technical Context (files & patterns)

| File | Role |
|---|---|
| `internal/coordinator/validate.go` | **Replaced.** Current hand-rolled matcher. |
| `internal/coordinator/validate_test.go` | Existing tests; update assertions, add new cases. |
| `internal/coordinator/coordinator.go:113-124` | Existing pre-plan call site; keeps working. |
| `internal/plugin/protocol.go:20-28` | `DiscoverResponse.ConfigSchema map[string]any` — unchanged. |
| `internal/plugin/manager.go` | **New field + method** for compiled-schema cache. |
| `internal/config/loader.go` | **New call site** for load-time validation (after discover). |
| `internal/config/types.go:18-23` | `Target.Config map[string]any` — where defaults land. |
| `plugins/go/plugin.bb:31-41` | Representative schema (uses legacy `type`-only form). |
| `plugins/docker/plugin.bb:28-36` | Uses `default`, good test case for defaults application. |
| `plugins/aws/plugin.bb:181-183` | Minimal schema; tests the "no required" path. |
| `plugins/lint/plugin.bb:34` | Uses `items: {type: string}` — tests array-item validation. |
| `go.mod` | New dep: `github.com/santhosh-tekuri/jsonschema/v5`. |

Patterns in this codebase to respect:
- Errors flow up with context via `fmt.Errorf("coordinator: %w", err)`.
- NDJSON plugin protocol is untouched — this is a coordinator-internal
  refactor. `ConfigSchema` stays `map[string]any` on the wire.
- `internal/coordinator/` owns build-graph concerns; new types like
  `ConfigError` belong here, not in `internal/config/`.

---

## Open Questions

1. **Strict mode for unknown fields?** Today unknown fields are a
   warning. Do we want a `strict: true` per-plugin flag, or should
   `additionalProperties: false` in the schema be the sole dial? (Lean:
   schema-driven.)

2. **Where does the defaults-materialized config live?** Mutate
   `Target.Config` in place (simpler, but `config.Target` is shared
   across the codebase) or attach a separate `ResolvedConfig` field
   (cleaner, more refactor surface)? (Lean: mutate in place for v1,
   refactor later if needed.)

3. **Do we fingerprint `config_schema` in the build cache key?** If a
   plugin ships a stricter schema in v0.2, previously-valid configs
   might now be invalid — do we want that to bust caches? (Lean: no —
   schema changes are a validation concern, not an artifact-identity
   concern.)

4. **`$ref` + remote schemas?** Plugins might want to share common
   sub-schemas (e.g. a `k8s_resource` type). Do we support `$ref` in
   v1, or forbid it for hermeticity? (Lean: allow local `$ref` only,
   forbid remote `$ref` — enforced by custom loader.)

5. **Should shell-toolchain targets get a schema too?** Currently
   `coordinator.go:115` skips shell targets entirely. The built-in
   shell handler has implicit expectations (`script`, `args`, `env`);
   worth declaring them. (Lean: yes, but out-of-scope for this epic —
   file a follow-up.)

6. **Meta-validation on startup vs. test-only?** Running meta-validation
   at every `mu build` adds ~milliseconds of startup; test-only keeps
   prod fast. (Lean: test-only for v1; add a `mu plugins check` command
   for on-demand meta-validation.)
