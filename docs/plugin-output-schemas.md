# Authoring plugin output schemas

A mu plugin can optionally declare a CUE schema for the data it
produces. Downstream consumers (most notably pudl) use this to classify
imported data without re-inferring its shape.

This is **opt-in**: plugins without an output schema continue to work
exactly as before; pudl falls back to inference and the catchall
`pudl/core.#Item`.

## When to add an output schema

- The plugin produces structured records that pudl ingests.
- The shape of those records is stable and worth a typed name.
- You want users to query/filter on a meaningful schema name in
  `pudl list`, drift output, etc.

If the plugin only produces files for `mu` itself to consume, you
don't need this — output schemas are for plugins whose output crosses
into pudl.

## The two pieces

1. A **declaration** in your plugin's `discover` response.
2. A **vendored CUE module** shipped in the plugin bundle.

### 1. Declare it in `discover`

Add an `output_schema` field to your discover response:

```clojure
{"name"             "aws"
 "version"          "0.1.0"
 "protocol_version" 1
 "consumes"         []
 "produces"         ["aws:resource"]
 "output_schema"    {"module"     "mu/aws"
                     "version"    "v1"
                     "definition" "#EC2Instance"}}
```

Field meaning:

- `module` — the CUE module path. Follow the namespace convention in
  `cue-conventions.md` §6: `mu/<plugin-name>` for schemas you own.
- `version` — opaque version label. `v1`, `2026-05-04`, anything that's
  stable per release of your schema.
- `definition` — optional. The specific CUE definition selector
  (e.g. `#EC2Instance`) when the module exports more than one.

### 2. Vendor the schema in your plugin

Lay the schema files out under `schemas/` in your plugin source tree
so the directory tree mirrors the module path:

```
plugins/aws/
├── plugin.bb
├── mu.cue
└── schemas/
    └── mu/
        └── aws/
            └── ec2.cue       ← package aws
```

Then declare the vendored module in `mu.cue`:

```cue
plugin: {
    entrypoint: "plugin.bb"
    toolchain:  "bb"
    files: ["plugin.bb"]
    schemas: [
        {module: "mu/aws", version: "v1", path: "schemas/mu/aws"},
    ]
}
```

Each `schemas[]` entry pins one CUE module to a directory. mu picks
these up at bundle time, includes the `.cue` files in the plugin
artifact, and records the declarations in the OCI manifest so pudl can
find them on the consumer side.

## How pudl picks up the schema

When mu imports plugin output into pudl (or a user runs
`pudl import --schema mu/aws@v1#EC2Instance`):

1. pudl looks the ref up in its schema cache. Hit → classify, record
   `item_schemas` row with `status='declared'`.
2. Miss with vendored definition shipped alongside → auto-register,
   classify, record with `status='auto_registered'` *(scheduled — not
   yet implemented)*.
3. Miss without a definition → run pudl's existing inference, record
   the inferred result and tag the item with the unresolved ref so
   `pudl reclassify` can upgrade it later.

Today the resolution lands at step 3 for refs pudl hasn't been taught;
once cross-process schema cache exposure is wired, steps 1 and 2 light
up automatically — no plugin changes required.

## Verifying

`mu verify` walks `<projectRoot>/plugins/*` and warns when a plugin
declares a `mu/<x>` namespace whose `<x>` doesn't match its own name.
The warning is advisory; it does not fail verification. The intent is
to catch typos and discourage namespace squatting.

## Relevant code paths

- `internal/plugin/protocol.go` — `DiscoverResponse.OutputSchema`,
  `SchemaRef`.
- `internal/config/types.go` — `PluginManifest.Schemas`,
  `SchemaDecl`.
- `internal/coordinator/pluginschemas.go` — `LoadVendoredSchemas`.
- `internal/schemacache/` — content-addressed cache for vendored CUE.
- `internal/cas/oci/plugin.go` — `PluginConfig.Schemas` carried in the
  OCI artifact.
