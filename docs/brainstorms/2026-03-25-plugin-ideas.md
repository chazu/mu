# Plugin Ideas

**Date:** 2026-03-25
**Status:** Brainstorm

Ideas for new mu plugins and pudl schemas, organized by where the
functionality belongs in the mu/pudl split.

## The Split

- **mu** owns execution: plugins that plan actions, execute them, and observe
  drift. mu plugins speak the NDJSON protocol and produce/consume artifacts.
- **pudl** owns intent and constraints: CUE schemas that define what "correct"
  looks like, structural validation, and policy enforcement. pudl generates
  mu.json targets from CUE definitions.

Some ideas span both — mu provides the observe/converge plugin, pudl provides
the CUE schema and constraints that give it teeth.

---

## Secrets Management

### `pass` plugin (mu)

Use [pass](https://www.passwordstore.org/) (the Unix password manager) as a
secret source. pass is already content-addressed (git-backed, gpg-encrypted),
making it a natural fit for mu's model.

- **Plan:** Read secrets from the pass store, write them as Kubernetes secrets,
  env files, or config files.
- **Observe:** Verify that deployed secrets match what's in pass. Report drift
  when someone edits a secret manually on the cluster.
- **Config:** `store_path`, `gpg_id`, `output_format` (k8s-secret, env, file).

### `op` plugin (mu)

Same pattern as pass but using [1Password CLI](https://developer.1password.com/docs/cli/).

- **Plan:** `op read` to fetch secrets, write to targets.
- **Observe:** Compare live secrets against 1Password vault.
- **Config:** `vault`, `account`, `output_format`.

### Secrets constraints (pudl)

CUE schemas that enforce secrets hygiene:

- "All secrets in namespace X must originate from pass/1password, not inline."
- "No secret may be older than 90 days."
- "Secret rotation policy: these secrets must change every N days."

pudl validates these constraints against the mu target graph. mu plugins
do the actual fetching and deploying.

---

## Policy Enforcement

### `policy` plugin (mu)

Run [OPA](https://www.openpolicyagent.org/) / [conftest](https://www.conftest.dev/) /
[Gatekeeper](https://open-policy-agent.github.io/gatekeeper/) policies against
manifests or live resources.

- **Plan:** Validate manifests against policy. Fail the build if violations
  are found (pre-deploy gate).
- **Observe:** Check if deployed resources still comply with policy. Drift =
  a resource that was compliant at deploy time has been mutated into a
  non-compliant state.
- **Config:** `policy_dir`, `input_format`, `fail_on` (warn/violation).

This plugs directly into BRICK — a policy target can `implements` an interface
like `//policy/no-root-containers`, making the constraint explicit in the
dependency graph.

### Policy constraints (pudl)

pudl is arguably the better home for most policy:

- CUE schemas can express "no container runs as root" directly as a type
  constraint, without needing OPA.
- pudl can validate the entire target graph against policy before mu ever
  runs, catching violations at definition time rather than deploy time.
- OPA/Gatekeeper policies are still useful for runtime enforcement (things
  that can only be checked against the live cluster), which is where the mu
  plugin fits.

**Split:** pudl handles static/structural policy (pre-deploy). mu's policy
plugin handles runtime policy (post-deploy observation).

---

## Developer Standards

### `lint` plugin (mu)

Wrap any linter as an observe target. Language-agnostic — the plugin runs
a configurable command and interprets exit codes.

- **Plan:** Run linter with `--fix` if available. Some linters (gofmt,
  eslint --fix, rubocop -a) can auto-remediate.
- **Observe:** Run linter in check mode. Converged = zero violations.
  Drifted = violations found, reported as diff.
- **Config:** `command` (the linter), `args`, `fix_command` (optional
  auto-fix), `sources` (glob patterns).

Example:

```json
{
  "target": "//lint/go",
  "toolchain": "lint",
  "sources": ["*.go", "**/*.go"],
  "config": {
    "command": ["golangci-lint", "run", "--out-format=json"],
    "fix_command": ["golangci-lint", "run", "--fix"]
  }
}
```

This is a mu plugin because it's about execution — running a tool and
reporting results. pudl doesn't need to know about linter details.

### `structure` plugin (mu)

Declare expected project layout as intent. Observe whether reality matches.

- **Observe:** Check that required files/dirs exist, forbidden patterns
  are absent, file sizes are within limits.
- **Plan:** Could create missing directories or template files, but
  mostly observe-only.
- **Config:** `required_files`, `required_dirs`, `forbidden_patterns`,
  `max_file_size_kb`.

### Structure constraints (pudl)

pudl is the better home for structural constraints that span the whole
project:

- "Every package directory must have a mu.json."
- "Every CUE package must have a README.md."
- "Every target must have at least one test target in its deps."
- "No target may depend on a target in a different BRICK kind without
  going through an interface."

These are graph-level constraints that pudl can enforce across the entire
workspace. mu's structure plugin handles the file-system-level checks
that pudl can't see (pudl doesn't walk the filesystem, it walks CUE
definitions).

### `docs` plugin (mu)

Check documentation completeness and freshness.

- **Observe:** Verify required docs exist (README, CHANGELOG, LICENSE,
  API docs), public symbols have doc comments, generated docs are
  up-to-date.
- **Config:** `required_files`, `doc_comment_threshold` (percentage of
  public symbols that must be documented), `staleness_days` (flag docs
  older than N days).

### `convention` plugin (mu)

Enforce project-specific naming and structure rules that aren't covered
by standard linters.

- **Observe:** Check file naming conventions, import ordering, file
  headers (license/copyright), directory naming, module paths.
- **Config:** Rules as patterns — `file_naming` (snake_case, kebab-case),
  `required_header`, `import_order`.

---

## Container Images

### `buildpack` plugin (mu)

Build container images using [Cloud Native Buildpacks](https://buildpacks.io/)
instead of Dockerfiles.

- **Plan:** Run `pack build` with detected buildpack.
- **Observe:** Check if the image in the registry matches what would be
  built from current sources.
- **Produces:** `docker_image` (same artifact type as the docker plugin).

### `ko` plugin (mu)

Build Go container images using [ko](https://ko.build/) — no Dockerfile
needed, produces minimal images.

- **Plan:** `ko build` from Go source.
- **Observe:** Image in registry matches current source hash.
- **Config:** `importpath`, `base_image`, `platform`.

---

## Where Things Live: Summary

| Concern | mu plugin | pudl schema | Notes |
|---------|-----------|-------------|-------|
| Secret fetching/deploying | pass, op | - | mu executes |
| Secret policy | - | CUE constraints | pudl validates |
| Runtime policy (live cluster) | policy (OPA) | - | mu observes |
| Static policy (pre-deploy) | - | CUE constraints | pudl validates |
| Code linting | lint | - | mu executes |
| Project file structure | structure | - | mu observes filesystem |
| Project graph structure | - | CUE constraints | pudl validates target graph |
| Documentation standards | docs | - | mu observes |
| Naming conventions | convention | - | mu observes |
| Container images | buildpack, ko | - | mu builds |
| Cross-cutting constraints | - | CUE + BRICK | pudl enforces via interfaces |

The pattern: **mu plugins observe and converge concrete resources** (files,
secrets, containers, cluster state). **pudl schemas define and enforce
abstract constraints** (every service must have a health check, all secrets
must come from vault, no target may skip linting).

When both are needed, pudl generates mu.json targets with the right
constraints baked in, and mu executes them.
