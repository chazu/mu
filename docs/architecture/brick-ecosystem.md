# BRICK ecosystem: current mu and PUDL boundaries

BRICK metadata is an optional classification layer shared by mu targets and
PUDL schemas. It does not create a second executor or an implicit translation
pipeline.

## Ownership

| Concern | Owner |
|---|---|
| Resource schemas, `#SystemModel` intent, observations, drift, approvals | PUDL |
| Plugin discovery and planning, action DAG, cache, sandbox, execution | mu |
| Secret provider resolution and storage | mu |
| Durable model/run-set provenance | PUDL |

Mu does not validate BRICK contracts. PUDL may use the optional `kind` and
`implements` metadata when it classifies or reports resources; mu preserves
those fields in target/manifest metadata while executing the target normally.

## Current workflow

When PUDL owns the model:

```bash
pudl run app                              # observe-only
pudl run app --converge                   # execute through mu, then verify
pudl run-set network app                  # exact producer/consumer set
pudl run-set network app --converge       # whole-set preflight, then execute
```

There is no `pudl drift`, `pudl export-actions`, or generated `mu.json` step.
PUDL renders a temporary `mu.cue` inside the selected mu project, invokes mu,
ingests its typed observe/build results, and records the outcome.

When a hand-written `mu.cue` is the source of truth:

```bash
mu build --plan //app
mu build --emit-manifest //app > manifest.json
mu observe --json //app > observe.json
```

Those results can be imported with `pudl mu ingest-manifest` and
`pudl mu ingest-observe`, but an apply receipt alone is only `converging`; a
subsequent live observation establishes `clean`.

## Composition

Mu composition uses explicit target `deps` and plugin-declared artifacts. PUDL
composition uses explicit model dependencies and value bindings.

Plain PUDL bindings project only scalar source fields marked
`@pudl(binding=plain)`. `pudl run-set` names the exact closed set, schedules
producers first, and pins their observations. It never starts an omitted model.

Sealed bindings remain provider refs. PUDL-generated targets set
`sealed_routing: "strict"`; plugin actions must claim exact refs and modes, all
declared inputs must be used, and each output has exactly one writer. Mu
validates routing without resolving values, resolves inputs immediately before
execution, and enforces `secrets.writable_refs` on writes.

## BRICK kinds

The accepted optional vocabulary remains:

| Kind | Meaning |
|---|---|
| `relationship` | Typed connection between blocks |
| `interface` | Contract another block may implement |
| `component` | Concrete implementation |
| `kit` | Curated composition of blocks |

Use these labels only when the contract adds useful reasoning. Ordinary build
targets and one-off resources may leave them unset.

Historical `mu.json` / export-actions designs remain available in git history
and `docs/history/`; they are not operational instructions.
