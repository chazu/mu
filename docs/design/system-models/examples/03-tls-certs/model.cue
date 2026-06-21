// Example 3 — TLS certificate lifecycle
// Illustrative; #SystemModel schema not finalized.
//
// Fixed point: CONVERGENCE, time-triggered (drift signal is the clock, not a
// config diff). v1: observe expiry + flag; the ACME converge arm is V1-OPEN.

import "op"

certs: #SystemModel & {
	name:   "tls-certs"
	schema: ["tls.certificate"]
	vault: {ACME_KEY: "pass:acme/account", DNS_TOKEN: "pass:dns/cloudflare"}

	// POPULATE — ewe program (see populate.cue). Reads each cert via openssl,
	// captures notAfter. Fan-out is #ExecBatch, not effects-in-a-comprehension.
	populate: #EweTarget & {
		eweSource: "populate.cue"
		outputs: ["certs.json"]
		// no network; reads local cert files via openssl in the sandbox
	}

	// RELATE — derive which certs are inside the 30-day renewal window.
	relations: ["rules/cert_expiry.cue"]

	// CHECK — observe-only flag: warn on anything expiring soon (v1-real).
	checks: [{
		name: "none_expiring_soon", query: "cert_expiring", expect: "empty"
		severity: "warn", message: "certificate within renewal window"
	}]

	// DESIRED — every managed cert valid > 30 days out.
	desired: [for d in ["api.example.com", "www.example.com"] {
		_schema: "tls.certificate", domain: d, min_days_remaining: 30
	}]

	// CONVERGE — V1-OPEN. For each cert in `cert_expiring`, run ACME (DNS-01 with
	// DNS_TOKEN revealed only at the DNS sink), write the renewed cert back.
	converge: #PluginPlan & { // # V1-OPEN
		plugin: "acme"
		input: {account_key: vault.ACME_KEY, dns_token: vault.DNS_TOKEN, only: "cert_expiring"}
	}

	// Daily cadence is the real trigger — time advances the system into drift.
	freshness: {every: "24h"}
}
