// Example 4 — DNS zone convergence
// Illustrative; #SystemModel schema not finalized.
//
// Fixed point: CONVERGENCE (textbook — desired ∖ actual = create, actual ∖ desired
// = delete). v1: observe records + flag drift; the apply arm is V1-OPEN.

// dns.#Record is a model-introduced schema (shipped in .pudl/dns, D2).
import "dns"

dnsZone: #SystemModel & {
	name: "dns-example-com"
	// schema: definition reference (D3).
	schema: [dns.#Record]
	vault: {DNS_TOKEN: "pass:dns/cloudflare"}

	// POPULATE — ewe program (see populate.cue): list the provider's records.
	// Secret via auth.bearer REF (S1), paging via style:"page" (I2).
	populate: #EweTarget & {
		eweSource: "populate.cue"
		outputs: ["records.json"]
		network:  true
		sealed_inputs: {DNS_TOKEN: "pass:dns/cloudflare"}
		sealed_input_modes: {DNS_TOKEN: "env"}
	}

	// DESIRED — the zone as it should be.
	desired: [
		{_schema: "dns.#Record", type: "A", name: "@", value: "203.0.113.10"},
		{_schema: "dns.#Record", type: "CNAME", name: "www", value: "example.com"},
		{_schema: "dns.#Record", type: "MX", name: "@", value: "10 mail.example.com"},
	]

	// CHECK — observe-only flag: warn when actual differs from desired (v1-real,
	// one-shot drift relation).
	checks: [{
		name: "zone_in_sync", query: "dns_drift", expect: "empty"
		severity: "warn", message: "DNS zone drifted from declaration"
	}]

	// CONVERGE — V1-OPEN. Apply the desired/actual set difference (POST/PUT/DELETE)
	// via the provider plugin; mu runs the actions, token revealed only at the API.
	converge: #PluginPlan & { // # V1-OPEN
		plugin: "cloudflare-dns"
		input: {zone: "Z123", token: vault.DNS_TOKEN}
	}

	freshness: {every: "1h", drift: true}
}
