# Plugin Output Schemas: mu → pudl Typing

Status: brainstorm / design sketch
Date: 2026-05-04

## Problem

When data flows from a mu plugin into pudl (via `pudl import` against
plugin-produced output), pudl has no signal about what it's looking at and
falls through to the `pudl/core.#Item` catchall. The producer knows the
shape; the consumer is guessing. The current workaround — import, then
hand-author a schema and reclassify — is friction we'd rather not bake in.

mu plugins already declare a `produces` field, but it's a capability tag
(`"file"`, `"source:any"`), not a data schema. mu's CUE machinery is
currently aimed at plugin/target *config* schemas, not *output* schemas.

## Proposal

A plugin may **optionally reference a CUE module** that describes the shape
of its output. The reference can resolve two ways:

1. **Vendored** alongside the plugin (shipped in the plugin bundle)
2. **Remote** (fetched from a CUE module registry)

Resolution order: vendored → plugin-cache → remote. Vendored wins so that
air-gapped and reproducible builds don't depend on network.

### Schema cache is separate from plugin cache

Schemas are content-addressed by `(module, version)` and cached
independently of the plugin that referenced them. Multiple plugins
referencing `mu/aws@v1` share one canonical copy, and pudl sees one set
of definitions regardless of which plugin happened to surface them first.

### Namespace convention: three tiers of provenance

Schema package names carry provenance by convention:

- `pudl/...` — first-party pudl schemas (already the case today;
  `pudl/core.#Item` is the catchall).
- `mu/<plugin>` — schemas originated by a mu plugin's authors. The `mu/`
  prefix means "this came with a plugin," not "this is official mu."
- anything else — third-party or user-defined schemas.

This makes provenance visible in `pudl list`, drift output, and any other
place a type name appears. A user reading `mu/aws.#EC2Instance` knows
immediately where that definition came from.

#### Policing the convention

The convention is social, but it should have light mechanical backing —
otherwise everyone publishes under `mu/` and the signal rots. Initial
approach: `mu verify` (or pudl on register) warns when a `mu/foo` schema
is being registered from a plugin whose identity doesn't match `foo`.
Stricter enforcement (rejection, signing) can come later.

## Import-time behavior

When pudl encounters output tagged with a schema reference:

- **Schema known** → match and classify normally.
- **Schema unknown, definition travels with the data** (vendored
  in-bundle or attached) → auto-register, content-addressed, then
  classify. First import of a new plugin teaches pudl its types.
- **Schema unknown, no definition available** → fall back to pudl's
  existing inference path (heuristics + CUE unification against known
  schemas). If inference lands on a match, classify with that; either
  way, tag the item with the unresolved reference so `pudl reclassify`
  is trivial once the declared schema arrives.
- **Inference also yields nothing** → soft fallback to `pudl/core.#Item`,
  still tagged with the unresolved reference.

The combination of auto-register, inference, and tagged fallback means
the workflow degrades gracefully: you never lose data, you get the best
typing available at import time, and you never have to re-import to pick
up a schema that showed up later.

## Versioning and the append-only schema cache

When a plugin bumps its referenced module version, old imports stay
tagged with the old version (good — it's bitemporally honest about what
schema the data was understood under at import time). pudl ends up
holding multiple versions of `mu/aws` in its cache. The cache is
effectively append-only: "latest" is a query, not a replacement.

## Open questions

- **Reference syntax**: how does a plugin spell its module reference?
  Likely an addition to the `discover` response — e.g. an
  `output_schema: { module: "mu/aws", version: "v1", definition: "#EC2Instance" }`
  field. Exact shape TBD.
- **Multiple outputs, multiple schemas**: a plugin that emits more than
  one kind of artifact needs to tag each one. Probably a per-artifact
  field rather than per-plugin.
- **Remote registry**: which registry/registries does mu trust by default
  for remote module fetches? Reuse the OCI cache infra, or separate?
- **Identity check for namespace policing**: what counts as "the plugin's
  authors"? Plugin signing isn't in place yet; in the meantime, a
  declared author field plus a warning is probably enough.

## Why this is cheap

mu and pudl already share CUE as a substrate. pudl isn't learning a
foreign format — it's reading a sibling's package. The plugin protocol
already has a discover step where this metadata can ride. The schema
cache layers naturally onto the existing OCI/disk cache work.

The expensive part — authoring schemas — stays with whoever is best
placed to do it: plugin authors for plugin-specific types, the pudl
project for the universal core.
