# 3 — TLS certificate lifecycle

**Goal.** Keep TLS certs valid: observe each cert's expiry; when one nears its
renewal window, renew (ACME) and store it.

**Fixed point.** Convergence, **time-triggered** — the drift signal is the clock,
not a config diff. **In v1: observe expiry + flag**; the ACME converge arm is
deferred (ledger V1).

## Under the current design

- **Populate is an ewe program** ([`populate.cue`](populate.cue)), referenced by
  `eweSource` and content-addressed (I1) — not an inline `ewe:` block.
- **The headline fix (E2):** the original sketch ran `op.#ReadFile` + `op.#Exec`
  *inside* `for d in _domains`. That is now illegal (no effects in comprehensions).
  The program instead: pure CUE builds a list of openssl specs → **one
  `#ExecBatch`** runs them → pure CUE joins by index. `#ExecBatch` is the exec
  sibling of `#HttpBatch` (same batch pattern); openssl reads each cert directly,
  so no separate `#ReadFile`.
- **Per-item errors** ride the `#ExecBatch` E4 envelope (`.value.stdout`); a cert
  that fails to read is flagged downstream rather than aborting the scan.

## What `pudl run tls-certs` does

**v1 (observe + flag):**
1. **Accumulate** — the ewe program runs openssl over each cert, writes
   `certs.json` → ingested as `tls.#Certificate` (model-shipped schema, D2).
2. **Unify** — `cert_expiry.cue` derives `cert_expiring` (under 30 days).
3. **Check** — `none_expiring_soon` warns on anything inside the window. Most days:
   empty → nothing to do.

**Deferred to V1 (convergence):**
4. **Transform + Execute** — `converge` runs ACME (DNS-01, Cloudflare token revealed
   only at the DNS API call), writes the renewed `fullchain.pem`.
5. **Repeat** — re-scan; renewed cert is > 30 days out; `cert_expiring` empties.

**The interesting property:** the system drifts *spontaneously over time* — expiry
marches forward with no human action. `freshness.every: "24h"` re-enters the loop.
A clean showcase of vault + sink-only reveal (the secret is a `pass:` ref used only
inside the ACME/DNS plugin).

## Files
- [`model.cue`](model.cue) — the model declaration.
- [`populate.cue`](populate.cue) — the ewe populator (batch-exec fan-out).
- [`rules/cert_expiry.cue`](rules/cert_expiry.cue) — the renewal-window relation.
