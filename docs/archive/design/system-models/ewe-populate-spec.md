# Spec: the ewe-populate path (`#EweTarget` → catalog)

The **integration-layer** spec for ewe-driven populate: defines `#EweTarget`,
wires its output into the catalog, and ties the four ewe primitive specs into one
end-to-end flow. Targets the **pudl** + **mu** repos. Every claim grounded in
current source (verified 2026-06-22).

**Relationship to the other specs.** The ewe *internals* are already specced —
this doc does **not** re-derive them:
- [`ewe-arg-resolution-spec.md`](ewe-arg-resolution-spec.md) — the engine fix
  (nested-ref args resolve).
- [`ewe-secrets-spec.md`](ewe-secrets-spec.md) — secrets are refs in CUE, revealed
  only in Go sinks.
- [`ewe-http-pagination-spec.md`](ewe-http-pagination-spec.md) — `#HttpAll` fetch +
  paging.
- [`ewe-body-kind-spec.md`](ewe-body-kind-spec.md) — how an ewe program becomes a
  mu action (the `ewe` body kind), emitting an output file.

Those stop at **mu's output** (a records file in `$MU_OUT`). This spec covers the
two pieces they leave open: the **`#EweTarget` schema** (undefined today, like
`#PluginPlan` was) and the **pudl ingest seam** (records file → catalog observe
entries → drift's live side).

---

## 1. The end-to-end path

```
#SystemModel.populate: #EweTarget                                   (a model field)
  └─ compiles to a mu `ewe`-body action            (ewe-body-kind-spec)
       └─ `mu build //models/<model>` runs it:
            ewe program does HTTP (ewe-http-pagination-spec),
            reveals secrets in-sink (ewe-secrets-spec),
            writes <output>.json = [ _schema-tagged records ]   (records ARRAY)
            └─ pudl ingests <output>.json into the catalog        (§3 — the seam)
                 └─ stored as OBSERVE ENTRIES, routed by `_schema`
                      └─ drift reads GetLatestObserve(<def>)       (checker.go:72)
                           — the live side, populate-kind-agnostic
```

The output of an ewe populate is the **same unit** as a plugin observe: an array of
`_schema`-tagged records. Only the transport differs (a build-produced file vs a
`mu observe` result). §3 normalizes that difference away.

---

## 2. `#EweTarget` — the schema

Undefined in CUE today (referenced at `README.md:93`, used in
`examples/04-dns-zone/model.cue:18`). Define it (fields grounded in the examples +
the body-kind spec's action shape):

```cue
#EweTarget: {
    eweSource:           string              // project-relative path to the ewe .cue program
    outputs:             [...string]         // files the program writes to $MU_OUT (records JSON)
    network?:            bool | *false       // program performs network I/O
    impure?:             bool | *false       // skip CAS cache (live fetch is non-hermetic)
    sealed_inputs?:      {[string]: string}  // NAME -> secret ref (e.g. "pass:dns/cloudflare")
    sealed_input_modes?: {[string]: string}  // NAME -> "env" | "file"
}
```

This is the populate-arm-level declaration; it **compiles to** the body-kind spec's
`ewe` action (`eweSource`/`outputs`/`network`/`impure`/`sealed_inputs` map 1:1 to
that action's fields, `ewe-body-kind-spec.md:60-72`). `#EweTarget` is the
`#SystemModel`-facing name for that action shape.

**Convention:** the program's emitted records **must** self-tag with a **quoted**
`"_schema"` label (the `"pudl/<module>.#<Def>"` reference form, ledger D4). That
tag is what routes each record to its catalog schema at ingest (§3). Records
without `"_schema"` are an authoring error (a populate program that can't be
bound).

> **The quote is load-bearing (verified building this).** A bare `_schema:` is a
> *hidden* CUE field, and both CUE's `json.Marshal` and ewe's value conversion
> drop hidden fields — so a bare tag silently disappears from the records file and
> every record falls back to the generic `pudl/mu.#ObserveResult` schema. Write
> `"_schema": "git.repository.gitlab"` (a normal string field whose name starts
> with `_`). Equivalently, never `json.Marshal` records *in CUE*; pass them
> structured to `#WriteFile` and let the Go sink marshal them.

---

## 3. The ingest seam (the new pudl work)

**Problem.** `mu build` writes the ewe output as a **raw records array**
(`<output>.json` = `[{_schema, …}, …]`, per `ewe-body-kind-spec.md:55-66`). The
shipped catalog ingester `IngestObserveResults` expects a **`[]ObserveResult`** —
`{target, current{records:[…]}}` (`pudl/internal/mubridge/ingest.go:28-34,59-62`).
Shapes don't match; the array can't be ingested as-is.

**Decision — wrap and reuse, do not add a parallel ingester.** `pudl run`, after
the populate action completes, reads each declared `outputs` file and wraps it in
the observe envelope:

```
ObserveResult{ Target: "//models/<model>", Current: { records: <the array> } }
```

then feeds the existing `IngestObserveResults` (`ingest.go:44`). That ingester
already: snapshots the run, stores each record as an **observe entry**, and routes
by `_schema` (`ingest.go:36-41,117`). **Zero new ingest/routing/snapshot logic** —
the only new code is read-file + wrap + call.

**Why this is the right seam:**
- **Drift becomes populate-kind-agnostic.** ewe-target records and
  `#PluginObserve` records land as the *same* observe-entry type, so
  `drift.Check`'s `GetLatestObserve(def)` read (`checker.go:72`) is identical for
  both. Drift, checks, and the convergence loop never branch on populate kind.
- **Reuses the shipped `_schema` routing + binding** (ledger D4 exact-match,
  `inference/heuristics.go`), not a second copy.
- A parallel "ingest-records" path would duplicate snapshot + routing and risk the
  two populate kinds drifting apart in catalog shape.

**Where it runs:** inside the `pudl run` orchestrator (the populate phase), not a
user-invoked command — consistent with `pudl run` owning the loop. (A standalone
`pudl mu ingest-records --schema-from-field` could be factored out later if a
non-`pudl run` caller needs it; not in V1.)

---

## 4. Relationship to `#PluginObserve`

Both are populate **kinds** that land catalog observe entries; the union is
`populate: #EweTarget | #PluginObserve` (`README.md:93`).

| | `#EweTarget` | `#PluginObserve` |
|---|---|---|
| What | custom fetch program (the GitLab/DNS case) | reuse a shipped observer plugin (the Proxmox/host case) |
| Runs via | `mu build` + an `ewe` body action | the plugin's `observe` op (`mu observe`) |
| Output transport | records file in `$MU_OUT` → wrapped (§3) | `mu observe --json` → `[]ObserveResult` |
| Catalog result | **identical** — `_schema`-routed observe entries | **identical** |

The point of §3's wrap-and-reuse is to make the last row literally true: after
ingest, **the catalog cannot tell which kind produced a record**, so everything
downstream (drift, checks, convergence) is written once.

---

## 5. What ships vs what's new

**Ships (reuse):**
- `IngestObserveResults` — snapshot + per-record observe entry + `_schema` routing
  (`pudl/internal/mubridge/ingest.go:36`).
- drift's live-side read `GetLatestObserve` (`pudl/internal/drift/checker.go:72`).
- the `_schema` exact-match binding (ledger D4).
- mu's output path — `$MU_OUT` → `WorkDir`, hashed/stored
  (`ewe-body-kind-spec.md:150`).

**New (must be built):**
- **The ewe HTTP/secret primitives** — `#HttpAll`, `#Secret`, the `ewe` body kind,
  the arg-resolution engine fix. These are specced (the four docs above) but
  **unbuilt in ewe** (ewe has zero HTTP / `EweTarget` / `auth.bearer` code today —
  the review's F4). This is the bulk of the effort and lands in the **ewe** + **mu**
  repos.
- **`#EweTarget` CUE def** (§2).
- **The pudl-run ingest seam** — read `outputs`, wrap as `ObserveResult`, call
  `IngestObserveResults` (§3). Small.

**Build order:** the ewe primitives (per the four specs) are the prerequisite; the
`#EweTarget` def + ingest seam are thin glue on top. ewe-populate is itself a
**prerequisite for the observe-only `pudl run`** ([`V1-BUILD-SPEC.md`](V1-BUILD-SPEC.md) §2),
which in turn precedes the convergence loop.

---

## 6. Done when

A `#SystemModel` instance with a `populate: #EweTarget` arm, run via
`pudl run <model>`:
1. executes the ewe program over `mu build` (HTTP fetch + paging, secrets revealed
   only in-sink),
2. emits a `_schema`-tagged records file,
3. has those records ingested as observe entries indistinguishable from a
   `#PluginObserve` run, and
4. a subsequent `drift check` reads them as the live side and reports drift vs
   `desired` — with **no populate-kind-specific code anywhere downstream**.
