# Spec: `#HttpAll` pagination vocabulary

Resolves ledger **I2** (folds review finding **m3**). Targets the **mu** sink
layer (the `#HttpAll` ewe function). Companion to
[ewe-body-kind-spec.md](ewe-body-kind-spec.md) (where sinks are registered) and
[ewe-secrets-spec.md](ewe-secrets-spec.md) (auth/secret resolution inside the sink).

## The constraint

ewe cannot drive an unbounded loop from CUE — that is the entire reason a
paginating fetch exists as a single Go effect rather than a CUE comprehension over
pages. So **the paging loop is 100% Go, inside the `#HttpAll` sink**; CUE only
declares *which* strategy and *where its signals live*. Review finding m3 is
correct: every paging strategy is a Go feature, not a config knob — config selects
and wires a fixed set of Go-implemented strategies.

**Grounding (verified 2026-06-20):** mu has exactly one HTTP primitive today —
pith's `http/request`, a single request (`internal/pithvm/register.go:414`). There
is **no pagination anywhere**. `#HttpAll` paging is greenfield Go.

## Design: fixed strategy enum, parameterized by signal location

A discriminated union by `style`. Four strategies cover the observe-only API
universe (GitLab, GitHub, Stripe-likes, Slack, Cloudflare). **Not** a general
paginator DSL — paging shapes are a closed, well-known set; a config mini-language
would carry the maintenance weight the project keeps rejecting (the Glojure-removal
precedent). A new API shape means adding a `style` and one Go function, not
extending a DSL.

```cue
#HttpAll: { args: [{
    url:     string
    method?: "GET"
    query?:  { [string]: _ }
    headers?:{ [string]: _ }      // secret refs resolved in-sink (secrets spec)
    auth?:   #Auth                // bearer/header/query/basic (secrets spec)

    paginate: {
        style: "none" | "page" | "link" | "cursor"

        // --- style: "page" --- increment a query param until a stop condition
        param?:     string        // page-number param, e.g. "page"
        start?:     int | *1
        sizeParam?: string        // page-size param, e.g. "per_page" (enables "partial")
        stop?:      "empty" | *"empty" | "partial"  // empty page, or page < size

        // --- style: "cursor" --- read a next-token from the body, feed to a param
        cursorParam?: string      // request param the token goes in, e.g. "starting_after"
        nextPath?:    string      // body path holding the next token, e.g. "meta.next_cursor"
        hasMorePath?: string      // optional boolean gate, e.g. "has_more" (Stripe)
        // stop = nextPath absent/null/"" OR hasMorePath == false

        // --- style: "link" --- follow RFC 5988 `Link: <url>; rel="next"`.
        //     Self-describing; needs no fields beyond style. Stop = no rel="next".

        // --- all styles ---
        itemsPath?: string        // where items live in the body; default: body IS the list
        maxPages?:  int | *1000   // SAFETY CAP (see below) — enforced default
    }
}] }
// result: the concatenated item list across all pages — one flat [...] for a
// CUE comprehension to reshape. Loop, item extraction, and stop-detection are Go.
```

### Strategy semantics (Go)

| `style` | next request | stop condition | who uses it |
|---------|--------------|----------------|-------------|
| `none`  | — (single request) | always | non-paginated endpoints |
| `page`  | `param := start; param++` each round | empty page, or (with `sizeParam`) a page smaller than the size | GitLab, generic REST |
| `link`  | the URL in `Link: …; rel="next"` | no `rel="next"` header | GitHub REST, GitLab |
| `cursor`| set `cursorParam` to the token at `nextPath` | `nextPath` absent/null/empty, or `hasMorePath == false` | Stripe, Slack, GraphQL |

`itemsPath` (a dotted body path, reusing the `#GetPath` accessor from arg-spec
Tier 3) extracts the per-page item list before concatenation; default = the whole
response body is the list.

## The safety cap (`maxPages`)

`maxPages` has an **enforced default of 1000** and is non-optional in effect: a
buggy cursor that never reports `absent`, or a server that always returns a
`rel="next"`, is an **infinite live-HTTP spend loop** — strictly worse than a CPU
loop because it is unbounded *network* iteration. The sink hard-stops at
`maxPages` and **errors** (does not silently truncate — see partial-failure
below), so a runaway surfaces as a loud failure, not a quietly capped result. The
cap doubles as cost control. Override upward per call when an endpoint genuinely
has more pages; it is never disengaged.

## Partial failure mid-pagination: fail loud (all-or-nothing)

If any page request fails (non-2xx after the action's `Retries`, or a transport
error), `#HttpAll` **fails the whole call**. It does not return a partial list
with a truncation marker.

Rationale: a paginated fetch is **one logical fetch** of a complete collection. A
partial inventory silently presented as complete is a *correctness* trap — a
downstream `#Check` would flag entities as "missing X" when they were merely
unfetched. Fail loud; let mu's existing network-action `Retries`/`RetryBackoffMs`
(`dag/graph.go`) absorb transients. This is distinct from the **batch** per-item
error envelope (ledger E4): a batch fans out over *independent* items where
per-item tolerance is correct; pagination is a single collection where it is not.
Different shape, different policy, deliberately.

## Scope

**v1: build `none` + `page` + `link` + `cursor`.** These are not speculative —
the v1 observe-only targets *require* them on day one (GitHub needs `link` or
`cursor`; `until:"empty"` alone cannot fetch GitHub). Same reasoning as the full
`auth:` vocabulary: the first real models exercise the whole set immediately.

**Deferred: `total-count`** (loop `1..X` from `X-Total-Pages`/a body count). Rare,
and expressible as a `page` variant; add only when a target needs it.

## Determinism / caching

`#HttpAll` is a live network read inside an `impure` populator → never cached
(ledger K1). No cache-key concern. The concatenated result is captured as the
action's output (written via `#WriteFile`), content-hashed by the output path, and
deduped catalog-side by pudl (the observation fixed point) — not by the action
cache.

## Tests (mu repo)

1. `page` + `stop:"empty"`: 3 full pages then an empty page → concatenated items
   from pages 1–3; 4 requests made.
2. `page` + `sizeParam` + `stop:"partial"`: stops on the first short page.
3. `link`: follows `rel="next"` across pages; stops when the header drops it.
4. `cursor` + `nextPath`: feeds the token; stops when `nextPath` is null.
5. `cursor` + `hasMorePath:false`: stops on the boolean gate.
6. `itemsPath`: items nested under `data` are extracted and concatenated.
7. `maxPages`: a never-terminating stub hits the cap → **error**, not a truncated
   result.
8. Mid-pagination 500 (after retries exhausted) → whole call errors; no partial
   list returned.
9. Secret/auth resolution applies per page (the `auth:` block reveals on every
   request, not just the first).

## Sequencing

1. `#HttpAll` sink with `style: none|page` + `maxPages` cap + `itemsPath` (tests
   1–2, 6–7). Rides the secrets-spec sink suite (shares `resolveSecrets`).
2. `style: link` + `cursor` (tests 3–5, 9).
3. Fail-loud partial-failure wiring over mu's existing `Retries` (test 8).
4. `total-count` only if a target demands it.
