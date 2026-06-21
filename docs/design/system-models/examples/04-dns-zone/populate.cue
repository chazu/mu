// Example 4 — DNS zone listing, ewe populator program (current design).
//
// Content-addressed; referenced by `eweSource` (I1).
//
// SHOWCASE: secret-as-auth via REFERENCE (S1). The original sketch wrote
//   headers: { Authorization: "Bearer \(_tok.result)" }
// which is FORBIDDEN — you cannot interpolate a secret-ref struct into a string
// (CUE rejects it; verified). Instead the request carries an `auth.bearer` block
// holding only a ref; the #HttpAll sink reveals DNS_TOKEN in Go immediately
// before the request and builds `Authorization: Bearer <token>` there.

import (
	"op"
	"encoding/json"
)

// paginate in Go (offset paging); Cloudflare wraps records under `.result`.
_raw: op.#HttpAll & {args: [{
	url:  "https://api.cloudflare.com/client/v4/zones/Z123/dns_records"
	auth: {bearer: {ref: "DNS_TOKEN"}} // ref only — no secret in CUE
	paginate: {style: "page", param: "page", stop: "empty"}
	itemsPath: "result" // CF response: { result: [ … ], result_info: {…} }
}]}

_recs: [for r in _raw.result {
	_schema: "dns.#Record" // def reference (D4), bound exactly at ingest
	type:    r.type
	name:    r.name
	value:   r.content
}]

out: op.#WriteFile & {args: ["\(op.#Env.result.MU_OUT)/records.json", json.Marshal(_recs)]}
