---
title: "feat: mu plugin test — a harness for plugin authors"
type: feat
status: proposed
date: 2026-04-19
leverage: 5
---

# feat: `mu plugin test` — a test harness for plugin authors

## Summary

Add a `mu plugin test` subcommand that exercises a plugin against canned
NDJSON request scenarios and verifies its responses. Plugins are mu's entire
value-add surface area — every integration (aws, host, docker, k8s, file,
lint, go, pass, cowsay, scratch, terraform, zig) is a plugin — yet authors
today debug by hand:

```sh
echo '{"method":"discover"}' | bb plugins/host/plugin.bb
```

That workflow is undocumented, fragile (you re-type the JSON), and offers
no regression coverage. `mu plugin test` promotes this loop into a
first-class developer command with scenarios, golden files, CI-friendly
output, and bundled fixtures that ship with mu. It makes plugin authoring
feel like writing a Go program against `go test`: fast, local, iterative.

## Problem Frame

Today:

- Authors hand-roll NDJSON requests and eyeball the responses.
- There is no shared library of reference requests (discover, plan, observe,
  resolve_secret, missing-capability, invalid-config, crash).
- The only automated coverage is `internal/plugin/*_test.go`, which uses
  shell-script mocks (`internal/plugin/testdata/mock_*.sh`) — those exercise
  the **manager**, not real plugins.
- CI has no way to gate a PR on "plugin `foo` still answers discover
  correctly," which means wire-format regressions land silently.
- Onboarding a new plugin author requires reading `protocol.go` and
  inferring the envelope by reverse-engineering `plugins/host/plugin.bb`.

We want the plugin protocol to be as welcoming as `net/http` — obvious
inputs, obvious outputs, obvious failures.

## User Stories

- **As a plugin author** I want to run `mu plugin test ./plugins/host`
  and see pass/fail for a handful of baseline scenarios so I know my
  plugin is wire-compatible before I commit.
- **As a plugin author** I want to record a golden response with
  `--update` so I can snapshot the exact shape my plugin emits today
  and be alerted when it changes.
- **As a plugin author** I want to write a custom scenario YAML that
  sends a specific `plan` request with my target config and sources so
  I can TDD a new capability.
- **As a mu maintainer** I want bundled scenarios (`discover_ok`,
  `plan_minimal`, `invalid_config`, `missing_capability`) that every
  plugin can opt into, so our plugin directory gets free regression
  coverage.
- **As a CI operator** I want `mu plugin test ./plugins/... --json`
  to emit machine-readable results with a non-zero exit on failure,
  so I can wire it into GitHub Actions.
- **As a protocol maintainer** I want bumping `ProtocolVersion` to
  surface as a coordinated test failure across every plugin, so we
  notice breaking changes before release.

## User Experience

### Entry points

```text
mu plugin test <plugin-path> [flags]

Flags:
  --scenario <name>       Run only the named scenario (may repeat).
  --golden <file>         Treat <file> as the expected response (JSON).
  --update                Rewrite golden files / response snapshots
                          from the actual output. Implies pass.
  --json                  Emit machine-readable results on stdout.
  --verbose, -v           Show full request/response on failure.
  --timeout <dur>         Per-request timeout (default: protocol default).
  --bundled               Also run mu's built-in generic scenarios
                          (discover_ok, invalid_json_request, etc.).
                          On by default; --bundled=false to disable.
```

`<plugin-path>` is any of:

1. A directory containing `mu.json` + `plugin.bb` (e.g. `./plugins/host`) —
   the normal case during authoring.
2. A file path to the plugin entrypoint directly (`./plugins/host/plugin.bb`).
3. A plugin name registered in the surrounding `mu.json` (resolved via
   `resolveProjectRoot`), so `mu plugin test host` works from anywhere
   in the tree.

### The inner loop

```text
$ mu plugin test ./plugins/host
=== plugins/host (toolchain: bb) ===
  ✓ discover_ok                   (12ms)
  ✓ discover_declares_observe    (11ms)
  ✓ observe_missing_host         (23ms) — error path matched
  ✗ plan_minimal                  (14ms)
        scenario: plan_minimal
        request:  {"method":"plan","target":{"name":"//x",...}}
        expected: {"actions":[...], ...}
        actual:   {"error":"plan not supported"}
        diff:
          - "actions": []
          + "error":   "plan not supported"

3 passed, 1 failed in 60ms
```

Clean, colorless-by-default (`NO_COLOR` respected), diff inline. The
visual language is deliberately conservative so a future riso-print or
TUI skin can restyle without touching the harness.

### Scenario file format

YAML by default (readable), JSON accepted (tooling-friendly). Files live
beside the plugin at `plugins/<name>/testdata/<scenario>.yaml` or in
`internal/plugin/testdata/scenarios/` for bundled/shared scenarios.

```yaml
# plugins/host/testdata/observe_missing_host.yaml
name: observe_missing_host
description: Plugin returns an error when config.host is missing.
request:
  method: observe
  target:
    name: "//svc/web"
    toolchain: host
    sources: []
    config: {}                 # intentionally empty
  toolchain_artifacts: {}
  secrets: {}
expect:
  # Three tiers of assertion, most specific wins.
  match: shape                # one of: shape | exact | golden
  shape:
    error: "<string,non-empty>"
  # Optional: exit within a duration budget.
  within: 2s
  # Optional: exit code of the subprocess (default 0).
  exit: 0
```

Three assertion modes:

- `match: exact` — equality against an inline `response:` block.
- `match: shape` — structural template: literal values must equal;
  `<type,…>` sentinels assert type and optional constraints (`non-empty`,
  `>=0`, regex). Keys not listed are allowed (by default) or forbidden
  (`strict: true`).
- `match: golden` — compare against `testdata/<scenario>.golden.json`.
  `--update` rewrites the file.

A scenario can optionally carry a `pre:` block declaring fixture files
to place in a temp cwd before launching the plugin, and a `cleanup:` to
teardown — matching what `host/plan.bb` needs when driving real
commands. (Design sketch; see open questions.)

### Bundled / generic scenarios

Shipped at `internal/plugin/testdata/scenarios/` and run unless
`--bundled=false`:

| Scenario                | Asserts                                                 |
|-------------------------|---------------------------------------------------------|
| `discover_ok`           | `method: discover` returns `protocol_version == 1`, non-empty `name`, non-empty `capabilities` (or old-plugin fallback). |
| `discover_version_pin`  | `protocol_version` equals `plugin.ProtocolVersion`.     |
| `unknown_method`        | Sending `{"method":"bogus"}` yields `{"error": "..."}`, plugin does not crash, still responds to a follow-up discover. |
| `malformed_json`        | Plugin survives a malformed line (per-plugin opt-out — some authors legitimately fail-fast). |
| `missing_capability`    | If discover does not declare `observe`, calling observe yields a graceful error; if it does declare observe, the scenario is skipped automatically. |
| `plan_minimal`          | Gated on `"plan"` capability: send a minimal target, expect a `PlanResponse` with `actions` array (possibly empty) and `declared_outputs` map. |
| `invalid_config`        | Gated on `"plan"` capability: send a target whose `config` violates `config_schema`, expect a non-empty `error`. |

Bundled scenarios introspect the plugin's own `discover` response to
decide which to run — so a secret-provider plugin (`pass`) that only
declares `["discover", "resolve_secret"]` won't be failed for lacking
`plan`.

### Golden files and `--update`

```sh
mu plugin test ./plugins/aws --scenario observe_ec2 --update
```

Rewrites `plugins/aws/testdata/observe_ec2.golden.json` with the actual
response. Golden files are regular JSON (pretty-printed, stable key
order). Diffs use the same pretty format for clean `git diff` output.

### CI integration

```sh
mu plugin test ./plugins/... --json > results.json
```

- Exit 0 on all pass, 1 on any failure, 2 on usage errors.
- `./plugins/...` glob runs the harness over every plugin directory
  containing a `mu.json` with a `plugin` block — mirrors the build-target
  semantics.
- JSON output:

  ```json
  {
    "plugin": "host",
    "path": "plugins/host",
    "toolchain": "bb",
    "results": [
      {"scenario":"discover_ok","status":"pass","duration_ms":12},
      {"scenario":"plan_minimal","status":"fail","duration_ms":14,
       "diff":"...","request":{...},"actual":{...},"expected":{...}}
    ],
    "summary": {"pass": 3, "fail": 1, "skip": 0}
  }
  ```

## Acceptance Criteria

1. `mu plugin test` is registered under `cmd/mu/plugin.go` alongside
   `list` and `add`; `mu plugin` usage text lists it.
2. `mu plugin test <dir>` auto-detects toolchain (`bb` or direct)
   from `mu.json` or file extension, reusing `inferPluginToolchain`
   and `resolveBbPath`.
3. Running `mu plugin test ./plugins/host` from a clean checkout
   (after `mu build` bootstraps bb) passes the bundled `discover_ok`,
   `discover_version_pin`, and `unknown_method` scenarios without any
   hand-written fixtures.
4. Adding a file `plugins/host/testdata/my_scenario.yaml` with a
   `request` + `expect` block and re-running the command picks it up
   automatically.
5. `--update` on a golden-mode scenario overwrites the `.golden.json`
   file and exits 0; a second run with no change passes; modifying the
   plugin so the response shape shifts causes the next run to fail
   with a readable diff.
6. `--json` emits a valid JSON object per plugin on stdout; a human
   summary still goes to stderr so `--json` is pipeable.
7. Missing-capability scenarios gracefully skip (reported as
   `status: "skip"`) when the plugin legitimately doesn't declare that
   capability — they do not count as failures.
8. Exit codes: 0 all-pass, 1 any-fail, 2 usage or fixture-parse error,
   matching `runPluginAdd` / `runPluginList` conventions.
9. `mu plugin test ./plugins/...` runs every plugin directory and
   returns non-zero if any single plugin fails.
10. At least four plugins in the tree (`host`, `aws`, `pass`, plus one
    without `observe` e.g. `cowsay`) ship at least one scenario under
    `plugins/<name>/testdata/` demonstrating the pattern.
11. Each existing plugin `GUIDE.md` gets a short "Testing" section
    pointing at `mu plugin test`.
12. Unit tests for the harness cover: YAML/JSON loader, shape matcher
    (including `<string,non-empty>` and `<number,>=0>` sentinels),
    golden read/write round-trip, capability gating, and subprocess
    timeout handling. Use the existing `internal/plugin/testdata/mock_*.sh`
    mocks as plugins-under-test.

## Technical Context

### Files to extend

- `cmd/mu/plugin.go` — new `runPluginTest(args []string) int` dispatched
  from the `switch args[0]` in `runPlugin`. Update the usage block.
- `internal/plugin/process.go` — already exposes `StartProcess`,
  `Discover`, `Plan`, `Observe`, `ResolveSecret`, `Close`. The harness
  can reuse these directly; no protocol changes.
- `internal/plugin/protocol.go` — zero changes. All assertions are over
  existing `Request` / `*Response` shapes.
- `internal/plugin/manager.go` — the harness may bypass `Manager`
  entirely (single plugin, no fan-out), but reuses `inferPluginToolchain`
  and `resolveBbPath` from `cmd/mu/plugin.go`.

### New files

- `internal/plugintest/` — new package (sibling to `internal/plugin`)
  implementing the harness:
  - `harness.go` — orchestrator; loads scenarios, runs plugin, reports.
  - `scenario.go` — YAML/JSON fixture loader, scenario struct.
  - `match.go` — exact / shape / golden matchers with sentinel parser
    (`<string,non-empty>` etc.).
  - `report.go` — human + JSON reporters.
  - `bundled.go` — generates the generic scenarios as in-memory structs
    (so they ship with the binary, no embed surprises).
  - `*_test.go` — use `internal/plugin/testdata/mock_plugin.sh` and
    friends as plugins-under-test; assert the harness itself.
- `internal/plugin/testdata/scenarios/` — if we want bundled scenarios
  as YAML on disk with `//go:embed`, place them here. (Alt: inline in
  `bundled.go`. Inlining avoids embed coupling; leave the choice open.)
- `plugins/<name>/testdata/*.yaml` + optional `*.golden.json` — per-plugin
  fixtures. Four seed scenarios required (see AC 10).

### Patterns already in the tree to match

- **Flag parsing / usage**: see `runPluginAdd` and `runPluginList` —
  `flag.NewFlagSet("plugin test", flag.ContinueOnError)`, `--config`
  flag, `--json` flag, exit codes 0/1/2.
- **Project-root resolution**: `resolveProjectRoot` handles both
  `--config` and cwd discovery — reuse verbatim.
- **Starting a single plugin**: the `pluginListCachedDiscover` path
  shows how to extract a plugin from CAS and register it with a
  `Manager`. For `test` we usually want to point straight at a source
  file on disk, skipping CAS — `StartProcess(name, command, projectRoot, workDir)`
  is sufficient.
- **bb resolution**: `resolveBbPath` returns `~/.mu/toolchains/bb/bb`
  or "". When empty, emit the same `--discover requires bb toolchain; run
  mu build first` message for consistency.
- **NDJSON request shapes**: `NewDiscoverRequest`, `NewPlanRequest`,
  `NewObserveRequest`, `NewResolveSecretRequest` already construct all
  four methods. The harness can translate scenario YAML straight to
  these helpers when the shape matches; otherwise marshal a generic
  `Request`.

### Example plugin targets (real NDJSON dialects)

- `plugins/host/plugin.bb` — discover + observe (SSH).
- `plugins/aws/plugin.bb` — discover + observe (AWS CLI).
- `plugins/pass/plugin.bb` — discover + resolve_secret (secret provider).
- `plugins/cowsay/plugin.bb` — discover + plan (classic build plugin).
- `plugins/file/plugin.bb` — plan, minimal.
- `plugins/go/plugin.bb` — plan + produces artifacts.
- `plugins/docker/plugin.bb`, `plugins/k8s/plugin.bb`,
  `plugins/terraform/plugin.bb`, `plugins/zig/plugin.bb`,
  `plugins/lint/plugin.bb`, `plugins/scratch/plugin.bb` — further
  coverage targets. Seeding fixtures for all of them is out of scope
  for v1; AC 10 sets a four-plugin minimum.

## Scope Boundaries

- **Out of scope**: coverage metrics (% of capabilities exercised);
  property-based / fuzzing mode; parallel scenario execution
  (serial is fine for a dozen scenarios); mocking the secret-provider
  chain for plan scenarios that reference sealed inputs; a watch mode.
- **Non-goals**: replacing `go test ./internal/plugin/...` — the harness
  exercises real plugin subprocesses, not Go code paths.

## Open Questions

1. **Fixture files for plan/observe scenarios.** Some plugins
   (host, aws) read files on disk or call external CLIs. Should
   scenarios ship a `pre:` block to stage tempfiles, or is it better
   to require authors to structure their plugin so that in-process
   stubs suffice? Proposal: v1 supports a simple `pre.files: {"path":
   "body"}` plus `env:` overrides; skip mocking external CLIs (that's
   the author's problem — use `pass` or a mock binary on `PATH`).

2. **Secret resolution in tests.** For plan scenarios with
   `sealed_inputs`, should the harness inject a canned secret map, or
   spawn the real `pass` plugin? Proposal: accept `secrets:` inline
   in scenario YAML; never shell out to real secret providers from
   tests.

3. **Scenario discovery location.** `plugins/<name>/testdata/*.yaml`
   is author-local; `internal/plugin/testdata/scenarios/` is bundled.
   Do we also support `.mu/scenarios/` at project root for
   project-specific scenarios that span plugins? Defer to v2.

4. **Parallel runs across plugins.** `mu plugin test ./plugins/...`
   could fan out. Probably not worth the complexity for v1 — typical
   run is < 5s serially.

5. **YAML dependency.** mu currently has no YAML parser in `go.mod`.
   Options: (a) add `gopkg.in/yaml.v3`; (b) JSON-only scenarios. JSON
   is less friendly for hand-authoring but avoids the dep. Proposal:
   accept both, pick a small YAML lib, document JSON as the fallback.

6. **Diff renderer.** Writing one from scratch vs. importing
   `github.com/google/go-cmp`. The existing tree uses `go-cmp` in
   tests already; reusing it for human output (with `cmp.Diff`) gives
   stable, Go-idiomatic diffs. Proposal: import `go-cmp`, wrap for
   pretty stderr rendering.

7. **Capability-aware scenario selection semantics.** Should missing
   capabilities be `skip` or `pass`? Treating them as `skip` is honest
   but means a green run could hide zero coverage. Proposal: `skip`
   with a visible `(skipped: capability not declared)` note, and a
   final summary line `N skipped`.

8. **Risograph / TUI output.** Out of scope for v1 per the brief; the
   reporter interface (`Reporter` in `report.go`) should be a small
   interface (`StartPlugin`, `Result`, `Summary`) so a future riso-print
   or bubbletea skin is a drop-in.

## Recommended Implementation Order

1. Scaffolding: `runPluginTest` stub + flag parsing + usage update.
2. `internal/plugintest`: scenario loader (JSON first), exact matcher,
   subprocess runner that reuses `plugin.StartProcess`.
3. Bundled `discover_ok` + `discover_version_pin` + `unknown_method`.
4. YAML loader + shape matcher + sentinel parser.
5. Golden matcher + `--update`.
6. `--json` reporter + exit-code contract.
7. `./plugins/...` glob expansion.
8. Seed fixtures for host/aws/pass/cowsay; update their `GUIDE.md`.
9. Harness unit tests against `internal/plugin/testdata/mock_*.sh`.
10. Documentation: new top-level section in `README.md`, and a
    `docs/plugins/testing.md` walkthrough.
