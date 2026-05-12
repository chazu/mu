# pith-plugins guide added to mu guide command

Date: 2026-05-11

## Summary

Added `mu guide pith-plugins` topic to the guide command, documenting
how to author inline plugins using pith VM programs instead of full
NDJSON plugin binaries.

## Changes

- `cmd/mu/guide.go`: Added `printGuidePithPlugins()` function and wired
  it into the switch/index. Also added cross-reference in the overview's
  "what to read next" section.

## Public API

- `mu guide pith-plugins` — new guide topic

## Coverage

- What pith is (stack-based VM, JSON arrays, string literals)
- Three integration points: plan, transform, body fields
- Driver word availability table by phase (plan/transform/execute)
- Core vocabulary reference
- CUE schema validation with pith.#Program
- Decision table: pith vs plugin binaries
- Worked example with plan + transform
- Shared language with pudl
- Error handling and debugging
- Cross-references to related guides
