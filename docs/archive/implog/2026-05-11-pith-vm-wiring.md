# Pith VM Wiring: getOutput, Transforms, HTTP/exec/CAS Drivers

Date: 2026-05-11

## Summary

Completed three remaining TODOs in the pith VM integration: wired up the
getOutput closure in the executor, injected transform phase actions into the
coordinator DAG, and added HTTP/exec/CAS driver words to the execute phase.

## Changes

### internal/dag/executor.go
- Added `completedOutputs` map and `outputsMu` RWMutex fields to Executor struct
- Initialize `completedOutputs` in Execute() before spawning workers
- Record completed action output paths by target prefix in the completion handler
- Replaced nil getOutput in executePithVM with a real closure that reads
  completed output files, parses JSON when possible, falls back to string
- Pass `e.Store` to RegisterExecDrivers for CAS driver support

### internal/pithvm/register.go
- Changed RegisterExecDrivers signature to accept `store cas.Store` parameter
- Added `http` driver with `get` (pop url, GET, decode JSON, push result) and
  `post` (pop body then url, POST JSON, decode response, push result)
- Added `exec` driver with `run` (pop []any args, execute command, push output)
  and `shell` (pop string, run via sh -c, push output)
- Added `cas` driver (when store non-nil) with `store` (pop value, marshal JSON,
  put in CAS, push digest string) and `fetch` (pop digest string, get from CAS,
  decode JSON, push result)

### internal/coordinator/coordinator.go
- After collecting planActions for a target, inject a synthetic `_transform`
  action when `t.Transform` is set. All real actions depend on `_transform`,
  ensuring the transform runs after deps complete but before target actions.

## Public API

```go
// Updated signature (added store parameter):
func RegisterExecDrivers(vm *pith.VM, env map[string]string, getOutput func(string) (map[string]any, error), store cas.Store)
```

### New pith VM words (execute phase)
- `http/get` -- (url -- response)
- `http/post` -- (url body -- response)
- `exec/run` -- (args -- output)
- `exec/shell` -- (cmdStr -- output)
- `cas/store` -- (value -- digestStr)
- `cas/fetch` -- (digestStr -- value)
