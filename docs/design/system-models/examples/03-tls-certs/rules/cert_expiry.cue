// Datalog relation for example 3 (illustrative).
// Selects certs inside the renewal window. `days_until` parses the raw
// `notAfter=...` string (a Datalog builtin / EDB-side helper).

package rules

cert_expiring: {
	// cert_expiring(domain) :-
	//     certificate(domain, not_after),
	//     days_until(not_after) < 30.
	_ = "head: (domain)"
}
