# Pith VM Integration

Date: 2026-05-11

## Summary

Added pith VM as an alternative execution backend for mu targets. Targets can
now use inline stack programs (pith VM) instead of shell commands or plugin
binaries for both planning and execution phases.

## Changes

### New package: `internal/pithvm/`
- `register.go` -- Phase-scoped driver registration for pith VM integration.
  Three registration functions for plan, transform, and execute phases:
  - `RegisterPlanDrivers(vm, targetConfig, buf)` -- plan phase words
    (`action/emit`, `target/config`, config field refs)
  - `RegisterTransformDrivers(vm, targetConfig, getOutput)` -- transform phase
    words (`target/config`, `target/output`)
  - `RegisterExecDrivers(vm, env, getOutput)` -- execute phase words
    (`target/output`)
  - `ActionBuffer` -- collects emitted action specs during plan phase

### Modified files

- **go.mod** -- Added `github.com/chazu/pith` dependency with local replace
  directive (`=> ../pith`)
- **internal/plugin/protocol.go** -- Added `Body []any` field to `ActionSpec`
  (pith VM program, mutually exclusive with Command)
- **internal/dag/graph.go** -- Added `Body []any` field to `Action`
- **internal/config/types.go** -- Added `Plan []any` and `Transform []any`
  fields to `Target`
- **internal/coordinator/resolve.go** -- Passes `Body` through from ActionSpec
  to dag.Action
- **internal/coordinator/coordinator.go** -- Plan phase dispatches to pith VM
  when target has `Plan` field set. Added `mapToActionSpec()` helper to convert
  pith-emitted maps to ActionSpec structs.
- **internal/dag/executor.go** -- Execution dispatches to `executePithVM()`
  when action has Body set. Added `executePithVM()` method.
- **internal/dag/actionkey.go** -- Body field included in cache key hash
  (JSON-serialized for determinism)

## Public API

### pithvm package
```go
type ActionBuffer struct { Actions []map[string]any }
func RegisterPlanDrivers(vm *pith.VM, targetConfig map[string]any, buf *ActionBuffer)
func RegisterTransformDrivers(vm *pith.VM, targetConfig map[string]any, getOutput func(string) (map[string]any, error))
func RegisterExecDrivers(vm *pith.VM, env map[string]string, getOutput func(string) (map[string]any, error))
```

### New fields
- `plugin.ActionSpec.Body []any`
- `dag.Action.Body []any`
- `config.Target.Plan []any`
- `config.Target.Transform []any`
