// Example 1 — remote server provisioning (Odroid HC2)
// Illustrative; #SystemModel schema not finalized (lives in mu/pudl/shared — open).
//
// Fixed point: CONVERGENCE. v1 gives observe + drift-flag; the converge arm and
// the reconcile loop are V1-OPEN (ledger V1).

import "op"

odroid: #SystemModel & {
	name:   "odroid-hc2"
	schema: ["host.package", "host.service", "host.file", "host.user"]
	vault: {ROOT_SSH_KEY: "pass:infra/odroid/root"}

	// POPULATE — reuse the shipped `host` SSH observer. Under the current design
	// this is a #PluginObserve populate KIND, not an inline op.#Plugin call (P2):
	// the plugin's observe runs as its own action and its output is ingested.
	populate: #PluginObserve & {
		plugin: "host"
		input: {
			host:  "192.168.1.104"
			user:  "root"
			key:   vault.ROOT_SSH_KEY // resolved + revealed plugin-side, never in CUE
			probe: ["packages", "users", "files", "services"]
		}
	}

	// DESIRED — IDEA Definition layer: what should be true on the box.
	desired: [
		{_schema: "host.package", name: "podman", state: "present"},
		{_schema: "host.package", name: "restic", state: "present"},
		{_schema: "host.user", name: "svc", shell: "/usr/sbin/nologin"},
		{_schema: "host.file", path: "/etc/svc/config.toml", mode: "0640", content: "interval = \"1h\"\n"},
		{_schema: "host.service", name: "svc", state: "running", enabled: true},
	]

	// CHECK — observe-only flag: report any drift from desired (one-shot, feasible
	// today via the drift relation). This is the v1-real value of the model.
	checks: [{
		name: "no_residual_drift", query: "host_drift", expect: "empty"
		severity: "warn", message: "host drifted from desired state"
	}]

	// CONVERGE — V1-OPEN. Hands drift to mu; remote-exec/remote-file plugins emit
	// actions (apt install, useradd, write file, systemctl) and mu build runs them
	// over SSH. The reconcile LOOP (re-observe → converge → … until drift = ∅) is
	// not yet designed.
	converge: #PluginPlan & { // # V1-OPEN
		plugin: "remote-exec"
		input: {host: "192.168.1.104", user: "root", key: vault.ROOT_SSH_KEY}
	}

	freshness: {every: "30m", drift: true}
}
