# Spec: host convergence (complete the `host` plugin's `plan` op)

The **declarative-host** plugin the convergence examples need (example 1) is **not
a new plugin** — it is the existing `host` plugin's stub `plan` op, completed.
Targets the **mu** repo (`plugins/host`). Grounded in current source (verified
2026-06-22).

## Why this, not a new plugin

`host` already **observes** a remote box over SSH (OS/packages/services/users/files
via `gather.sh`, `plugins/host/main.go:81`) and already registers a `Plan` handler
— but it is a **stub** that returns no actions (`main.go:71-78`: *"required by the
SDK but this plugin produces no build actions — it is purely an observer"*).

Completing that `plan` makes `host` a **declarative config-manager**: observe +
converge in one plugin, exactly like `k8s` (which does both). The same plugin
example 1 already uses for `populate` becomes its `converge` plugin — no second
plugin, no second SSH path.

**Example 1 fix:** its `converge: #PluginPlan & {plugin: "remote-exec"}`
(`examples/01-remote-server/model.cue:51`) is mis-specced — `remote-exec` is
imperative (needs a literal `config.command`, `plugins/remote-exec/main.go:71`) and
cannot reconcile declarative `desired`. Change it to `plugin: "host"`.

## How it fits the apply path (§5.5 of the build spec)

The apply-path decision stands: **the plugin owns translation; pudl routes `desired`
to it as sources.** So:

1. `pudl run` renders the instance's `desired` linux/fs records (filtered by
   `--only`) to a generated sources file and sets `Target.Sources`
   ([`V1-BUILD-SPEC.md`](V1-BUILD-SPEC.md) §5.5).
2. `host.plan` reads those desired records from `Sources`, and emits **one
   idempotent SSH action per desired resource**, keyed by the record's `_schema`.
3. `mu build` runs the actions over SSH (reusing `sshBaseCmd`,
   `plugins/host/main.go:135`); `ingest-manifest` records exit codes; the loop
   re-observes (`host.observe`) and re-drifts to confirm.

`host.plan` computes **no cross-resource diff** — it ensures each desired resource,
relying on (a) command idempotency and (b) a per-resource **guard** (the
`remote-exec` `check` idiom: `dpkg -l X || apt-get install -y X`). The loop's
re-observe→drift is the verifier; the plugin stays a pure desired→commands
translator.

## The per-`_schema` handler set (the new code)

A closed registry: `_schema` → a function emitting an idempotent, guarded SSH
command. V1 covers the four resource types example 1 declares:

| `_schema` | Desired fields | Emitted (guarded) command |
|-----------|----------------|---------------------------|
| `pudl/linux.#Package` | `name`, `state: "present"\|"absent"` | present → `dpkg -l <name> \|\| apt-get install -y <name>`; absent → `dpkg -l <name> && apt-get remove -y <name>` |
| `pudl/linux.#User` | `name`, `shell`, … | `id <name> \|\| useradd --shell <shell> <name>` |
| `pudl/fs.#File` | `path`, `mode`, `content` | write `content` via here-doc to `path`, then `chmod <mode>` (overwrite is idempotent) |
| `pudl/linux.#Service` | `name`, `state: "running"`, `enabled: true` | `systemctl enable --now <name>` (idempotent) |

**New resource type = add a handler, not a DSL** — same closed-set philosophy as
`#HttpAll` paging styles (`ewe-http-pagination-spec.md`). Each handler emits a
`muplugin.ActionSpec` whose `Command` is `["bash","-c", <guarded-script>]`, mirroring
how `remote-exec.plan` builds its SSH wrapper (`plugins/remote-exec/main.go`).

The `_schema` form is the `"pudl/<module>.#<Def>"` reference already used by the
desired records (ledger D4; `examples/01-remote-server/model.cue:35-39`). A desired
record whose `_schema` has no handler is a hard plan error (don't silently skip a
resource the author asked to converge).

## Deletion / pruning — OUT of V1

`desired ∖ actual = ensure-present` is V1 (the table above). `actual ∖ desired =
delete` (prune a package/user present on the host but absent from `desired`) is
**deferred**:
- Auto-deleting on a live host is dangerous (a desired list rarely enumerates every
  legitimate system package; prune would remove them). Consistent with the
  cut-rollback caution — V1 does not auto-destroy live state.
- `k8s` makes `prune` opt-in and off by default (`plugins/k8s/plugin.bb:55`); host
  follows, but even the opt-in is post-V1.

So V1 host convergence is **additive/ensure-state**, never removal. The DNS
"textbook set-difference" (which *does* delete) is a different, API-backed plugin,
not host. Record this scope explicitly so it isn't mistaken for full set-difference.

## What ships vs what's new

**Ships (reuse):**
- `host.observe` + `gather.sh` — the observe half (`plugins/host/main.go:81`).
- `sshBaseCmd` — the SSH command vector incl. `SSH_PASS`/sshpass
  (`plugins/host/main.go:135`).
- the `remote-exec` `check`-guard pattern (`plugins/remote-exec`).
- the mu `Plan` protocol + action execution + `ingest-manifest` recording.

**New (must be built):**
- the four per-`_schema` handlers (above) inside `host.plan`, replacing the stub
  (`plugins/host/main.go:71-78`).
- reading `desired` from `Target.Sources` in `host.plan`.
- a `prune` config flag stub, defaulting off and unimplemented (documents the
  deferral; no delete logic in V1).

## Done when

Example 1 (`odroid`), run via `pudl run odroid --converge`:
1. `host.plan` reads the desired `linux.#Package`/`#User`/`#Service` + `fs.#File`
   records from sources and emits guarded SSH actions,
2. `mu build` applies them over SSH; already-satisfied resources are skipped by
   their guards,
3. the loop re-observes via `host.observe` and a subsequent drift check reports ∅,
4. no resource is ever deleted (prune is out of V1).
