# Plugin Output Schemas — Implementation Plan

Date: 2026-05-04
Status: draft
Source brainstorm: `docs/brainstorms/2026-05-04-plugin-output-schemas.md`

## Goal

Let mu plugins optionally declare a CUE schema for the data they produce,
so pudl can classify imports without falling back to `pudl/core.#Item`.
Resolution supports vendored and remote modules; namespaces follow the
three-tier provenance convention (`pudl/`, `mu/<plugin>`, third-party).

## Scope

In:
- mu plugin protocol additions (declaring a schema reference)
- mu plugin packaging (vendoring CUE modules in plugin bundles)
- Schema cache infra (content-addressed, separate from plugin/OCI cache)
- pudl ingest path: read schema reference, register schemas, classify
  with the decision tree from the brainstorm
- `mu verify` light namespace policing for `mu/<plugin>` packages

Out (deferred):
- Plugin signing / strong identity for namespace enforcement
- Remote CUE registry implementation (use existing OCI infra; defer
  dedicated registry)
- Migration of existing imports already classified as `#Item`

## Non-goals

- Replacing pudl's inference path. Inference stays as a fallback tier
  (see brainstorm "Import-time behavior").
- Breaking the existing plugin protocol. The schema reference is purely
  additive and optional.

## Architecture sketch

```
plugin discover  ──►  output_schema { module, version, definition? }
                           │
                           ▼
plugin observe   ──►  Current data + (optional per-response override)
                           │
                           ▼ (mu hands the data + ref to pudl ingest)
pudl ingest
   │
   ├── ref known in schema cache?         ─►  classify
   ├── ref unknown, def travels w/ data?  ─►  auto-register, classify
   ├── unknown, no def → pudl inference   ─►  classify (tag w/ ref)
   └── inference yields nothing           ─►  pudl/core.#Item (tag w/ ref)
```

Schema cache key: `(module, version)`, content-addressed. Lives at
`~/.mu/schemas/` (or analogous in pudl), separate from `~/.mu/cache/`.

## Workstreams

### W1: Protocol additions (mu)

**Files:** `internal/plugin/protocol.go`, plugin examples (`*.bb`,
`plugins/*/plugin.bb`)

Add to `DiscoverResponse`:

```go
OutputSchema *SchemaRef `json:"output_schema,omitempty"`
```

Where:

```go
type SchemaRef struct {
    Module     string `json:"module"`              // e.g. "mu/aws"
    Version    string `json:"version"`             // e.g. "v1"
    Definition string `json:"definition,omitempty"`// e.g. "#EC2Instance"
    Source     string `json:"source,omitempty"`    // "vendored" | "remote"; advisory
}
```

**Decision (locked):** discover-only at v1. No per-observe override.
Adding it later is additive if a real plugin demands it.

**Tests:** protocol round-trip JSON; HasCapability unaffected.

### W2: Plugin bundle packaging (mu)

**Files:** `internal/coordinator/manifest.go`, OCI push path,
`internal/plugin/loader.go` (or wherever plugin files are enumerated).

**Decision (locked):** mirrored layout — the on-disk path under
`schemas/` *is* the CUE module path.

- Plugin authors place CUE under `schemas/<module-path>/`, e.g.
  `plugins/aws/schemas/mu/aws/ec2.cue` for module `mu/aws`.
- Plugin bundle (the OCI artifact) includes the `schemas/` tree as part
  of its layer; no special media type required (ORAS is agnostic to
  layer internal structure).
- `mu.cue` declares which subtrees are schema modules:
  ```
  plugin: { ..., schemas: ["schemas/mu/aws"] }
  ```
- mu CLI exposes a way to list/extract a plugin's vendored schemas
  (used by pudl ingest and by `mu verify`).

**Tests:** golden bundle layout; round-trip pack/unpack preserves schema
files; coordinator can locate vendored schemas given a plugin name.

### W3: Schema cache (shared lib)

**Files:** new package, e.g. `internal/schemacache/` in mu (or a
location pudl can also import — possibly extracted later).

- Storage: `<root>/schemas/<module>/<version>/...cue`
- Lookup by `(module, version)` returns either local files or "miss."
- Insert is content-addressed: identical CUE content for same
  `(module, version)` is idempotent; mismatched content is an error
  (versions are immutable once cached).
- Append-only: never evict on version bump; older versions stay for
  reclassification of historical imports.

**Tests:** insert idempotent; mismatched insert errors; concurrent
inserts safe (file-level locking or atomic rename).

### W4: pudl ingest integration

**Files (pudl):** `internal/importer/importer.go`,
`internal/importer/enhanced_importer.go`, `internal/importer/detection.go`,
plus a new catalog migration for the `item_schemas` junction table.

**Decision (locked) — transport:** envelope. A single JSON document
with shape `{"schema": {...}, "definitions": [...], "data": <payload>}`.
pudl detects envelope by shape (top-level object with both `schema`
populated and `data` present); raw JSON without that shape is
imported untouched. For agentic / ad-hoc use, `pudl import` also
accepts `--schema mu/aws@v1[#Definition]` as an explicit override on
raw JSON; same downstream code path, different ref source.

(Originally sidecar; reverted 2026-05-05 because mu's natural producer
is a stream/stdout path and writing a sibling file from `mu observe`
required filesystem coordination that didn't fit. Envelope is one
self-contained artifact, easier to pipe and store.)

**Decision (locked) — catalog:** dedicated junction table
`item_schemas`, not a column on the items table. Supports an item
matching multiple schemas, naturally encodes the unresolved-ref tag as
a row with `status='unresolved'`, and aligns with pudl's existing
fact-store shape. Migrating from a single column to this table later
would be a one-way door once data accumulates; starting here avoids it.

```sql
CREATE TABLE item_schemas (
    item_id        TEXT NOT NULL,
    schema_ref     TEXT NOT NULL,    -- "mu/aws@v1#EC2Instance"
    status         TEXT NOT NULL,    -- 'declared' | 'auto_registered'
                                     -- | 'inferred' | 'unresolved'
    classified_at  TIMESTAMP NOT NULL,
    PRIMARY KEY (item_id, schema_ref)
);
CREATE INDEX item_schemas_status_idx ON item_schemas(status);
CREATE INDEX item_schemas_ref_idx    ON item_schemas(schema_ref);
```

- Resolution order in importer:
  1. Look up ref in schema cache → insert `item_schemas` row with
     `status='declared'`.
  2. If unknown and the import carries a vendored definition, register
     into the cache, then row with `status='auto_registered'`.
  3. Else, run existing inference. On match: row with
     `status='inferred'` *and* a second row with `status='unresolved'`
     carrying the original declared ref so `pudl reclassify` can
     upgrade it later.
  4. Else, catchall (`pudl/core.#Item`) with `status='inferred'`,
     plus `status='unresolved'` row for the declared ref.

**Tests:** four-way decision tree covered. Multi-schema item gets
multiple rows. Envelope detection (shape-based), unwrap round-trip,
raw-JSON pass-through, `--schema` flag works on both raw and envelope
inputs.

### W5: `pudl reclassify` (or extend existing)

**Files (pudl):** likely new `cmd/pudl/reclassify.go` if it doesn't
exist; otherwise extend.

- Find rows in `item_schemas` where `status='unresolved'`.
- On `pudl reclassify` (no args): for each such row, look up the ref in
  the schema cache; if now resolvable, attempt classification. On
  success, insert a new row with `status='declared'` and either delete
  or mark-superseded the unresolved row (TBD during impl — leaning
  delete for now, since `classified_at` already gives temporal context).
- `--ref <module@version>`: target one schema only.

**Tests:** import → unresolved row → register schema → reclassify →
declared row appears.

### W6: `mu verify` namespace policing

**Files:** wherever `mu verify` lives (or add it if pending).

**Decision (locked):** identity = plugin's `name` field from
`DiscoverResponse`. Good enough until signing exists; when signing
lands, the *check* swaps to a stronger source of truth but the *rule*
("the segment after `mu/` matches the plugin's identity") is unchanged.

- For each plugin declaring an `output_schema`:
  - If module starts with `mu/`, the segment after `mu/` should match
    the plugin's name.
  - Mismatch → warn (not error) initially. Document the rule.
- Out of scope: signed identity check.

**Tests:** matching name passes silently; mismatch warns with a clear
message; non-`mu/` namespaces are not policed.

### W7: Docs

- Update `docs/cue-conventions.md` with the three-tier convention.
- Update `pudl/docs/mu-integration.md` to describe the import-direction
  flow (currently only covers pudl→mu drift).
- Author a short "Authoring plugin output schemas" guide for plugin
  authors.

## Sequencing

1. **W1 + W3** in parallel (protocol + cache; minimal interdependency).
2. **W2** depends on W1 (needs the `OutputSchema` declaration to mean
   something).
3. **W4** depends on W1 + W3.
4. **W5** depends on W4 (needs the unresolved-ref tag).
5. **W6** depends on W1 + W2.
6. **W7** last; write once the shape is real.

A vertical slice that lights up the workflow end-to-end:
W1 → minimal W2 (one example plugin with a vendored schema) → W3 →
W4 (just the "known" and "auto-register" tiers, leaving inference/catchall
behavior unchanged) → smoke-test import path → fill in W5/W6/W7.

## Resolved decisions

- **W1** — discover-only at v1; no per-observe override.
- **W2** — mirrored layout (`schemas/<module-path>/`). Compatible with
  ORAS (layer internals are opaque to the spec).
- **W4 transport** — envelope JSON (shape-detected) for mu-driven
  imports; `pudl import --schema <ref>` for raw-JSON / agentic use.
  (Originally sidecar — reverted 2026-05-05.)
- **W4 catalog** — junction table `item_schemas`, not a column. Supports
  multi-schema-per-item from day one and avoids a future migration.
- **W6** — namespace check uses the plugin's `name` from
  `DiscoverResponse`; warn on mismatch; rule survives a future move to
  signed identity.

## Open questions remaining

- **W5**: when reclassifying, do we delete the `unresolved` row or
  retain it as a historical record? Lean delete; `classified_at` on the
  new row preserves temporal context.
- **W3**: GC strategy for the schema cache (append-only growth). Track
  separately as a follow-up.
- Confirm there isn't an existing pudl metadata structure that the
  junction-table choice should align with stylistically before W4
  begins.

## Risks

- **Coupling drift**: even an "optional" reference, once present, may
  pull mu into pudl-side schema concerns. Mitigation: schema cache and
  reference parsing live in a shared lib that neither side owns
  exclusively; mu never validates CUE, only ships and references it.
- **Schema cache bloat**: append-only growth across many version bumps.
  Mitigation: GC command (out of scope here, track separately).
- **Convention rot**: `mu/` namespace abuse. Mitigation: W6 warning;
  upgrade to enforcement later.

## Done criteria

- A plugin with a vendored CUE schema can be imported through pudl and
  the resulting catalog rows are classified under the declared schema
  without manual intervention.
- A plugin without `output_schema` behaves exactly as today
  (inference + catchall path unchanged).
- An import where the schema ref is unknown and no definition is
  available falls through inference and catchall, with the row tagged
  for later reclassify.
- `pudl reclassify` upgrades unresolved `item_schemas` rows once the
  schema is registered.
- A single item satisfying multiple schemas is representable
  (multiple `item_schemas` rows).
- `mu verify` warns on `mu/<wrong-name>` namespace usage.
