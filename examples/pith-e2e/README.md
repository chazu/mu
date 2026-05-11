# Pith VM End-to-End Example

Demonstrates inline pith Plan and Transform programs in mu targets,
replacing the need for external toolchain plugins.

## What This Shows

- **`//data:metrics`** — Uses a pith `plan` program to read target config
  and emit an action spec that generates a JSON metrics file. No plugin
  needed; the planning logic lives directly in `mu.cue`.

- **`//report:summary`** — Depends on `data`. Uses a pith `transform`
  program to read the dependency's output after it completes, then a
  pith `plan` program to emit the action that writes the final report.

## Key Concepts

**Plan programs** run during the planning phase. They can:
- Read target config via `target/config`
- Emit action specs via `action/emit`
- NOT perform side effects (no exec, no HTTP)

**Transform programs** run after dependencies complete but before
the target's own actions execute. They can:
- Read dependency outputs via `target/output`
- Read target config via `target/config`
- NOT emit actions or perform side effects

**Why this matters:** Traditional mu targets require a toolchain plugin
(a Babashka script or external binary) to handle planning. Pith programs
let you express the same logic inline — useful for simple targets,
glue targets, or config-driven pipelines where writing a full plugin
is overkill.

## Running

```sh
cd examples/pith-e2e
mu build //report:summary
```
