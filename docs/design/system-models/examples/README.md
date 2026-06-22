# System Model examples — under the current (post-review) design

Each of the five examples from [../archive/examples.md](../archive/examples.md),
re-expressed as it would actually look after the issue-ledger resolutions. These
supersede the inline sketches in the archived `examples.md`, which predate the
design corrections.

## What changed from the archived `examples.md`

| Original sketch | Current design | Ledger |
|---|---|---|
| inline `ewe: { … }` block in the plan | **populator is a program file** (`populate.cue`), content-addressed; the action carries `eweSource` | I1 |
| effects inside a `for` comprehension (ex. 3, 5) | **batch effect + pure build/join** — `#HttpBatch` / `#ExecBatch`, no effect in a comprehension | E2 |
| `"Bearer \(_tok.result)"` (ex. 4) | **`auth:` block carrying a ref** — secret revealed only in the Go sink | S1 |
| `headers: { "PRIVATE-TOKEN": _t.result }` (ex. 5) | **`auth.header` ref** | S1 |
| inline `op.#Plugin & {args:[{op:"observe"}]}` (ex. 1, 2) | **`#PluginObserve` populate kind** — the plugin's own observe, consumed by dataflow | P2 |
| `paginate: { until: "empty" }` | **`paginate: { style: "page" \| "link" \| "cursor", … }`** | I2 |
| `schema: ["host.package", …]` (dotted resource-type strings) | **`schema: [linux.#Package, …]`** — definition references; records self-tag with a `_schema` def-ref (`"pudl/linux.#Package"`) | D3, D4 |

## Schemas (D1–D4)

`#SystemModel` lives in **pudl**; every record is an instance of a **pudl schema
definition**. Models name schema *definitions*, never dotted `resource_type`
strings (which are an internal derived handle, not an author concept):

- **Reused** schemas are shipped by pudl — `pudl/linux.#Package`, `pudl/fs.#File`,
  `pudl/git.#GitLabRepository` (examples 1, 5).
- **Introduced** schemas (`tls`, `dns`, `k8s`, `gitgov`) are **model-shipped** in a
  pudl schema package — repo-scoped `.pudl/` or global `~/.pudl` (examples 2, 3, 4,
  and ex. 5's branch-protection). The example `import "tls"` etc. stand in for those
  shipped packages.
- A populator record self-identifies with a `_schema` definition reference
  (`_schema: "pudl/linux.#Package"`), bound *exactly* at ingest — one populator may
  emit several schemas into one output, so each record carries its own.

## v1 scope convention used here

v1 is **observe-only**. So in each example:

- **Real in v1:** `populate` (ewe program or `#PluginObserve`) → ingest → `relations`
  / `checks` (observe + flag) → report.
- **V1 (design-resolved, not yet built):** `desired` + `converge` and the reconcile
  *loop*. Where an example converges (1, 3, 4) the converge arm is flagged
  `# V1-OPEN` — the design is settled (ledger V1; `pudl run --converge`, drift==∅
  + max-iter cap, `#PluginPlan` converge path), the code is pending. One-shot
  drift-vs-`desired` *flagging* is feasible today (`pudl drift check`); the
  iterate-to-fixed-point loop is the V1 build.

## Layout

```
NN-name/
  README.md       — goal, fixed point, v1-vs-deferred, what pudl run does
  model.cue       — the #SystemModel declaration (illustrative; schema not final)
  populate.cue    — the ewe populator program (ewe-populated examples only: 3,4,5)
  rules/*.cue     — Datalog relations (illustrative stubs)
```

`#SystemModel` and the `op.#*` function shapes are illustrative — the schema's home
(mu / pudl / shared module) is still open, and CUE here is for reading, not
compilation.

| # | Model | Fixed point | Populate kind | Showcases |
|---|---|---|---|---|
| 1 | [Remote server](01-remote-server/) | convergence (V1-open) | `#PluginObserve` (host) | plugin observe; desired/drift |
| 2 | [k8s policy](02-k8s-policy/) | observation | `#PluginObserve` (k8s) | Datalog policy; observe + flag |
| 3 | [TLS certs](03-tls-certs/) | convergence (V1-open) | ewe (`#ExecBatch`) | exec fan-out without comprehension effects |
| 4 | [DNS zone](04-dns-zone/) | convergence (V1-open) | ewe (`#HttpAll`) | `auth.bearer` ref; cursor-free paging |
| 5 | [Repo governance](05-repo-governance/) | observation | ewe (`#HttpAll`+`#HttpBatch`) | the fan-out showcase: batch + join |
