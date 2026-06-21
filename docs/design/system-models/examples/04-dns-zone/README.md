# 4 — DNS zone convergence

**Goal.** A declared DNS zone (records you want) vs. the provider's actual records.
Drive the provider to match.

**Fixed point.** Convergence — the textbook case: `desired ∖ actual = create`,
`actual ∖ desired = delete`. **In v1: observe records + flag drift**; the apply arm
is deferred (ledger V1).

## Under the current design

- **Populate is an ewe program** ([`populate.cue`](populate.cue)), `eweSource`-
  referenced (I1).
- **The headline fix (S1):** the original sketch wrote
  `Authorization: "Bearer \(_tok.result)"`. That is **forbidden** — a secret-ref
  struct cannot be interpolated into a CUE string (the engine rejects it; this is
  the keystone safety property, verified). Instead the request carries
  `auth: { bearer: { ref: "DNS_TOKEN" } }` — a ref only. The `#HttpAll` sink reveals
  the token in Go and builds the `Authorization` header there, never in CUE.
- **Paging** is `style: "page"` with `itemsPath: "result"` (Cloudflare nests records
  under `.result`) — the I2 vocabulary, all Go-side.

## What `pudl run dns-example-com` does

**v1 (observe + flag):**
1. **Accumulate** — the ewe program paginates the Cloudflare API → one `dns.record`
   per row → `records.json` → catalog.
2. **Unify** — the `dns_drift` relation diffs `desired` vs actual (add/delete/update).
3. **Check** — `zone_in_sync` warns on any drift; report lists the differences.

**Deferred to V1 (convergence):**
4. **Transform + Execute** — `cloudflare-dns plan` turns the diff into
   POST/PUT/DELETE actions; mu runs them (token revealed only at the API call).
5. **Repeat** — re-list; diff empties; convergence fixed point. A record edited by
   hand in the dashboard is reverted within the hour by `freshness`.

The purest illustration of "apply the difference until the difference is empty" —
once the V1 loop exists.

## Files
- [`model.cue`](model.cue) — the model declaration.
- [`populate.cue`](populate.cue) — the ewe populator (`auth.bearer` ref + paging).
