# Code Review: mu Build System

**Task:** mu-o9n
**Date:** 2026-02-19
**Scope:** Full codebase review (29 Go files across 8 packages)

## Executive Summary

mu is a build system with a plugin-based architecture using NDJSON IPC. The codebase is well-structured with clean separation of concerns, good error handling patterns, and reasonable test coverage. All tests pass and `go vet` reports no issues.

**Overall Assessment:** Solid foundation, production-ready after addressing the critical and high-priority items below.

### Test Coverage Summary

| Package | Coverage | Assessment |
|---------|----------|------------|
| cmd/mu | 0.0% | No integration tests for runBuild |
| internal/builtin | 87.2% | Good |
| internal/cas | 93.3% | Excellent |
| internal/cas/disk | 73.8% | Needs error path tests |
| internal/config | 87.0% | Good |
| internal/coordinator | 74.1% | Missing Build() tests |
| internal/dag | 88.0% | Good |
| internal/plugin | 73.6% | Missing concurrency tests |

---

## Architecture Review

### Package Structure (Good)

```
cmd/mu/          CLI entry point, flag parsing
internal/
  builtin/       Built-in actions (fetch with retry)
  cas/           Content-addressable store interface
  cas/disk/      Disk-backed CAS implementation
  config/        JSON config loading and validation
  coordinator/   Build orchestration, plugin coordination
  dag/           DAG construction, topological sort, parallel executor
  plugin/        NDJSON plugin protocol and process management
plugins/         Example babashka plugins (bootstrap, cowsay)
```

**Strengths:**
- Clean `internal/` boundary prevents external imports
- Single external dependency (`renameio`)
- Good interface-based design (cas.Store)
- Layered architecture: config -> coordinator -> dag -> executor

**Concerns:**
- No `internal/logging` package — errors are written directly to stderr
- No metrics or observability hooks

---

## Critical Issues (Fix Before Release)

### C1. Nil ProcessState Panic (dag/executor.go)

When `cmd.Run()` returns an error before the process starts (e.g., command not found), `cmd.ProcessState` is nil. Accessing `.ExitCode()` will panic.

```go
// executor.go ~line 169
exitCode := cmd.ProcessState.ExitCode() // PANIC if ProcessState is nil
```

**Fix:** Guard with nil check:
```go
exitCode := -1
if cmd.ProcessState != nil {
    exitCode = cmd.ProcessState.ExitCode()
}
```

### C2. Race Condition in Manager.Plan() (plugin/manager.go)

Between releasing the read lock and calling `Plan()`, another goroutine can `Close()` and nil the process:

```go
m.mu.RLock()
entry, ok := m.plugins[toolchain]
m.mu.RUnlock()       // <-- lock released
// ...
return entry.process.Plan(...)  // <-- entry.process could be nil
```

**Fix:** Hold RLock through the Plan call:
```go
m.mu.RLock()
defer m.mu.RUnlock()
entry, ok := m.plugins[toolchain]
if !ok { return nil, ... }
if entry.process == nil { return nil, ... }
return entry.process.Plan(ctx, ...)
```

### C3. Nil Dereference in Process.Kill() (plugin/process.go)

`p.cmd.Process.Kill()` at line 113 can panic if the process already exited.

**Fix:** Add nil check: `if p.cmd.Process != nil { p.cmd.Process.Kill() }`

### C4. Path Traversal in CAS (cas/disk/disk.go)

`blobPath()` slices `dgst.Hash[:2]` without validating length or characters. A crafted hash like `"../../../etc/passwd"` could escape the blob directory.

**Fix:** Validate hash is hex-only and at least 2 chars:
```go
if len(dgst.Hash) < 2 || !isHex(dgst.Hash) {
    return "", fmt.Errorf("invalid digest hash: %q", dgst.Hash)
}
```

---

## High Priority Issues

### H1. Non-Deterministic Topological Sort (coordinator/coordinator.go)

Map iteration in Go is random. The initial queue of zero-in-degree nodes is populated from a map, producing non-deterministic build ordering.

**Fix:** Sort the initial queue: `sort.Strings(queue)`

### H2. File Handle Not Deferred (coordinator/resolve.go)

```go
f, err := os.Open(path)
dgst, err := cas.ComputeDigest(f)
f.Close()  // not called if ComputeDigest panics
```

**Fix:** Use `defer f.Close()` immediately after `os.Open`.

### H3. Path Traversal in resolve.go

`filepath.Join(projectRoot, value)` doesn't validate the result stays within projectRoot. A value like `"../../../etc/passwd"` escapes the project.

**Fix:** Validate resolved path has projectRoot as prefix.

### H4. Internal Map Exposure (coordinator/toolchain.go)

`ArtifactsMap()` returns the internal artifacts map directly. Callers can mutate it.

**Fix:** Return a defensive copy.

### H5. Incomplete Validation (config/validate.go)

`Validate()` only checks Targets and Services. Plugins, Toolchains, Triggers, CacheConfig, and Preprocessor are unvalidated.

### H6. Missing Service Dependency Validation (config/validate.go)

`Service.DependsOn` references other services but there's no validation those services exist.

### H7. No Build() Integration Tests (coordinator)

The core `Build()` orchestration method has 0 direct tests. Coverage comes only from indirect paths.

---

## Medium Priority Issues

### M1. flag.ExitOnError Bypasses Cleanup (cmd/mu/build.go)

`flag.NewFlagSet("build", flag.ExitOnError)` causes `os.Exit()` on bad flags, bypassing runBuild's return path. Use `flag.ContinueOnError` instead.

### M2. formatTargets Inefficiency (cmd/mu/build.go)

```go
s += " "  // O(n^2) string concatenation
s += t
```

**Fix:** `strings.Join(targets, " ")`

### M3. Silent Cache Write Failures (dag/executor.go)

Cache write errors are swallowed with no logging. Makes production debugging impossible.

### M4. writeFile Drops Close Error (dag/executor.go)

`defer f.Close()` after `io.Copy` means a close error is lost. Should close explicitly and check error.

### M5. dirOf() Reinvents filepath.Dir() (dag/executor.go)

Custom `dirOf()` function reimplements `filepath.Dir()` without handling edge cases (Windows, trailing slashes).

### M6. Dead Code in TopoSort (dag/topo.go)

```go
inDegree[id] += 0      // no-op
_ = dep                  // unused assignment
```

### M7. Symlink Following in Config Walker (config/loader.go)

`filepath.Walk` follows symlinks, risking infinite loops from cyclic symlinks or config injection from outside project root.

### M8. No URL Scheme Validation (builtin/fetch.go)

`http.NewRequestWithContext` accepts any URL scheme. Should validate HTTPS.

### M9. stdin Not Closed on Timeout (plugin/process.go)

When context times out, `Kill()` is called but stdin is not closed first.

### M10. DiscoverInfo Returns Internal Pointer (plugin/manager.go)

`DiscoverInfo()` returns a pointer to internal `DiscoverResponse` state. Callers can mutate it.

---

## Low Priority / Code Quality

| Issue | Location | Description |
|-------|----------|-------------|
| L1 | cmd/mu/build.go | Unused `verbose` flag (assigned to `_`) |
| L2 | cmd/mu/build.go | Inconsistent "mu build:" error prefix |
| L3 | cas/cas.go | `NewSHA256()` doesn't validate hex input |
| L4 | config/loader.go | `loadFile()` returns bare errors without path context |
| L5 | config/types.go | Inconsistent JSON tag naming (Name→"target", Name→"service") |
| L6 | coordinator/coordinator.go | Magic number: `runtime.NumCPU()` for workers=0 |
| L7 | dag/graph.go | Internal field names `deps`/`rdeps` confusing vs method names |
| L8 | All packages | Missing package-level documentation comments |
| L9 | config/loader.go | No directory exclusion list (.git, node_modules) in Walk |
| L10 | Multiple | Abbreviated variable names (ds, er, t) could be more descriptive |

---

## Security Summary

| Risk | Location | Severity | Description |
|------|----------|----------|-------------|
| Path traversal | cas/disk/disk.go | HIGH | Hash values used in file paths without validation |
| Path traversal | coordinator/resolve.go | HIGH | Input file paths can escape project root |
| Command injection | dag/executor.go | MEDIUM | Arbitrary commands from plugin output (by design, needs docs) |
| Symlink attacks | config/loader.go | MEDIUM | Walk follows symlinks without validation |
| URL injection | builtin/fetch.go | MEDIUM | No scheme validation on URLs |
| Env injection | dag/executor.go | LOW | Plugin-provided env vars passed to commands unchecked |
| Unbounded JSON | cas/disk, coordinator | LOW | No size limits on JSON deserialization |

---

## Recommended Action Plan

### Phase 1: Critical Fixes (Before any release)
1. Fix nil ProcessState panic in executor
2. Fix Manager.Plan() race condition
3. Fix nil dereference in Process.Kill()
4. Add hash validation to CAS disk store (path traversal)
5. Add path containment checks in resolve.go

### Phase 2: Reliability (Before production use)
1. Make topological sort deterministic
2. Add Build() integration tests for coordinator
3. Use defer for file handles in resolve.go
4. Replace flag.ExitOnError with ContinueOnError
5. Add symlink detection in config walker
6. Complete config validation for all types

### Phase 3: Polish
1. Replace formatTargets with strings.Join
2. Replace dirOf with filepath.Dir
3. Remove dead code in topo.go
4. Add package-level documentation
5. Return defensive copies from public APIs
6. Add URL scheme validation in fetch
