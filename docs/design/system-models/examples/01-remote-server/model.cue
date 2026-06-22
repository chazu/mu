// Example 1 — remote server provisioning (Odroid HC2)
// Illustrative; #SystemModel schema not finalized (lives in mu/pudl/shared — open).
//
// Fixed point: CONVERGENCE. v1 gives observe + drift-flag; the converge arm and
// the reconcile loop are V1-OPEN (ledger V1).

import (
	"pudl/linux"
	"pudl/fs"
)

odroid: #SystemModel & {
	name: "odroid-hc2"
	// schema: definition references (D3), not dotted strings. These are shipped
	// pudl schemas; the host plugin's OutputSchema declares the same defs.
	schema: [linux.#Package, linux.#Service, linux.#User, fs.#File]
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
	// records self-tag with a _schema definition reference (D4): "pudl/<mod>.#<Def>"
	desired: [
		{_schema: "pudl/linux.#Package", name: "podman", state: "present"},
		{_schema: "pudl/linux.#Package", name: "restic", state: "present"},
		{_schema: "pudl/linux.#User", name: "svc", shell: "/usr/sbin/nologin"},
		{_schema: "pudl/fs.#File", path: "/etc/svc/config.toml", mode: "0640", content: "interval = \"1h\"\n"},
		{_schema: "pudl/linux.#Service", name: "svc", state: "running", enabled: true},
	]

	// CHECK — observe-only flag: report any drift from desired (one-shot, feasible
	// today via the drift relation). This is the v1-real value of the model.
	checks: [{
		name: "no_residual_drift", query: "host_drift", expect: "empty"
		severity: "warn", message: "host drifted from desired state"
	}]

	// CONVERGE — V1-OPEN (design resolved, see host-converge-spec.md). The SAME
	// `host` plugin used for populate gains a declarative `plan` op: pudl routes
	// `desired` to it as sources; host.plan emits guarded idempotent SSH actions
	// (apt install, useradd, write file, systemctl); mu build runs them; the loop
	// re-observes until drift = ∅. (Was mis-specced as `remote-exec`, which is
	// imperative and can't reconcile declarative desired.)
	converge: #PluginPlan & { // # V1-OPEN
		plugin: "host"
		input: {host: "192.168.1.104", user: "root", key: vault.ROOT_SSH_KEY}
	}

	freshness: {every: "30m", drift: true}
}
