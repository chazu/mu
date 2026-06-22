// Example 3 — TLS cert scan, ewe populator program (current design).
//
// This file is content-addressed into CAS; the action references it by `eweSource`
// (I1). mu restores + runs it through the ewe evaluator at execute time.
//
// SHOWCASE: per-domain fan-out WITHOUT effects inside a comprehension (E2).
// The original sketch ran op.#ReadFile + op.#Exec inside `for d in _domains` —
// illegal under the current design. Instead: pure CUE builds a list of exec
// specs, ONE #ExecBatch runs them, pure CUE joins the results.
//
// #ExecBatch is the exec sibling of #HttpBatch — same E2 batch pattern
// (list in → list of per-item result envelopes out). openssl reads each cert
// file directly, so no separate #ReadFile is needed.

import (
	"op"
	"encoding/json"
)

_domains: ["api.example.com", "www.example.com"]

// pure CUE — build one openssl invocation per domain. NO effect here.
_specs: [for d in _domains {
	cmd: "openssl"
	args: ["x509", "-enddate", "-noout", "-in", "/etc/certs/\(d)/fullchain.pem"]
}]

// ONE batch effect — N openssl runs → list of E4 result envelopes.
_info: op.#ExecBatch & {args: [_specs]}

// pure CUE — join domain ↔ its notAfter. `not_after` is the raw `notAfter=...`
// line; days-to-expiry is computed downstream by the cert_expiry Datalog rule.
_certs: [for i, d in _domains {
	_schema:   "tls.#Certificate" // def reference (D4), bound exactly at ingest
	domain:    d
	not_after: _info.result[i].value.stdout // .value: the E4 envelope payload
}]

out: op.#WriteFile & {args: ["\(op.#Env.result.MU_OUT)/certs.json", json.Marshal(_certs)]}
