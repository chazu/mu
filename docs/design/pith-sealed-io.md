# Design: Sealed inputs & outputs for pith VM bodies

Status: draft
Scope: mu repo only (no protocol, no coordinator, no pudl changes)
Related guides: `mu guide secrets`, `mu guide pith-plugins`, `mu guide cache`

## Problem

Pith `body` actions can already perform side effects (`http/get`, `exec/run`,
`cas/store`), but they cannot participate in the secret subsystem:

- They cannot **read** a sealed input. The resolved value is delivered to the
  action via `execEnv`, but no pith word reads `execEnv`.
- `http/get` is URL-only — even with the value in hand, a pith body cannot send
  an `Authorization` / `PRIVATE-TOKEN` header.
- They cannot ergonomically **write** a sealed output: `MU_SEALED_OUT_DIR` is
  present in `execEnv`, but pith has no plain `file/write` word to drop a value
  there (only `cas/store`, which targets the CAS, not the side-channel dir).

The plumbing already reaches the VM. `internal/dag/executor.go:executePithVM`
receives the fully-resolved `env` (sealed inputs in env mode + `MU_SEALED_OUT_DIR`)
and passes it to `pithvm.RegisterExecDrivers(vm, env, ...)`, which currently
**ignores** the `env` parameter entirely. This is purely a missing-vocabulary
problem, not an architectural gap.

## Motivating use case

A single pith-driven target that fetches GitLab repos with a token and reshapes
them for `pudl/git.#GitLabRepository`, instead of falling back to a shell command:

```cue
{
  target: "//inventory/gitlab-repos"
  sealed_inputs:      { GITLAB_TOKEN: "pass:gitlab/token" }
  sealed_input_modes: { GITLAB_TOKEN: "env" }
  config: { base: "https://gitlab.com/api/v4" }
  plan: [ /* emit one body action below */ ]
}
```

Body (illustrative, final word names per this design):

```json
[
  "'GITLAB_TOKEN", "env/get",
  ["'PRIVATE-TOKEN", "swap", "obj/set"], "apply",        // build header map
  "'https://gitlab.com/api/v4/projects?membership=true&per_page=100",
  "swap", "http/request",                                 // GET with headers
  /* ...reshape to GitLabRepository... */
  "format/json",
  "'MU_SEALED_OUT_DIR", "env/get", "'/result.json", "concat", "swap", "file/write"
]
```

## Non-goals

- No change to the NDJSON plugin protocol or coordinator planning.
- No change to the action cache-key contract (`internal/dag/actionkey.go`).
- No new secret *provider*; `resolve_secret`/`store_secret` remain plugin-side.
- File-mode (`sealed_input_modes: file`) for sandboxed/toolchain actions stays
  unsupported — unchanged from today.

## Proposed words (all additive, all in `internal/pithvm/register.go`)

Registered **only** in `RegisterExecDrivers` (execute phase). Plan and transform
phases must NOT get these — plan/transform are declared side-effect-free and
secret-free, and adding secret access there would let secrets leak into
ActionSpecs that the coordinator logs/caches.

| Word | Stack effect | Purpose |
|---|---|---|
| `secret/get` | `(name -- Secret)` | Read a **sealed input** as a tainted value; sealed names only |
| `env/get` | `(name -- value)` | Read a **non-secret** env var; **errors on miss**; refuses sealed names |
| `env/get-default` | `(name default -- value)` | Non-secret env with `default` on miss; refuses sealed names |
| `http/request` | `(req -- json)` | HTTP with `{url, method?, headers?, body?}` map; `Reveal`s at the wire |
| `file/write` | `(path content -- )` | Write to a path under a sanctioned root; `Reveal`s at the syscall |
| `file/read` | `(path -- content)` | Read a file (file-mode inputs, general) — optional |
| `obj/set` | `(map key value -- map)` | Build header maps inline — optional helper (or reuse `set`) |

`secret/get` is the secret door; `env/*` is the non-secret door, and each refuses
the other's names. `http/request`/`file/write` are the only sinks that `Reveal`
tainted values. See "Secret namespace + taint tracking" for the full model.

### `env/get` / `env/get-default` miss semantics (resolved — OQ1)

**Decision: error on every miss for `env/get`; provide a defaulting variant for
non-secret env vars only.** (Refined by OQ3: `env/*` reads **non-secret** vars
only and refuses sealed names; secrets go through `secret/get`, not `env/get`.)

- `env/get (name -- value)` — errors if `name` is absent from `execEnv`, and
  **errors if `name` is a declared sealed-input name** (use `secret/get`). No
  silent empty string; a missing config var fails loud at the point of use.
- `env/get-default (name default -- value)` — returns `default` when a
  **non-secret** `name` is absent. **Refuses (errors) if `name` is a declared
  `sealed_inputs` name**, whether or not it resolved. Defaulting a secret would
  re-introduce the silent-proceed-on-failed-secret hazard: an empty token papered
  over a default, surfacing later as an opaque 401 instead of a clear resolution
  failure. Secrets always go through strict `secret/get`.

**Dependency:** to enforce the sealed-name refusal, `RegisterExecDrivers` must
receive the set of sealed-input names, not just the merged `env` map. Add a
`sealedNames map[string]bool` parameter; populate it at the single call site in
`executePithVM` from `a.SealedInputs`. This also lets `env/get` emit a sharper
error ("sealed_input %q declared but did not resolve" vs. generic
"env %q not set"), and it is the same thread-through OQ3 will build on.

**Security note:** `sealedNames` is a set of *names*, never values — it carries
no secret data and is safe to hold in the VM context. The cache-key boundary
(S1) is unaffected: neither word writes to any field that flows into
`ComputeActionKey`.

### Effort

| Item | Estimate |
|---|---|
| `env/get` + unit test | ~30 min |
| `env/get-default` + sealed-name refusal test | ~30 min |
| thread `sealedNames` into `RegisterExecDrivers` | ~15 min |
| `http/request` (header-aware) + test | ~1 hr |
| `file/write` + test | ~30 min |
| `file/read` (optional) | ~15 min |
| `obj/set` (optional, or reuse existing `set`) | ~15 min |
| Cache-key regression test (value not in key) | ~30 min |
| Guide updates (`mu guide pith-plugins` driver table) | ~30 min |
| **Total** | **~3–5 hrs** |

## Security model

The existing secret subsystem has five invariants (`mu guide secrets`, SECURITY
MODEL). The new words must preserve every one. Values must never appear in:
the CAS or action cache, build manifests, stdout/stderr captured by the cache
layer, verbose plugin I/O logs, or the process table.

### S1. Secret values must never enter the cache key

**The boundary that protects this already exists and must not move.** The cache
key (`actionkey.go`) hashes `a.Env` — the *static, declared* env. Sealed input
values are injected at exec time into a **separate `execEnv` copy**
(`executor.go`), never into `a.Env`. `env/get` reads from the runtime `env` map
passed to `RegisterExecDrivers` (= `execEnv`), so:

- A body that calls `env/get "GITLAB_TOKEN"` cache-keys on the **ref+mode**
  (`sealed_in:GITLAB_TOKEN=pass:gitlab/token mode=env`) plus the canonical
  `body` JSON — never the resolved token.
- **Regression test (required):** two builds with the same body+ref but
  different resolved values produce the **same** action key. Assert this
  directly; it is the load-bearing security property.

`env/get` must NOT be added to a path that writes the value back into any field
that flows into `ComputeActionKey`. It only ever pushes onto the VM stack.

### S2. `secret/get` / `env/get` must not become host-env exfiltration primitives

`execEnv` is `a.Env` (declared, non-secret) + sealed inputs + a few `MU_*` vars.
It is **not** the full host environment (`os.Environ()` is not merged in by the
executor). Decisions:

- **Do not** fall back to `os.Getenv` on a miss — a body cannot read
  `AWS_SECRET_ACCESS_KEY` or other ambient host secrets the target never
  declared.
- `secret/get` reads **only** names in `sealedNames`; `env/*` reads **only**
  non-sealed `execEnv` names. The two doors partition the namespace; neither can
  reach beyond `execEnv`.

### S3. No value logging — enforced by the taint type, not discipline

The OQ3 taint mechanism is what makes this structural rather than best-effort:

- Secrets enter only via `secret/get`, which pushes a `pith.Secret`. Anything
  derived from it stays a `Secret` (concat/format/get/merge propagate taint).
- `formatItem`/`formatStack` route through `Redact`, so a `Secret` anywhere on
  the stack prints `***REDACTED***` even under `--trace`.
- pith builds error strings via `Redact`; mu's new words additionally never
  interpolate `headers`/`body`/`content` into errors (status + URL + path only).
- Execute-phase trace is nil in production (`pith.New`, not `NewWithTrace`) —
  defense in depth on top of `Redact`.
- The real value is materialized only by `Reveal` at the three sanctioned sinks,
  immediately before the write, and is never pushed back onto the stack.

### S4. `file/write` must be confined; default to the sealed-out dir

A general `file/write` is a write-anywhere primitive. Constraints:

- **Path containment:** reject paths that escape the action's sanctioned write
  roots. Allowed roots: `MU_SEALED_OUT_DIR`, `MU_OUT`, and the action `WorkDir`.
  Resolve symlinks / `..` and verify the cleaned path is under an allowed root;
  error otherwise. This mirrors the sandbox's file-write deny posture for the
  bare-mode pith path (which is not kernel-sandboxed).
- **Permissions:** files written under `MU_SEALED_OUT_DIR` inherit that dir's
  `0700`; write the file itself `0600` to match the sealed-input file-mode
  convention (values stay off the world-readable path).
- The sealed-output capture (`captureSealedOutputs`) already runs post-exec for
  body actions and routes via `store_secret`; `file/write` only needs to land
  the bytes in the right place. **No change to capture/routing.**

### S5. Sealed-output values are forced impure (already true) — keep it

Actions with non-empty `sealed_outputs` are forced impure by the resolver, so
the cache is skipped and the `store_secret` side effect always runs. The new
words do not touch this; the test suite should assert a pith body with
`sealed_outputs` is still impure.

### `http/request` shape (resolved — OQ2)

**Decision: a single request-map argument, method-parameterized from day one.**

`http/request (req -- json)` where `req` is a map:

```
{ url:      string            // required
  method?:  string | *"GET"   // GET|POST|PUT|DELETE|PATCH
  headers?: {[string]: string}
  body?:    any }             // JSON-encoded when present
```

Rationale: a positional 4-input signature `(headers body method url -- json)` is
awkward in a concatenative VM and rigid — adding timeout/query-params later would
be a breaking stack-signature change to a word programs already depend on. A map
is one stack slot, self-documenting, and extends without ever changing the
signature. It composes with the object words (`obj/set`/`set`) used to build the
header map. The URL-only `http/get`/`http/post` remain as conveniences;
`http/request` is the full-control word and supersedes them for authenticated and
non-GET calls (the convergence half of the ACUTE loop needs POST/PUT with auth).

Validation: error if `url` absent or non-string; error on unknown method;
non-2xx status is an error whose message includes status + URL but **never** the
`headers` or `body` (S3 — they may hold the token).

### S6. `http/request` egress security

- Honor the action `Network` flag semantics. In bare (non-sandboxed) pith
  execution the flag is advisory (per `mu guide sandbox`, Copy level), but the
  word should still refuse when the action declares `network: false` so behavior
  is consistent with command actions and intent is enforced at the word layer.
- Send the token only to the caller-specified URL. Do **not** follow cross-host
  redirects with the auth header attached (Go's default client re-sends headers
  on same-host redirects only; explicitly strip sensitive headers on host
  change). Prevents token leakage to a redirected third-party host.

## Secret namespace + taint tracking (resolved — OQ3)

**Decision: implement full taint tracking in `github.com/chazu/pith` itself
(option B), with a dedicated `secret/*` access namespace in mu. Secrets stay
masked after `concat`/`format`/`get`/`merge`; they are revealed only at
sanctioned sinks.**

This is the one genuinely invasive piece. It spans two repos (a `pith` release +
mu wiring) because pith is an external module (`v0.2.0`, no `replace`) whose
builtins type-assert bare values and would strip any taint marker. Verified
against `pith/vm.go`, `builtins.go`, `data.go`, `cue.go`: the stack holds bare
`any`; every value word (`concat`, `get`, `set`, `merge`, `path`, `pick`,
`split`, …) does raw `v.(string)` / `v.(map[string]any)` assertions and produces
fresh untainted values. So taint must live inside pith.

### The core type (in pith)

```go
// Secret wraps a value whose contents must never be rendered to a human-facing
// sink (trace, error, log). It propagates through value words and is revealed
// only by sanctioned effectful sinks.
type Secret struct{ inner any }

func NewSecret(v any) Secret { return Secret{inner: v} }
func (s Secret) Inner() any  { return s.inner }
func (s Secret) String() string { return "***REDACTED***" } // Stringer safety net
```

### Two render modes — the key distinction

A naive "tainted ⇒ always print `***`" breaks the sealed-output path: a
`file/write` of a JSON body containing the token would persist `***REDACTED***`
instead of the real secret. So pith exposes two recursive walkers:

- **`Redact(v)`** — for human sinks (trace formatter, error messages). Replaces
  every `Secret` (at any depth in maps/slices) with `***REDACTED***`. Used by
  `formatItem`/`formatStack` and anywhere pith builds an error string.
- **`Reveal(v)`** — for sanctioned machine sinks only. Recursively unwraps every
  `Secret` to its `inner`, yielding plain values for the actual syscall/network
  write. Called **only** at the boundary inside `http/request`, `file/write`,
  `cas/store`. Never pushed back onto the stack.

### Taint propagation rules (in pith builtins)

Pop helpers (`popString`, `popMap`, `toFloat64`, `toBool`, `toSlice`) gain
transparent unwrapping that also reports whether the operand was tainted:

```go
func unwrap(v any) (any, bool) {        // (inner, wasTainted)
    if s, ok := v.(Secret); ok { return s.inner, true }
    return v, false
}
```

Words that derive a new value re-wrap the result in `Secret` iff **any** operand
was tainted:

- **Propagate taint:** `concat`, `split`, `get`, `path`, `set`, `merge`, `pick`,
  `omit`, `values`. Stack movers (`dup`/`over`/`swap`/…) never unwrap, so taint
  rides along for free.
- **`format/json` (mu side):** if the input contains any `Secret` at any depth,
  the produced JSON has the **real** values inlined (valid/complete JSON) but the
  whole resulting string is wrapped `Secret` — "anything derived from a secret is
  a secret." Serializes via `Reveal` into the content, then taints the output.
- **Comparisons/arithmetic** (`eq`, `lt`, `add`, …) unwrap operands and push a
  plain `bool`/number. A boolean derived from a secret is not the secret;
  tainting booleans would make `if`/`filter` unusable. Deliberate, documented
  declassification of *predicates* (see Residual risks).

### The `secret/*` namespace (mu side)

- `secret/get (name -- Secret)` — sealed inputs only. Errors if `name` ∉
  `sealedNames`, or declared-but-unresolved. Pushes `pith.NewSecret(value)`.
- `env/get` / `env/get-default` — non-secret env only; **refuse** names in
  `sealedNames`. Push plain strings.

The call site is self-documenting: `secret/get "GITLAB_TOKEN"` visibly marks
where a secret enters, and the type (not discipline) keeps it masked thereafter.

### Worked flow (sealed output)

```
…build payload map containing Secret…
format/json                                  → Secret("{...real json...}")
"'MU_SEALED_OUT_DIR" env/get "'/out" concat  → path (plain)
swap file/write                              → Reveal() → real bytes to 0600 file;
                                               captureSealedOutputs → store_secret
```

At no point does the token appear in a trace line, an error, the action cache
key (S1), or stdout.

### Effort (revised — this is the big one)

| Item | Estimate |
|---|---|
| pith: `Secret` type, `Redact`/`Reveal` walkers | ~2 hr |
| pith: unwrap in pop helpers + re-taint in value words | ~3–4 hr |
| pith: `formatItem` → `Redact`; taint unit tests across every word | ~3 hr |
| pith: cut a new tagged release; bump mu's `go.mod` | ~30 min |
| mu: `secret/get`, `env/*` refusal, `sealedNames` thread-through | ~1 hr |
| mu: `Reveal` at `http/request`/`file/write`/`cas/store` sinks | ~1 hr |
| mu: end-to-end + leak-absence tests | ~2 hr |
| **Subtotal (taint)** | **~1.5–2 days** |

On top of the ~3–5 hr base word work, the full feature with proper taint is
**~3 days**.

### Residual risks (documented, accepted)

- **Predicate declassification:** comparisons/arithmetic on a secret yield plain
  booleans/numbers; `len` yields a plain int. A body could in principle
  exfiltrate a secret bit-by-bit (`secret eq "a"`, branch, repeat). Posture:
  pith bodies are authored in `mu.cue` by the **same trust domain** that declares
  the `sealed_inputs`. This is leak-*prevention* against accidental
  logging/serialization, **not** a sandbox against a body actively trying to leak
  its own declared secret. Stated as an explicit non-goal.
- **Third-party / future builtins:** any pith word that does `v.(string)`
  without the unwrap helper hard-errors on a `Secret` (fail-closed) — a loud type
  error, not a silent leak. Safe failure direction.

## Test plan

1. **Unit:** each new word — happy path + error path (wrong stack type, missing
   name, path escape, non-2xx HTTP).
2. **Security S1 (critical):** same body + same ref + *different resolved value*
   → identical `ComputeActionKey`. Resolved value never appears in the key bytes.
3. **Security S2:** `env/get` errors for an undeclared name and **errors for a
   sealed name** (must use `secret/get`); `secret/get` errors for a non-sealed
   name; neither can read an ambient host env var not in `execEnv`.
4. **Security S4:** `file/write` to `../../etc/passwd` (and symlink escape) is
   rejected; write under `MU_SEALED_OUT_DIR` succeeds with `0600`.
5. **Security S5:** pith body with `sealed_outputs` is impure; cache is skipped;
   `SealedOutputWriter` invoked once per name.
6. **Taint propagation (OQ3, pith):** for every value word — `concat`, `get`,
   `set`, `merge`, `path`, `pick`, `split`, `values`, stack movers — a `Secret`
   operand yields a `Secret` result. `format/json` of a struct containing a
   `Secret` yields a tainted string whose content is the **real** JSON.
   `eq`/`lt`/`add` yield plain values (declassified predicates).
7. **Taint rendering (OQ3, pith):** `Redact` masks `Secret` at any depth in
   maps/slices; `formatStack`/error strings show `***REDACTED***`; `Reveal`
   round-trips to the real value. A `--trace` run over a body holding a secret
   shows no secret bytes.
8. **Integration:** end-to-end target reads a sealed input via `secret/get`,
   calls `http/request` against a local test server asserting the **real** header
   arrived (Reveal worked), `file/write`s a tainted JSON payload to
   `MU_SEALED_OUT_DIR` asserting the **real** bytes landed, and the value is
   routed by a stub provider — asserting the value appears in **none** of: action
   key, CAS, manifest JSON, captured stdout, trace output.
9. **Negative logging:** force `http/request` and `file/write` errors; assert the
   token/content is absent from the returned error string.

## Open questions

1. ~~`env/get` on miss: empty string vs error?~~ **RESOLVED:** error on every
   miss; `env/get-default` defaults non-sealed names only (sealed names error).
2. ~~Should `http/request` accept a method arg now?~~ **RESOLVED:** yes —
   single request-map `{url, method?, headers?, body?}`, method defaults GET.
3. ~~Dedicated `secret/...` namespace, and how to do redaction?~~ **RESOLVED:**
   full taint tracking in pith (option B) + `secret/get` namespace; `Redact` for
   human sinks, `Reveal` at sanctioned sinks. See the taint-tracking section.

_No open questions remain. Implementation can proceed; the one cross-repo
dependency is cutting a new `github.com/chazu/pith` release with the `Secret`
type before mu can wire the sinks._
