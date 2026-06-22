# System Model examples

Five walkthroughs of `#SystemModel` in action. Each shows the model declaration,
which **fixed point** it converges to (observation vs. convergence — see
[README.md](README.md#fixed-points--the-acute-mapping)), the **ACUTE** phases it
exercises, and what `pudl run <model>` actually does step by step.

The ewe functions (`op.#Secret`, `op.#HttpAll`, `op.#Plugin`, …) and Datalog
`checks` are as defined in the README. CUE is illustrative, not final syntax.

| # | Model | Fixed point | Drives a system? |
|---|---|---|---|
| 1 | Remote server provisioning (Odroid HC2) | convergence | yes |
| 2 | Kubernetes policy compliance | observation (→ optional convergence) | flag, then optionally fix |
| 3 | TLS certificate lifecycle | convergence (time-triggered) | yes |
| 4 | DNS zone convergence | convergence | yes |
| 5 | Repo governance (GitLab branch protection) | observation (→ optional convergence) | flag, then optionally enforce |

---

## 1. Provisioning a remote server (Odroid HC2)

**Goal.** Bring a fresh box (`192.168.1.104`, OMV/Ubuntu) to a known state:
packages installed, a service user present, a systemd unit running, a config
file in place. This is the canonical **convergence** model — desired state is
fully known, and we want `pudl run` to drive the box there and keep it there.

**Model.**

```cue
import "op"

odroid: #SystemModel & {
    name:   "odroid-hc2"
    schema: ["host.package", "host.service", "host.file", "host.user"]
    vault:  { ROOT_SSH_KEY: "pass:infra/odroid/root" }

    // DESIRED — IDEA Definition layer: what should be true on the box.
    desired: [
        {_schema: "host.package", name: "podman",  state: "present"},
        {_schema: "host.package", name: "restic",  state: "present"},
        {_schema: "host.user",    name: "svc",     shell: "/usr/sbin/nologin"},
        {_schema: "host.file",    path: "/etc/svc/config.toml", mode: "0640",
                                  content: "interval = \"1h\"\n"},
        {_schema: "host.service", name: "svc",     state: "running", enabled: true},
    ]

    // POPULATE — Accumulate: observe the box's actual state via the `host`
    // SSH observer plugin (already shipped). No custom fetch needed.
    populate: op.#Plugin & { args: [{
        name: "host", op: "observe"
        input: { host: "192.168.1.104", user: "root", key: vault.ROOT_SSH_KEY,
                 probe: ["packages", "users", "files", "services"] }
    }]}

    // CONVERGE — Transform + Execute: hand drift to mu, which dispatches each
    // diffed resource to its convergence plugin (remote-exec / remote-file).
    converge: op.#Plugin & { args: [{ name: "remote-exec", op: "plan"
        input: { host: "192.168.1.104", user: "root", key: vault.ROOT_SSH_KEY } }]}

    // CHECK — belt-and-suspenders: after converge, nothing should still drift.
    checks: [{
        name: "no_residual_drift", query: "host_drift", expect: "empty"
        severity: "fail", message: "host still drifted after converge"
    }]

    freshness: { every: "30m", drift: true }
}
```

**What `pudl run odroid-hc2` does (convergence fixed point):**

1. **Accumulate** — `host observe` SSHes in, returns observed packages/users/
   files/services → ingested into the catalog under the `host.*` schemas.
2. **Unify** — `pudl drift check` diffs `desired` vs observed. First run: podman
   missing, `svc` user absent, config wrong, service down → drift set.
3. **Transform + Execute** — `converge` hands the drift to mu; `remote-exec` /
   `remote-file` plugins emit actions (apt install, useradd, write file, systemctl
   enable+start) and `mu build` runs them over SSH.
4. **Repeat** — re-observe. Drift shrinks. Loop until **drift = ∅** — the
   convergence fixed point. `checks` confirm it; `freshness` re-runs every 30m so
   manual tampering on the box is corrected.

**Why a model and not a shell script:** the box's state is now *typed, queryable,
versioned* in the catalog (when did podman appear? who changed the config?), and
the same declaration documents intent, drives convergence, and detects drift.

---

## 2. Kubernetes policy compliance

**Goal.** Across a cluster, require that **every workload sets CPU/memory
requests and limits, and has a PodDisruptionBudget**. This is fundamentally an
**observation + flag** model (swamp's "inventory and flag") — you are auditing a
system you may not own end-to-end. Convergence is *optional* and gated.

**Model.**

```cue
import "op"

k8s_policy: #SystemModel & {
    name:   "k8s-resource-policy"
    schema: ["k8s.workload", "k8s.pdb"]
    vault:  { KUBECONFIG: "pass:clusters/prod/kubeconfig" }

    // POPULATE — Accumulate: list Deployments/StatefulSets/DaemonSets + PDBs.
    // Reuse the k8s plugin's observe op; one record per workload + per PDB.
    populate: op.#Plugin & { args: [{
        name: "k8s", op: "observe"
        input: { kubeconfig: vault.KUBECONFIG
                 kinds: ["Deployment", "StatefulSet", "DaemonSet",
                         "PodDisruptionBudget"] }
    }]}

    // RELATE — Datalog rules derive the policy violations as relations.
    //   workload_missing_resources(ns, name) :- workload(ns,name,c),
    //       container(c, reqs, lims), (reqs == null ; lims == null).
    //   workload_without_pdb(ns, name) :- workload(ns,name,_),
    //       not pdb_covers(ns, name).
    relations: ["rules/k8s_policy.cue"]

    // CHECK — the policy gates. expect: "empty" means compliant.
    checks: [
        {name: "resources_set", query: "workload_missing_resources",
         expect: "empty", severity: "fail",
         message: "workload missing requests/limits"},
        {name: "pdb_present", query: "workload_without_pdb",
         expect: "empty", severity: "warn",
         message: "workload has no PodDisruptionBudget"},
    ]

    // No `desired`/`converge` here → OBSERVE-ONLY. Audit + report, do not mutate
    // the cluster. (Optional convergence sketched below.)
    freshness: { every: "15m", drift: true }
}
```

**What `pudl run k8s-resource-policy` does (observation fixed point):**

1. **Accumulate** — `k8s observe` lists the workloads and PDBs → catalog.
2. **Configure** — schema inference types each as `k8s.workload` / `k8s.pdb`.
3. **Unify** — Datalog evaluates `rules/k8s_policy.cue` to a **fixed point**
   (semi-naive, "stop when no new rows"), deriving `workload_missing_resources`
   and `workload_without_pdb` relations.
4. **Check** — each `check` evaluates its relation; non-empty `resources_set`
   fails the run, non-empty `pdb_present` warns. The report lists offenders
   (namespace/name) as markdown + JSON.
5. **Freshness** — every 15m the catalog is refreshed; because observation is
   idempotent, a compliant cluster produces no new versions — the **observation
   fixed point**: the audit is stable until the cluster actually changes.

**Note the two fixed points stack here:** the *Datalog* fixed point (rule
evaluation terminates) lives *inside* the *observation* fixed point (the catalog
stabilizes). Policy as Datalog is the natural fit — it is exactly the "derive
new facts until none appear" shape.

**Optional convergence (gated).** If you *do* own the cluster and want
auto-remediation for the PDB case:

```cue
    desired: [ /* a #PDB per workload, generated from the workload set */ ]
    converge: op.#Plugin & { args: [{ name: "k8s", op: "plan"
        input: { kubeconfig: vault.KUBECONFIG, apply: ["PodDisruptionBudget"] }}]}
```

Now `pudl run` would, *after flagging*, apply missing PDBs and loop to the
**convergence fixed point**. Requests/limits stay flag-only (mutating pod specs
is owner territory) — illustrating that a single model can converge *part* of a
system and only flag the rest. Whether convergence fires is one of the open
gating questions in the README.

---

## 3. TLS certificate lifecycle

**Goal.** Keep a set of TLS certs valid: observe each cert's expiry, and when one
is within the renewal window, renew it (ACME) and store it. This is a
**convergence** model where the drift signal is **time**, not a config diff —
the desired state is "not expiring soon," and `freshness` cadence is what
triggers the loop.

**Model.**

```cue
import "op"

certs: #SystemModel & {
    name:   "tls-certs"
    schema: ["tls.certificate"]
    vault:  { ACME_KEY: "pass:acme/account", DNS_TOKEN: "pass:dns/cloudflare" }

    // POPULATE — Accumulate: read each managed cert, extract notAfter.
    // ewe target: read files, parse, derive days-to-expiry (pure CUE math).
    populate: {
        plan: [{
            id: "scan", outputs: ["certs.json"]
            ewe: {
                _domains: ["api.example.com", "www.example.com"]
                _certs: [ for d in _domains {
                    let pem = op.#ReadFile & { args: ["/etc/certs/\(d)/fullchain.pem"] }
                    let info = op.#Exec & { args: [{cmd: "openssl", args:
                        ["x509", "-enddate", "-noout"], stdin: pem.result}] }
                    {_schema: "tls.certificate", domain: d,
                     not_after: info.result.stdout}   // parsed downstream
                }]
                out: op.#WriteFile & { args: ["\(op.#Env.result.MU_OUT)/certs.json",
                                              json.Marshal(_certs)] }
            }
        }, "action/emit"]
    }

    // DESIRED — every managed cert must be valid > 30 days out.
    desired: [ for d in ["api.example.com", "www.example.com"] {
        _schema: "tls.certificate", domain: d, min_days_remaining: 30
    }]

    // RELATE — derive which certs are inside the renewal window.
    //   cert_expiring(domain) :- certificate(domain, not_after),
    //       days_until(not_after) < 30.
    relations: ["rules/cert_expiry.cue"]

    // CONVERGE — for each expiring cert, run ACME via the acme plugin (DNS-01
    // using DNS_TOKEN), write the new cert back. mu executes; secrets revealed
    // only at the ACME/DNS sinks.
    converge: op.#Plugin & { args: [{ name: "acme", op: "plan"
        input: { account_key: vault.ACME_KEY, dns_token: vault.DNS_TOKEN,
                 only: "cert_expiring" } }]}

    checks: [{name: "none_expiring_soon", query: "cert_expiring",
              expect: "empty", severity: "warn",
              message: "certificate within renewal window"}]

    // Daily cadence is the real trigger — time advances the system into drift.
    freshness: { every: "24h" }
}
```

**What `pudl run tls-certs` does (time-triggered convergence):**

1. **Accumulate** — read each cert, capture `not_after` → catalog.
2. **Unify** — Datalog `cert_expiring` selects certs under 30 days. Most days:
   empty → already at the **convergence fixed point**, run is a no-op.
3. **Transform + Execute** — when a cert enters the window, `converge` runs ACME
   (DNS-01 with the Cloudflare token revealed only at the DNS API call), writes
   the renewed `fullchain.pem`.
4. **Repeat** — re-scan; the renewed cert is now > 30 days out; `cert_expiring`
   empties; fixed point restored.

**The interesting property:** the system *spontaneously drifts over time* even
with no human action — expiry marches forward. `freshness.every: "24h"` is what
re-enters the loop; the model converges the system back each day. This is the
convergence fixed point with a clock as the perturbation source, and a clean
showcase of `vault` + sink-only `Reveal`.

---

## 4. DNS zone convergence

**Goal.** A declared DNS zone (records you want) vs. the provider's actual
records. Drive the provider to match. The textbook **convergence fixed point** —
desired and actual are both concrete record sets, and convergence is the set
difference applied as create/update/delete.

**Model.**

```cue
import "op"

dns: #SystemModel & {
    name:   "dns-example-com"
    schema: ["dns.record"]
    vault:  { DNS_TOKEN: "pass:dns/cloudflare" }

    // DESIRED — the zone as it should be.
    desired: [
        {_schema: "dns.record", type: "A",     name: "@",   value: "203.0.113.10"},
        {_schema: "dns.record", type: "CNAME", name: "www", value: "example.com"},
        {_schema: "dns.record", type: "MX",    name: "@",   value: "10 mail.example.com"},
    ]

    // POPULATE — Accumulate: list the provider's current records (paginated API).
    populate: {
        plan: [{
            id: "list", outputs: ["records.json"], network: true, impure: true
            ewe: {
                _tok: op.#Secret & { args: ["DNS_TOKEN"] }
                _raw: op.#HttpAll & { args: [{
                    url: "https://api.cloudflare.com/client/v4/zones/Z123/dns_records"
                    headers: { Authorization: "Bearer \(_tok.result)" }
                    paginate: { param: "page", until: "empty" }
                }]}
                _recs: [ for r in _raw.result {
                    _schema: "dns.record", type: r.type, name: r.name, value: r.content
                }]
                out: op.#WriteFile & { args: ["\(op.#Env.result.MU_OUT)/records.json",
                                              json.Marshal(_recs)] }
            }
        }, "action/emit"]
    }

    // CONVERGE — apply the desired/actual set difference via the provider API.
    converge: op.#Plugin & { args: [{ name: "cloudflare-dns", op: "plan"
        input: { zone: "Z123", token: vault.DNS_TOKEN } }]}

    checks: [{name: "zone_in_sync", query: "dns_drift", expect: "empty",
              severity: "fail", message: "DNS zone drifted from declaration"}]

    freshness: { every: "1h", drift: true }
}
```

**What `pudl run dns-example-com` does:**

1. **Accumulate** — paginate the Cloudflare API → one `dns.record` per row.
2. **Unify** — `pudl drift check` computes the field-level diff of `desired` vs
   actual: records to add, records to delete, values to update.
3. **Transform + Execute** — `cloudflare-dns plan` turns that diff into
   POST/PUT/DELETE actions; mu runs them (token revealed only at the API call).
4. **Repeat** — re-list; diff empties; **convergence fixed point**. A record
   edited by hand in the dashboard is reverted within the hour by `freshness`.

This is the purest illustration: desired ∖ actual = create, actual ∖ desired =
delete, and convergence is "apply the difference until the difference is empty."

---

## 5. Repo governance (GitLab branch protection)

**Goal.** Extend the running GitLab inventory: not only catalog every repo, but
**require that the default branch is protected** (no force-push, requires
review). **Observe + flag** by default, with optional **enforcement**
(convergence) for repos you administer.

**Model.**

```cue
import "op"

gitlab_gov: #SystemModel & {
    name:   "gitlab-governance"
    schema: ["git.repository.gitlab", "git.branch_protection"]
    vault:  { GITLAB_TOKEN: "env:GITLAB_TOKEN" }

    // POPULATE — the gist generalized: paginate projects, then for each, fetch
    // its protected-branches. Two HttpAll calls, joined in CUE.
    populate: {
        sealed_inputs:      { GITLAB_TOKEN: "env:GITLAB_TOKEN" }
        sealed_input_modes: { GITLAB_TOKEN: "env" }
        plan: [{
            id: "fetch", outputs: ["repos.json"], network: true, impure: true
            ewe: {
                _t: op.#Secret & { args: ["GITLAB_TOKEN"] }
                _repos: op.#HttpAll & { args: [{
                    url: "https://gitlab.com/api/v4/groups/garner-health/projects"
                    query: { include_subgroups: true, per_page: 100 }
                    headers: { "PRIVATE-TOKEN": _t.result }
                    paginate: { param: "page", until: "empty" }
                }]}
                _withprot: [ for r in _repos.result if r.default_branch != null {
                    let prot = op.#Http & { args: [{
                        url: "https://gitlab.com/api/v4/projects/\(r.id)/protected_branches"
                        headers: { "PRIVATE-TOKEN": _t.result } }]}
                    {_schema: "git.repository.gitlab",
                     name: r.path_with_namespace, default_branch: r.default_branch,
                     protections: prot.result}
                }]
                out: op.#WriteFile & { args: ["\(op.#Env.result.MU_OUT)/repos.json",
                                              json.Marshal(_withprot)] }
            }
        }, "action/emit"]
    }

    // RELATE — derive unprotected default branches.
    //   default_unprotected(repo) :- repository(repo, branch, prots),
    //       not protects(prots, branch, {no_force_push, review_required}).
    relations: ["rules/branch_protection.cue"]

    checks: [{name: "default_branch_protected", query: "default_unprotected",
              expect: "empty", severity: "warn",
              message: "default branch not protected"}]

    // OBSERVE-ONLY by default. To ENFORCE on repos you admin, add:
    //   desired:  [ for r in repos { protection: standard_protection } ]
    //   converge: op.#Plugin & {args:[{name:"gitlab", op:"plan",
    //             input:{token: vault.GITLAB_TOKEN, apply:"protected_branches"}}]}
    // → flags everywhere, enforces only where desired is declared.

    freshness: { every: "6h", drift: true }
}
```

**What `pudl run gitlab-governance` does (observation fixed point):**

1. **Accumulate** — paginate projects; per repo, fetch protected-branches; join
   in CUE → one enriched `git.repository.gitlab` record each.
2. **Unify** — Datalog derives `default_unprotected`.
3. **Check** — `default_branch_protected` warns, listing offenders. Report =
   the governance audit.
4. **Freshness** — every 6h; stable unless a repo's protection actually changes.

**Enforcement is a one-field upgrade.** Add `desired` + `converge` and the same
model *applies* standard protection to the repos you administer, looping to the
convergence fixed point — while still only *flagging* repos you merely observe.
The observe→converge upgrade is the same shape in examples 2 and 5: start as an
auditor, become an enforcer where you have authority and a known desired state.

---

## What the five show together

- **Observation fixed point** (idempotent inventory + flag): 2 and 5 by default.
  The catalog stabilizes; checks evaluate over a stable snapshot; Datalog is the
  natural policy engine.
- **Convergence fixed point** (observed == desired): 1, 3, 4 — and 2/5 when a
  desired state is declared. `converge` is consistently "hand drift to mu via a
  plugin's `plan` op; `mu build` executes."
- **Perturbation sources differ:** config drift (1, 4), the passage of time
  (3), human edits to a shared system (4, 5), and external reality you don't
  control (2). The same `#SystemModel` + `pudl run` loop absorbs all of them;
  only `freshness.every` and whether `desired`/`converge` exist change.
- **One artifact, two roles:** every model is simultaneously *documentation* of
  intent and the *executable* that realizes/audits it — the swamp coherence,
  built from pudl (catalog + Datalog) and mu (DAG + plugins) parts already shipped.
