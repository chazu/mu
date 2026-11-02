# Pith E2E Example

Created `examples/pith-e2e/` with two files demonstrating inline pith
Plan and Transform programs in mu targets.

## Files

- `examples/pith-e2e/mu.cue` — Two targets:
  - `//data:metrics` — pith Plan emits an action that generates JSON metrics
  - `//report:summary` — depends on data; pith Transform reads dependency output,
    pith Plan emits the report-writing action
- `examples/pith-e2e/README.md` — Explains the example and key concepts

## Key Points

- Demonstrates Plan programs using `target/config` and `action/emit`
- Demonstrates Transform programs using `target/output`
- Shows how inline pith replaces the need for external toolchain plugins
- Self-contained, no external dependencies
