# Feat: Tagged plugin stderr capture

**Date:** 2026-04-19
**Status:** Design / Epic
**Scope:** `internal/plugin/process.go`, `internal/plugin/manager.go`,
and CLI verbosity plumbing in `cmd/mu/`.
**Source brainstorm idea:** #20 (stderr → structured logs), interacts with #16
(structured progress from plugins).

---

## Summary

Plugin subprocesses today have their stderr redirected into an unbounded
per-process `bytes.Buffer` (`internal/plugin/process.go:64`,
`cmd.Stderr = &p.stderr`). The buffer is only surfaced to the user when a
plugin exits with an error (via `Close()`) or when a response scan fails
(via `scanError()`). That has two consequences plugin authors trip over
every week:

1. **Nothing is visible during normal operation**, even with `--verbose`
   (which is declared on `mu build` as `_ = fs.Bool("verbose", false, "show
   plugin I/O")` but never read). An author adding a `println` to their
   `plugin.bb` sees silence and concludes the plugin isn't being invoked.
2. **When multiple plugins run concurrently** (Manager.Start kicks off
   every plugin in parallel via `errgroup`), any output that *does* escape —
   e.g. if a plugin author writes directly to `/dev/stderr`, or if a future
   change inherits stderr for faster debugging — is **interleaved byte-for-byte**
   across plugins with no tag. This is the #1 "WTF" complaint from new
   plugin authors.

This epic introduces a uniform stderr capture path: a per-process
`bufio.Scanner` goroutine reads the plugin's stderr pipe line-by-line,
each line is prefixed with `[<plugin-name>]`, lines are retained in a
bounded ring buffer, and they are either:

- **Streamed live** to the coordinator's stderr when `--verbose` is on, or
- **Flushed on failure** (dumping the last N lines with tags) when the
  plugin errors out or exits unexpectedly, or
- **Exposed programmatically** via a new `mgr.Logs(name)` for the
  structured-progress renderer and test harness (brainstorm idea #16 and #25).

The wire-level escape hatch for future structured progress (idea #16,
`{"method":"status", ...}`) is preserved: **stderr carries only
free-form human log lines; structured messages stay on stdout NDJSON**.
That boundary is stated as part of this epic's contract so the two
features don't collide when #16 lands.

---

## User stories

### Plugin author

- **As a plugin author**, I want my plugin's `println`/`log/println!`/`fmt.Fprintln(os.Stderr, …)` to show up in `mu build --verbose` tagged with my plugin name, so I can iterate on a plugin without attaching a debugger.
- **As a plugin author**, when my plugin crashes, I want the last N lines of its stderr surfaced in the top-level build error — tagged with the plugin name — so I can see what went wrong without re-running with a debug flag.
- **As a plugin author**, I want to write free-form logs on stderr without worrying about corrupting the NDJSON protocol on stdout.

### Build user

- **As a user running `mu build`**, I want the normal build output to stay quiet — plugin chatter should be suppressed by default.
- **As a user of `mu build --verbose`**, I want to see plugin stderr lines **prefixed with `[plugin-name]`** and ordered (not byte-interleaved) so I can tell who said what when multiple plugins are active.
- **As a user diagnosing a failed build**, I want the error output to include the last ~100 stderr lines from the failing plugin (tagged) automatically, so I don't have to re-run.

### Tool author / `mu plugin test` harness

- **As a test-harness author** (brainstorm idea #25), I want to read a plugin's captured logs programmatically (`mgr.Logs("go")`) so I can assert on them.

---

## Acceptance criteria

### Capture mechanics

1. `StartProcess` replaces `cmd.Stderr = &bytes.Buffer{}` with a pipe
   (`cmd.StderrPipe()`); a dedicated goroutine consumes the pipe with a
   `bufio.Scanner` (buffer up to the same 1 MiB cap used for stdout).
2. Each scanned line is delivered to a bounded, thread-safe `logBuffer`
   owned by the `Process`. The ring retains the last **N = 200 lines**
   (configurable at construction, but fixed-for-now by a package constant).
3. Lines are never dropped silently: overwriting a ring slot is fine
   (by design), but scanner errors are themselves recorded as a tagged
   synthetic line (`[<name>] <stderr read error: …>`).
4. The goroutine exits cleanly when the stderr pipe reaches EOF (child
   closed stderr, typically on exit). `Close()` waits for this goroutine
   in addition to `cmd.Wait()`.

### Live streaming

5. `Process` accepts an optional `io.Writer` sink (e.g. `os.Stderr`). When
   set, every scanned line is written through as `[<name>] <line>\n`.
6. Writes to the live sink are serialized across all plugin processes
   via a single `sync.Mutex` (or `sync.Mutex`-wrapped writer) shared by
   the Manager, so **lines from different plugins never interleave
   mid-line**. Ordering between plugins is best-effort (scheduling),
   but no line is ever split.
7. When `--verbose` is **not** set on `mu build` (and its siblings), no
   live streaming happens — the ring buffer still fills.

### Failure surfacing

8. If `Process.send` returns an error (timeout, decode, EOF), the returned
   error wraps up to the last N stderr lines (tagged) from the ring,
   replacing today's unbounded `p.stderr.String()`.
9. On `Close()`, if `cmd.Wait()` returns a non-nil error and the ring
   is non-empty, the error message contains the tagged tail — same
   format as #8.
10. Secrets: the plugin-stderr path is *not* a secret channel. The epic
    documents a non-goal — plugins must not log secret values; we don't
    redact. (Explicit callout so we don't accidentally promise redaction.)

### Manager API

11. `Manager.Logs(name string) []string` returns a **copy** of the ring
    contents (tagged strings, oldest first) for the named plugin, or
    `nil` if the plugin is unknown/not started.
12. `Manager.SetVerbose(w io.Writer)` (or `NewManager`-time option) wires
    the live sink through to every `Process` it spawns. Default is `nil`
    (no live streaming).
13. Manager-level mutex protects the shared live-sink writer (so one
    plugin's long line can't interleave with another's).

### CLI wiring

14. `cmd/mu/build.go` reads the existing `--verbose` flag value (today
    it's declared and discarded via `_ = fs.Bool(...)`) and calls
    `mgr.SetVerbose(os.Stderr)` when true.
15. At least one other command that spawns plugins (`mu observe`, `mu
    plugin`) gains the same `--verbose` plumbing for consistency.
16. On any plugin-originated error surfaced to the user in `cmd/mu`,
    the error message contains the tagged last-N-lines dump (this
    falls out of #8 automatically — the acceptance test verifies it).

### Interaction with future structured progress (brainstorm idea #16)

17. **Documented contract** (in the package doc comment of
    `internal/plugin/process.go`):
    - **stdout** carries NDJSON request/response pairs and, in a future
      version, NDJSON status messages (`{"method":"status", ...}`).
    - **stderr** carries free-form, line-delimited human logs only.
      The coordinator never parses stderr as JSON, and the plugin
      must not rely on stderr for protocol semantics.
18. Manager's `send` loop on stdout is untouched by this change —
    the stdout scanner still reads exactly one JSON line per request,
    serialized by `p.mu`. Idea #16, when implemented, will extend the
    stdout loop (likely by routing messages through a method switch)
    without touching the stderr goroutine.
19. Rationale note in the doc: we do **not** multiplex progress events
    onto stderr (e.g. tag-based sniffing) because (a) it would make
    `--verbose` accidentally fire the progress renderer, (b) it defeats
    the "free-form for authors" design, and (c) it forces every future
    log line to adopt structured formatting.

### Tests

20. `process_test.go` (new or extended): spawn a tiny Bash/bb helper
    plugin script in `testdata/` that writes to both stdout
    (protocol) and stderr (free text); assert that:
    - successful discover preserves normal behavior;
    - `Logs()` contains exactly the stderr lines, tagged;
    - ring truncates at N when overfilled;
    - live sink receives identical tagged lines when configured;
    - concurrent writes from two Processes sharing a sink produce
      no interleaved-mid-line garbage (verify by reading back and
      checking each line starts with a known tag).
21. Test for failure surfacing: a helper plugin that writes a known
    marker to stderr and then exits 1. Assert the error returned by
    `Close()` contains the marker with a `[<name>]` prefix.
22. Regression test covering the existing `scanError()` path (EOF mid-
    response with stderr content) continues to pass with the new ring.
23. Race-detector clean: `go test -race ./internal/plugin/...`.

---

## Technical context

### Relevant existing code

| File | Current role | Change |
|------|--------------|--------|
| `internal/plugin/process.go` | owns `cmd`, stdin, stdout scanner; `cmd.Stderr = &p.stderr` (bytes.Buffer); `scanError()` reads `p.stderr.String()`; `Close()` wraps stderr into error. | Replace `stderr bytes.Buffer` with a `*logBuffer` + goroutine consuming `cmd.StderrPipe()`. Add `liveSink io.Writer` field. Update `scanError`, `Close`, `send`-alive-check to use the ring. |
| `internal/plugin/manager.go` | registers/starts plugins, routes Plan/Observe/ResolveSecret. | Add `verboseSink io.Writer` + `sinkMu sync.Mutex`; pass a per-plugin line-serialized `io.Writer` into each `StartProcess`. Add `Logs(name)`. |
| `internal/plugin/log.go` *(new)* | — | Ring buffer type (`logBuffer`) with `Append(line string)`, `Snapshot() []string`, capacity constant `DefaultLogRingSize = 200`. Pure, no I/O. |
| `cmd/mu/build.go`, `cmd/mu/observe.go`, `cmd/mu/plugin.go` | declare `--verbose` (unused). | Read the flag; call `mgr.SetVerbose(os.Stderr)` when true. |
| `internal/plugin/plugin_test.go`, `testdata/` | existing helper scripts (bb-based). | Add a stderr-emitting helper plugin. |

### Patterns discovered / followed

- **Start-parallel-with-errgroup**: `Manager.Start` launches all
  plugins via `errgroup.WithContext` and calls `closeAllLocked` on any
  failure. The stderr goroutine fits this model — each Process owns
  its goroutine, and `Close()` joins it.
- **Defensive copies on exported getters**: `DiscoverInfo` already
  returns a deep copy. `Logs(name)` must do the same (return a freshly
  allocated `[]string`).
- **`p.mu` serializes request/response pairs**, not I/O direction —
  the stderr goroutine is independent and requires no lock on `p.mu`.
- **No shared CLI framework yet** (brainstorm idea #21). Verbose
  plumbing is duplicated across subcommands until that lands; accept
  the duplication for now, keep the API (`SetVerbose`) minimal so a
  future shared context can wire it once.

### Sketch (non-binding)

```go
// internal/plugin/log.go (new)
package plugin

const DefaultLogRingSize = 200

type logBuffer struct {
    mu    sync.Mutex
    lines []string // len == cap(lines) once full; circular
    head  int      // next write slot
    full  bool
}

func newLogBuffer(n int) *logBuffer { … }
func (b *logBuffer) Append(s string)   { … }
func (b *logBuffer) Snapshot() []string { … } // oldest first
```

```go
// internal/plugin/process.go (changed excerpt)
type Process struct {
    name   string
    cmd    *exec.Cmd
    stdin  io.WriteCloser
    scanner *bufio.Scanner
    logs    *logBuffer
    liveSink io.Writer  // may be nil; writes to it are caller-serialized
    stderrDone chan struct{}
    mu sync.Mutex
}

func (p *Process) pumpStderr(r io.ReadCloser) {
    defer close(p.stderrDone)
    sc := bufio.NewScanner(r)
    sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
    for sc.Scan() {
        tagged := "[" + p.name + "] " + sc.Text()
        p.logs.Append(tagged)
        if p.liveSink != nil {
            fmt.Fprintln(p.liveSink, tagged)
        }
    }
    if err := sc.Err(); err != nil {
        p.logs.Append("[" + p.name + "] <stderr read error: " + err.Error() + ">")
    }
}
```

### Non-goals

- Redacting secrets from log output.
- Structured/JSON logging from plugins (that's idea #16's job, on
  stdout).
- Persisting logs to disk / CAS.
- Retro-fitting stderr capture onto non-plugin subprocesses (executor
  actions). Those are a separate concern already largely handled by
  the sandbox.

---

## Open questions

1. **Ring size.** 200 lines feels right for bb plugins (typical crash
   stack is <50 lines). Is this configurable per-plugin, global env
   var (`MU_PLUGIN_LOG_LINES`), or package constant? Recommendation:
   package constant now, env var later if users ask.
2. **`--verbose` vs `MU_VERBOSE` env var.** Every subcommand spawning
   plugins needs the flag. Should we short-circuit the duplication by
   also reading `MU_VERBOSE=1` from env in `NewManager`? This would
   let brainstorm idea #21 (CLI framework) simplify cleanup.
3. **Line length cap.** `bufio.Scanner` truncates at 1 MiB; beyond
   that the goroutine records a read error and exits. Do we want to
   fall back to a `Reader` that reads chunks as "lines" instead? For
   now: no — 1 MiB of plugin stderr on a single line is itself a bug.
4. **Tag format.** `[name]` vs `name:` vs ANSI-colored `name │`. Keep
   boring (`[name]`) so log grepping stays trivial; color is idea #22.
5. **Close ordering.** Today `Close` does `stdin.Close(); cmd.Wait()`.
   With a stderr pump goroutine, `Wait()` blocks until the pipe is
   drained by the pump (Go's `exec` documents this). We just need to
   `<-p.stderrDone` before returning — confirm no deadlock if the
   plugin is stuck and `Close` is called (likely need a context /
   timeout wrapper on `Close`). Leaning toward: accept the existing
   semantics; document that `Close` blocks on cooperative exit.
6. **Should the ring survive `Close()`?** Users may want `mgr.Logs(name)`
   *after* a failed build to introspect. Proposal: yes — `Logs(name)`
   works post-Close until `Manager.Close` itself returns. Remove entry
   only on explicit `Manager.Reset` (not in scope).
7. **Observability for `mu plugin list`.** Should `mu plugin list` grow
   a `--show-logs` flag to dump the last run's logs? Probably, but
   out of scope here — leave as a follow-up.
8. **Interaction with `cmd.ProcessState` check in `send`.** Today the
   alive-check uses `p.stderr.String()` — with the ring, we instead
   join the ring snapshot. That changes the message if the stderr has
   rotated past N; users with extremely chatty plugins would see a
   tail rather than a full dump. Acceptable trade-off.
