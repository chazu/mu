# Handoff — ewe-populate BUILD session (fresh start)

This is the kickoff for building **ewe-populate**: custom HTTP-fetch observers for
`#SystemModel` (the examples that fetch a SaaS API rather than use a plugin —
TLS certs / DNS / GitLab, examples 3/4/5). It is the largest remaining V1 piece:
**~3 repos** (ewe, mu, pudl), fully **designed** — your job is to build it.

## Read first (the design is done — do not re-derive)

In order:
1. [`ewe-populate-spec.md`](ewe-populate-spec.md) — the **end-to-end path**
   (`#EweTarget` → mu observe → ingest seam). Start here; it frames the rest.
2. [`ewe-arg-resolution-spec.md`](ewe-arg-resolution-spec.md) — the ewe **engine fix**
   (nested-ref args resolve). **Prerequisite for everything else** (CRIT-1/2).
3. [`ewe-secrets-spec.md`](ewe-secrets-spec.md) — `#Secret`: secrets are refs in
   CUE, revealed only inside Go sinks (fail-closed).
4. [`ewe-http-pagination-spec.md`](ewe-http-pagination-spec.md) — `#HttpAll`: HTTP
   fetch + a closed set of paging strategies (all Go-side).
5. [`ewe-body-kind-spec.md`](ewe-body-kind-spec.md) — the `ewe` action body kind in
   **mu** (how an ewe program becomes a mu action, emits a records file).

Context: [`V1-BUILD-SPEC.md`](V1-BUILD-SPEC.md) (canonical) + [`issue-ledger.md`](issue-ledger.md)
(rationale). The **observe/converge loop that consumes this is already built** in
pudl — see `pudl/docs/system-models-build-status.md`.

## Scope — what ewe is and is NOT for

- **ewe = custom HTTP-fetch observers where no plugin exists.** It produces a
  records file that pudl ingests exactly like a plugin observer's output, so the
  whole downstream (drift / checks / converge) is **unchanged** — populate is
  populate-kind-agnostic after ingest (ewe-populate-spec §3/§4).
- **NOT needed where a plugin exists.** k8s / host / aws / docker observers use
  **zero ewe**. The alternative to ewe for any one system is "write a plugin"
  (bespoke Go). ewe wins when you'd otherwise write N per-API plugins (declarative
  CUE vs Go). This is the recorded DNS disposition (issue-ledger).

## Repos & current state (verified)

- **ewe** (`/Users/chazu/dev/go/ewe`, on `main`, clean): a CUE AST-rewriter for
  registered pure functions. **Zero HTTP, zero `EweTarget`/`HttpAll`/`auth.bearer`,
  zero `.cue` files** — confirmed greenfield. `Function.Execute` is the extension
  point (`ewe/function.go:51`); only `StringFunc`/`StringsFunc`/`MathFunc`
  constructors exist today.
- **mu** (`/Users/chazu/dev/go/mu`, on `main`): has **one** HTTP primitive — pith's
  `http/request`, a single request (`internal/pithvm/register.go:414`), no
  pagination. `#HttpAll` paging is greenfield Go in the mu sink layer.
- **pudl** (`/Users/chazu/dev/go/pudl`, on `main`): the consumer. `IngestObserveResults`
  + the `pudl run` populate path already exist; the ewe seam is "wrap the ewe
  output file as an `ObserveResult`, reuse the shipped ingest" (ewe-populate-spec §3).

## Build order (bottom-up; each step validatable)

1. **ewe engine fix** (arg-resolution) — ewe repo. Unblocks nested-ref args.
   Validate: ewe unit tests (the spec names the real `extractArgsWithFallback`
   defect + cases).
2. **`#Secret` + taint** — ewe (`#Secretf` sugar + fail-closed guard) + mu sink.
   Validate: the spec's probe table (CUE rejects struct-into-string interpolation;
   plaintext-in-source caught).
3. **`#HttpAll`** — mu sink. **Fully validatable with no external infra**: test the
   fetch + each paging `style` against a local `httptest.Server`.
4. **`ewe` body kind** — mu (`internal/dag`, `coordinator`, `internal/cas`). The
   seam where an ewe program runs as a mu action and writes its records file.
   Validate: a mu build of a tiny ewe target emits the expected output file.
5. **`#EweTarget` + pudl ingest seam** — thin. `#EweTarget` CUE def in
   `pudl/internal/systemmodel/schema.cue` (the union already lists it; struct
   fields exist in `Populate`); wire `pudl run` populate to run the ewe target and
   wrap its output as an `ObserveResult` → `IngestObserveResults`. Validate: a
   `pudl run <ewe-model>` against a local `httptest` API end-to-end.

## Working method (match it — the reviews punished assertion)

- **Ground every claim in real source before asserting.** Run greps/throwaway
  tests against actual ewe/mu/pudl code. This whole effort caught design errors
  (mu reads mu.cue not mu.json; k8s observe is differential; `_schema` is a hidden
  CUE field) only by probing real source/binaries. Do the same.
- **One issue at a time**: rundown + tradeoffs + your lean, then build; commit at
  checkpoints; branch off main (don't commit to main). The `bd` git hooks in pudl
  are removed; mu/ewe hooks unaffected.
- **Default to cut/defer** when there's no concrete consumer.
- **Validate before claiming done** — for ewe, `httptest` gives you real HTTP with
  zero external infra; use it.
- Commit trailers (see recent commits): `Co-Authored-By: Claude Opus 4.8 (1M context)`
  + `Claude-Session:` line.

## First move

Read `ewe-populate-spec.md` + `ewe-arg-resolution-spec.md`, then **ground the ewe
engine** (`/Users/chazu/dev/go/ewe`): read `function.go` + the arg-resolution path
the spec names, confirm the defect, and reproduce it with a throwaway test before
fixing. That bounds step 1.
