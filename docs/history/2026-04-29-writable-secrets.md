# Writable Secrets & Symmetric Secret Protocol

**Date:** 2026-04-29
**Status:** Brainstorm / design note — phases 1–5 implemented (see Implementation Notes at end)
**Author:** chazu (with claude)
**Scope:** Make the secret-provider plugin protocol symmetric (read +
write), add a sealed *output* path on targets, and reconsider the
delivery shape for resolved secrets so multi-line credentials stop
being shoehorned through env vars.

---

## Motivating concrete

The loosh infra project (`~/dev/loosh/dev/infra/mu.cue`) drives a real
registry deployment against `dalian.softchewy.center` using three
plugins from this repo: `pass`, `remote-exec`, `remote-file`. A diff
of the in-tree plugin sources against the loosh copies shows:

| Plugin | State |
|---|---|
| `remote-exec/plugin.bb` | byte-identical |
| `remote-file/plugin.bb` | byte-identical |
| `pass/plugin.bb` | **diverged**: loosh is at v0.2.0, repo is v0.1.0 |

The pass divergence is one feature: a `pass:raw:<path>` ref form that
returns the full multi-line `pass show` output (with trailing newlines
trimmed) instead of only the first line. It exists because the loosh
project needs to load an SSH private key into the agent:

```cue
sealed_inputs: SSH_KEY: "pass:raw:loosh/void.loosh.cloud"
```

Two things converge on the same area:

1. The `pass:raw:` extension is a strict, additive ref-grammar change
   — it should be upstreamed before further drift.
2. While digging into how to *put* data into pass (e.g. mint an
   `htpasswd` admin password as part of the deploy rather than
   pre-seeding by hand), it became clear the secret protocol is
   lopsided: there is no symmetric way to write a value through the
   provider, and no path for an action to emit a secret without it
   landing in stdout / the action cache.

---

## What's lopsided today

Current capabilities: `discover`, `plan`, `observe`, `resolve_secret`.

On the consumer side, only `sealed_inputs` exists:

```cue
sealed_inputs: ADMIN_PASS: "pass:loosh/registry.loosh.cloud/admin"
```

The runner resolves the ref at action time, hands the value as an env
var, and is careful never to cache it. Good. But:

- **Bootstrapping is out-of-band.** The loosh `//zot/htpasswd` target
  reads `ADMIN_PASS` from pass — somebody had to `pass insert` it
  manually first. There is no mu-native way to declare "this ref must
  exist; derive it from `openssl rand -base64 24` if absent."
- **Captured credentials have no home.** When an action mints a
  credential (terraform output, a one-time CA-signed token, an API
  key minted by a remote service), there is no way to route the value
  into the secret store. Today it has to land in stdout or a file the
  action's logging touches, which immediately breaks the
  no-secrets-in-cache invariant.

---

## Sketch of the extension

### 1. `store_secret` capability on secret-provider plugins

Mirror of `resolve_secret`. Plugins that declare `store_secret`
accept:

```json
{
  "method": "store_secret",
  "secret_ref": "pass:loosh/registry.loosh.cloud/admin",
  "value":      "<bytes>",
  "mode":       "create_if_absent" | "overwrite" | "create"
}
```

`pass` backend: `pass insert -m` (with stdin), guarded by `pass show`
when mode is `create_if_absent`. Other backends (1Password, Vault,
SSM Parameter Store) implement the same shape against their native
APIs. `create_if_absent` is the load-bearing mode for declarative
bootstrapping; `overwrite` is the load-bearing mode for capture.

### 2. `sealed_outputs` on targets

Symmetric to `sealed_inputs`:

```cue
sealed_outputs: ADMIN_PASS: "pass:loosh/registry.loosh.cloud/admin"
```

Runner contract:

- Action receives a side channel for emitting named values. Probably
  a directory at `$MU_SEALED_OUT_DIR` where the action writes
  `ADMIN_PASS` as a file, or a designated fd. Both keep the value
  out of stdout/stderr (which the cache and observe see).
- After the action exits successfully, runner reads each declared
  name, calls the configured provider's `store_secret` with the
  ref, and zeroes the temp.
- **Cache key hashes the ref, not the value.** Same invariant as
  `sealed_inputs`. Re-running an action that emits a fresh value
  must not surface that value into the cache layer.
- **Observe redacts.** The observe plane never reads sealed outputs.

This decouples "the action ran" from "the value it produced" —
exactly the property `sealed_inputs` already gives the read side.

### 3. Create-if-absent as a first-class target kind

Once `store_secret` exists, the bootstrapping flow becomes
declarative. A small upstream toolchain (call it `secret-gen` for
now) takes:

```cue
target: "//secrets/admin-pass"
toolchain: "secret-gen"
config: {
    ref:        "pass:loosh/registry.loosh.cloud/admin"
    derivation: ["openssl", "rand", "-base64", "24"]
    mode:       "create_if_absent"
}
```

Plan emits one action: run the derivation, capture stdout into the
sealed-output channel, store via the configured provider. Downstream
targets reference the ref through `sealed_inputs` as usual and depend
on `//secrets/admin-pass` to ensure existence.

This eats the "somebody has to pass insert it first" footgun without
inventing a new namespace.

### 4. Delivery modes for `sealed_inputs`

`pass:raw:` is the small half of the same lesson. Multi-line secrets
(SSH keys, service-account JSON blobs, x509 certs) don't naturally
want to be env vars — they ride in env vars today because that's the
only delivery shape `sealed_inputs` knows. The remote-exec plugin
already does `printf %q` gymnastics to forward env vars through
`ssh + sudo --preserve-env`; for a multi-KB key it works but is
ugly, and on some hops it leaks the value into `ps`/`/proc/<pid>/environ`.

A delivery-mode hint on the consumer side:

```cue
sealed_inputs: SSH_KEY: {ref: "pass:raw:loosh/void.loosh.cloud", mode: "file"}
```

Modes:

- `env` (default, current behavior): exported as `$NAME`.
- `file`: written to a temp path under the action sandbox, the path
  is exported as `$NAME`. Sandbox cleans up post-action.
- `stdin`: piped into the action's stdin. Useful for `pass insert -m`
  -style consumers and for `ssh-add -` (which is already what the
  loosh `//void/load-key` target hand-rolls).

The protocol stays backwards-compatible: bare-string refs default to
`env` mode.

---

## Cache, observe, and security invariants

These have to be nailed down before any plugin work, because the
whole point of the sealed-* mechanism is that the cache and observe
planes never see values.

- **Cache key derivation.** Both `sealed_inputs` and `sealed_outputs`
  contribute *refs* (and modes) to the cache key, never values.
- **Action cache contents.** Stored stdout/stderr must never include
  sealed-output values. The sealed-output side channel is mandatory
  because freeform stdout capture cannot enforce this.
- **Observe surface.** Observe must redact: it can report "ref
  ADMIN_PASS exists at provider pass" but never the value.
- **Logs.** Same redaction rules as today's `sealed_inputs` — value
  bytes never appear in logs, period.
- **Provider trust boundary.** `store_secret` is privileged: a
  compromised plugin could overwrite production credentials. Worth
  considering an allow-list of writable refs per project, separate
  from the readable surface.

---

## Sequencing

1. **Upstream `pass:raw:`** (small, additive, unblocks loosh today).
   No protocol change required; only the ref-grammar comment in the
   pass plugin and a version bump to v0.2.0.
2. **Protocol increment for `store_secret` + `sealed_outputs`.**
   Spec the side-channel mechanism, the cache/observe redaction
   rules, and the writable-ref allow-list before writing plugin code.
3. **Implement `pass.store_secret` and runner support for
   `sealed_outputs`.** Adds the capture half. Test against the
   `//zot/htpasswd` flow: have an action emit the admin pass into a
   sealed output rather than reading a pre-seeded one.
4. **Delivery-mode hints on `sealed_inputs`.** Smallest of the four
   in scope; can ship independently of (2)/(3) once the protocol
   change is agreed.
5. **`secret-gen` toolchain for create-if-absent.** Sits on top of
   (2)/(3). Closes the bootstrapping loop declaratively.

---

## Implementation notes (landed)

**Phase 1 — symmetric provider API.** Protocol gained `store_secret`
as a fifth method alongside `discover|plan|observe|resolve_secret`,
with `secret_value` and `secret_mode` fields on `Request` and a
`StoreSecretResponse` envelope. `Process.StoreSecret`,
`Manager.StoreSecret`, and the `scenario` test runner all gained
symmetric write paths. The pass plugin (`plugins/pass/plugin.bb`)
bumped to v0.3.0: upstreamed `pass:raw:` from loosh and added
`store_secret` via `pass insert -m -f`, honoring `create`,
`overwrite`, and `create_if_absent` modes.

**Phase 2 — sealed_outputs.** Targets and actions both grew a
`SealedOutputs` field (env-name → secret-ref). The CUE schema
accepts `sealed_outputs?` at the target level. Single-action
targets get target-level outputs auto-applied; multi-action plans
must route them explicitly via the plugin (the coordinator errors
out otherwise rather than guessing). The executor creates a
per-action `$MU_SEALED_OUT_DIR`, exposes its path in env, reads
each declared name as a file post-exec, and routes through
`Executor.SealedOutputWriter` (a closure backed by a transient
provider-only `plugin.Manager` started by the coordinator at
execute time). The temp dir is cleaned up unconditionally; values
are zeroed after routing. Actions with non-empty `SealedOutputs`
are forced impure by `Resolve` so cache hits never skip the
store_secret side-effect. The action cache key includes
sealed-output destination *refs* (not values), so changing a
destination invalidates the cache.

**Phase 3 — `secret-gen` toolchain.** A built-in toolchain (no
plugin process) that declares one action: run a derivation argv,
redirect stdout into the per-action `$MU_SEALED_OUT_DIR/VALUE`,
let the executor route it to the configured provider via
`store_secret`. Default mode is `create_if_absent` so re-running
the build is a cheap no-op once the secret exists. `keep_trailing_newline`
controls whether `echo`-style output gets its trailing `\n` stripped
before storing (default true: strip — `openssl rand`, `uuidgen`, etc.
all need this). To support per-output modes the protocol grew
`SealedOutputModes` on `ActionSpec`/`Action`, the `SealedOutputWriter`
signature picked up a `mode` parameter, and the coordinator-side
writer closure forwards it through to `mgr.StoreSecret`. Empty mode
means `overwrite` (the previous behavior). Wired into the coordinator
dispatch alongside `shell`; CUE schema validation is skipped for
`secret-gen` targets (the Go-side validator in `SecretGenPlan` is
the source of truth).

Example:

```cue
target: "//secrets/admin-pass"
toolchain: "secret-gen"
config: {
    ref:        "pass:registry/admin"
    derivation: ["openssl", "rand", "-base64", "24"]
    // mode defaults to "create_if_absent"
}
```

Downstream targets can now `sealed_inputs: ADMIN_PASS: "pass:registry/admin"`
and add `deps: ["//secrets/admin-pass"]` to declaratively bootstrap the
credential.

**Phase 4 — sealed-input delivery modes.** Added a parallel
`sealed_input_modes: {[name]: "env" | "file"}` map at target,
`TargetInfo`, `ActionSpec`, and `dag.Action` levels (symmetric with
`sealed_output_modes`). The CUE schema accepts the new field. The
executor's secret-injection path branches per-name: `env` (default)
exports the value as `$NAME` exactly as before; `file` writes the
value to a 0600 temp file under a per-action `mu-sealed-in-*` dir
(mode 0700) and exports the path as `$NAME`. The dir is removed
unconditionally on action completion. Sandboxed (toolchain) actions
reject `mode=file` because the temp file lives outside the sandbox's
view. Cache key now hashes the sealed-input *refs* and *modes* (not
values) — changing either invalidates the cache, which previously
it didn't. `stdin` mode was scoped out of v1: the load-bearing case
(multi-line keys, kubeconfigs) is covered by `file`, and `stdin`
would require a sandbox plumbing change for parity. Full user guide
at [`docs/sealed-input-delivery-modes.md`](../sealed-input-delivery-modes.md).

**Phase 5 — writable-ref allow-list.** A new top-level `secrets`
block in `mu.cue` (`#SecretsConfig` in the schema) carrying an
optional `writable_refs: [...string]` allow-list. Patterns use Go's
`path.Match` semantics (`*` matches a single path segment; literal
text otherwise). Three states: omitted = no allow-list (back-compat,
writes unrestricted); set with patterns = strict allow-list; set to
`[]` = explicit deny-all. Enforcement happens twice: once at plan
time, walking every `sealed_output` ref in the graph and aborting
before the provider manager starts; and again at write time inside
the `SealedOutputWriter` closure as defense in depth. The policy is
shared across all schemes — `pass` and a hypothetical future
`vault` would draw from the same list. Read operations
(`sealed_inputs` / `resolve_secret`) are unaffected; the policy
gates writes only. Full user guide at
[`docs/secrets-write-policy.md`](../secrets-write-policy.md).

**Deferred to future phases.**
- `stdin` delivery mode for `sealed_inputs` (would require sandbox
  plumbing; `file` mode covers the load-bearing cases).
- `revoke_secret` capability (currently `overwrite` with empty
  string is the closest analogue; pass has `pass rm` available
  natively).
- Per-plugin / per-scheme write-policy scoping. The current
  allow-list is global across all schemes.
- Re-running cached actions does not re-store secrets — by design
  via forced impurity, but if a future user wants cached + restored
  semantics, the executor would need to short-circuit the cache
  hit only when the destination ref still exists at the provider.

## Open questions

- Should `sealed_outputs` be allowed on any toolchain, or only on
  `shell` and a designated `secret-gen`? The latter is safer; the
  former is more honest about what's happening.
- For `create_if_absent`, who owns the read-before-write race? The
  plugin (atomic check-and-insert against the backend, where
  possible) or the runner (single-flight per ref)? Probably both:
  runner serializes per-ref, plugin still does the existence check.
- Do we want a separate `revoke_secret` capability, or is overwrite
  with empty/sentinel sufficient? Most backends distinguish "delete"
  from "set to empty"; pass does (`pass rm`), so probably yes.
- How does this interact with the cross-project source-input work
  in `2026-04-25-external-source-inputs.md`? If project A produces
  a secret meant for project B, does B reference it through the
  same ref grammar, or do we need a project-scoped namespace?
