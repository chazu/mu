---
title: "feat: BRICK Ecosystem Integration — Convergence, Observation, and Feedback Loop"
type: feat
date: 2026-03-24
---

# feat: BRICK Ecosystem Integration — Convergence, Observation, and Feedback Loop

## Overview

Transform mu from a pure build coordinator into a full participant in the
BRICK/ACUTE feedback loop. mu already handles the Execute phase — this work
adds the ability to plan without executing (dry-run), report what was done
(manifest), observe current state (drift detection), and converge
infrastructure via shell, Kubernetes, and Terraform plugins. Combined with
pudl's CUE-based desired-state and drift-detection capabilities, mu becomes
the execution engine in a self-refining infrastructure cycle:

```
pudl (CUE schemas) ──► export mu.json ──► mu build --emit-manifest
                                                │
                                                ▼
                                          manifest.json
                                                │
pudl ingest ◄───────────────────────────────────┘
     │
     ▼
pudl drift check ──► mu observe //targets
                           │
                           ▼
                     drifted? ──► mu build ──► converged
```

## Problem Statement / Motivation

mu can build software, but it cannot:
1. Show what it *would* do without doing it (no dry-run)
2. Report what it *did* in a structured format (no manifest output)
3. Check whether the world matches desired state (no observation)
4. Converge arbitrary infrastructure (only file and build plugins exist)

These gaps prevent mu from participating in the ACUTE cycle where pudl
defines desired state in CUE, detects drift, and exports convergence targets.
Without these capabilities, the feedback loop between "what should exist" and
"what actually exists" remains open.

## Proposed Solution

Six additions across the coordinator, protocol, CLI, and plugins:

1. **`--plan` flag** — show the action DAG without executing
2. **`--emit-manifest` flag** — emit structured JSON of what was done
3. **Shell plugin** — universal escape hatch for arbitrary commands
4. **`observe` protocol method** — plugins report current state vs desired
5. **k8s convergence plugin** — kubectl apply/diff via NDJSON
6. **Terraform convergence plugin** — terraform init/plan/apply via NDJSON

## Technical Approach

### Architecture

```
                    ┌──────────────────────────────────────┐
                    │           mu (the binary)            │
                    │                                      │
  mu.json ─────────►  Config Loader                       │
                    │  (parse, validate)                   │
                    │         │                            │
                    │         ▼                            │
                    │  ┌─────────────────────────┐         │
                    │  │     Coordinator          │         │
                    │  │                          │         │
                    │  │  Plan()                  │         │
                    │  │  ├─ Build from scratch   │         │
                    │  │  ├─ Start plugins        │         │
                    │  │  ├─ Resolve target graph │         │
                    │  │  ├─ Plan each target     │         │
                    │  │  └─ Return PlanResult    │         │
                    │  │       │                  │         │
                    │  │  Execute(PlanResult)     │         │
                    │  │  ├─ Run DAG              │         │
                    │  │  └─ Return BuildResult   │         │
                    │  │       │                  │         │
                    │  │  Observe()               │ NEW     │
                    │  │  ├─ Start plugins        │         │
                    │  │  └─ Send observe to each │         │
                    │  │       │                  │         │
                    │  │  EmitManifest()          │ NEW     │
                    │  │  └─ Serialize results    │         │
                    │  └─────────────────────────┘         │
                    │         │                            │
                    │         ▼                            │
                    │  ┌──────────────┐                    │
                    │  │     CAS      │                    │
                    │  │  (OCI store) │                    │
                    │  └──────────────┘                    │
                    └──────────────────────────────────────┘
                              │
              ┌───────────────┼───────────────────┐
              ▼               ▼                   ▼
         go.bb           shell (built-in)    k8s.bb / tf.bb
        [plan]           [plan]              [plan, observe]
```

### Design Decisions

**D1: CAS caching for impure convergence actions.**
Convergence actions (kubectl apply, terraform apply, shell commands with side
effects) are inherently impure — their outputs depend on external state, not
just their inputs. Add an `impure` field to ActionSpec and dag.Action. When
true, the executor skips cache lookup and storage. Action key is still computed
for logging. Convergence plugins set `impure: true` on their actions.

**D2: observe fallback for existing plugins.**
Existing plugins return `{"error": "unknown method: observe"}` for
unrecognized methods. The coordinator catches error responses from observe
calls and translates them to `{"state": "unknown"}`. This is
backwards-compatible — no existing plugins need updating.

**D3: Shell plugin is a Go built-in.**
The shell plugin bypasses the Babashka plugin system entirely. The coordinator
synthesizes ActionSpec directly from the target config. This avoids requiring
the bb toolchain for the simplest use case — running a shell command. If only
shell-plugin targets are configured, no bb toolchain is needed.

**D4: `--plan` and `--dry-run` are synonyms.**
Both flags map to the same boolean. `--dry-run` is the alias. Plan output goes
to stdout (for piping), build status messages go to stderr.

**D5: `--plan` and `--emit-manifest` are mutually exclusive.**
`--plan` skips execution, so there is nothing to manifest. mu prints an error
and exits 2 if both are specified.

**D6: observe exit codes.**
Exit 0 if all observed targets are converged or unknown. Exit 1 if any target
is drifted. Exit 2 for usage errors. Targets returning "unknown" are reported
but do not trigger a non-zero exit code.

**D7: Terraform auto_approve semantics.**
When `auto_approve` is false (or unset), the terraform plugin emits only
`terraform init` + `terraform plan` — no apply action. This avoids hanging on
interactive prompts. When `auto_approve` is true, the full init → plan → apply
chain is emitted.

**D8: Manifest is always JSON, versioned from day one.**
`--emit-manifest` writes JSON to stdout. The schema includes a `version` field
(starting at 1) so pudl can evolve its parser independently.

**D9: Plan/Execute split returns PlanResult with cleanup.**
`Coordinator.Plan()` returns a `PlanResult` containing the `*dag.Graph` and a
cleanup function that shuts down plugins. The caller is responsible for
cleanup. For `--plan` mode, cleanup runs immediately after printing. For full
build, cleanup runs after Execute().

**D10: ActionStatus carries output digests.**
Extend `dag.ActionStatus` to include `Outputs map[string]cas.Digest`. The
executor populates this from the action result before returning. This enables
manifest generation without CAS re-queries.

---

## Implementation Phases

### Phase 1: Plan/Execute Split + `--plan` Flag

**Goal:** Refactor `Coordinator.Build()` into `Plan()` + `Execute()`. Add
`--plan` (alias `--dry-run`) flag to `mu build` that prints the action DAG
without executing.

**Files:**
- `internal/coordinator/coordinator.go` — split Build(), add PlanResult type
- `cmd/mu/build.go` — add `--plan`/`--dry-run` flags, print DAG

**PlanResult type:**
```go
// PlanResult holds the planned action graph and resources for execution.
type PlanResult struct {
    Graph   *dag.Graph
    Manager *plugin.Manager // running plugin processes
    Cleanup func()          // shuts down plugins
}
```

**Coordinator changes:**
```go
// Plan runs steps 1-4: toolchain build, plugin resolve, start plugins,
// resolve targets, and plan each target. Returns the merged DAG.
func (c *Coordinator) Plan(ctx context.Context, targets []string) (*PlanResult, error)

// Execute runs the planned DAG. Caller must call result.Cleanup() after.
func (c *Coordinator) Execute(ctx context.Context, plan *PlanResult) (*BuildResult, error)

// Build is the existing convenience method: Plan() + Execute().
func (c *Coordinator) Build(ctx context.Context, targets []string) (*BuildResult, error)
```

The split point is at coordinator.go line 138 — everything before is planning,
lines 139-149 are execution.

**CLI changes (build.go):**
```go
plan := fs.Bool("plan", false, "show planned actions without executing")
dryRun := fs.Bool("dry-run", false, "alias for --plan")
jsonOut := fs.Bool("json", false, "output as JSON")
```

When `--plan` is set:
1. Call `c.Plan(ctx, targets)`
2. Print the DAG to stdout (human-readable or JSON)
3. Call `result.Cleanup()`
4. Exit 0

**Human-readable plan output:**
```
mu build --plan //cmd/server
  planned 3 actions for 1 target

  //cmd/server:mod-download
    command:  go mod download
    inputs:   go.mod (sha256:abc...), go.sum (sha256:def...)
    outputs:  (none)
    network:  true

  //cmd/server:build
    command:  go build -trimpath -o=server .
    inputs:   go.mod, go.sum, cmd/server/main.go
    outputs:  server
    depends:  //cmd/server:mod-download
    network:  false
```

**JSON plan output (`--plan --json`):**
```json
{
  "targets": ["//cmd/server"],
  "actions": [
    {
      "id": "//cmd/server:mod-download",
      "command": ["go", "mod", "download"],
      "inputs": {"go.mod": "sha256:abc...", "go.sum": "sha256:def..."},
      "outputs": [],
      "depends_on": [],
      "network": true,
      "impure": false
    }
  ]
}
```

**Tests:**
- `--plan` prints DAG without executing any actions
- `--plan --json` produces valid JSON parseable by jq
- `--plan` with `--emit-manifest` errors with exit 2
- `--dry-run` is equivalent to `--plan`
- Plan with missing target errors with exit 2
- Plan with failing plugin errors with exit 1

**Acceptance criteria:**
- [ ] `Coordinator.Plan()` and `Coordinator.Execute()` exist as separate methods
- [ ] `Coordinator.Build()` delegates to Plan() + Execute()
- [ ] `mu build --plan //target` prints action DAG to stdout
- [ ] `mu build --plan --json //target` emits valid JSON
- [ ] `--dry-run` is an alias for `--plan`
- [ ] `--plan` + `--emit-manifest` is rejected with exit 2
- [ ] Plugin processes are cleaned up after plan-only mode
- [ ] All existing tests still pass (Build() behavior unchanged)
- [ ] Tests pass

---

### Phase 2: Manifest Output + ActionStatus Extension

**Goal:** Extend `dag.ActionStatus` to carry output digests. Add
`--emit-manifest` flag that emits structured JSON after successful build.

**Files:**
- `internal/dag/executor.go` — extend ActionStatus with Outputs
- `internal/coordinator/manifest.go` — new file: Manifest type + serialization
- `cmd/mu/build.go` — add `--emit-manifest` flag

**ActionStatus extension (executor.go):**
```go
type ActionStatus struct {
    ID       string
    Cached   bool
    ExitCode int
    Err      error
    Outputs  map[string]cas.Digest // NEW: populated from action result
}
```

Populate in `executeAction()` after storing outputs in CAS — copy the
output digests from the ActionResult into the ActionStatus before returning.

**Manifest type (manifest.go):**
```go
// Manifest is the structured output of a mu build, designed to be consumed
// by external tools (e.g., pudl) as Application-layer data.
type Manifest struct {
    Version   int              `json:"version"`            // schema version (1)
    Timestamp string           `json:"timestamp"`          // ISO 8601
    Duration  string           `json:"duration"`           // build wall-clock time
    Targets   []TargetResult   `json:"targets"`
    Actions   []ActionResult   `json:"actions"`
    Summary   ManifestSummary  `json:"summary"`
}

type TargetResult struct {
    Name      string            `json:"name"`              // e.g. "//cmd/server"
    Toolchain string            `json:"toolchain"`
    State     string            `json:"state"`             // "completed", "failed", "cached"
    Outputs   map[string]string `json:"outputs,omitempty"` // artifact type -> digest
}

type ActionResult struct {
    ID        string            `json:"id"`
    Cached    bool              `json:"cached"`
    ExitCode  int               `json:"exit_code"`
    Outputs   map[string]string `json:"outputs,omitempty"` // output name -> digest
    Impure    bool              `json:"impure,omitempty"`
}

type ManifestSummary struct {
    Completed int `json:"completed"`
    Cached    int `json:"cached"`
    Failed    int `json:"failed"`
    Cancelled int `json:"cancelled"`
}

func NewManifest(result *BuildResult, elapsed time.Duration) *Manifest
```

**CLI changes (build.go):**
```go
emitManifest := fs.Bool("emit-manifest", false, "emit build manifest as JSON to stdout")
```

When `--emit-manifest` is set:
1. Redirect all build status output to stderr (already the case)
2. Run build normally
3. On success (or partial success), construct Manifest from BuildResult
4. Serialize to stdout as indented JSON
5. Exit code reflects build success/failure as normal

**Build status goes to stderr, manifest goes to stdout:**
```bash
mu build --emit-manifest //cmd/server 2>build.log >manifest.json
# or pipe directly to pudl:
mu build --emit-manifest //cmd/server | pudl ingest
```

**Tests:**
- `--emit-manifest` produces valid JSON on stdout
- Manifest contains correct target names and toolchains
- Manifest action entries include output digests
- Manifest summary counts match actual build results
- Cached actions are correctly flagged
- Failed build does not emit manifest (exit 1, no stdout)
- Manifest version field is 1
- Timestamp is valid ISO 8601

**Acceptance criteria:**
- [ ] `ActionStatus` carries `Outputs map[string]cas.Digest`
- [ ] `Manifest` type defined with versioned schema
- [ ] `mu build --emit-manifest //target` emits manifest JSON to stdout
- [ ] Manifest includes per-action output digests
- [ ] Build status messages go to stderr
- [ ] Failed builds produce no manifest output
- [ ] `NewManifest` correctly populates from BuildResult
- [ ] Tests pass

---

### Phase 3: Impure Actions + Shell Plugin

**Goal:** Add `impure` field to action types (skips CAS cache for convergence
actions). Implement the shell plugin as a Go built-in in the coordinator.

**Files:**
- `internal/plugin/protocol.go` — add `Impure` to ActionSpec
- `internal/dag/graph.go` — add `Impure` to Action
- `internal/dag/executor.go` — skip cache for impure actions
- `internal/dag/actionkey.go` — include impure in key (for logging)
- `internal/coordinator/shell.go` — new file: built-in shell plugin
- `internal/coordinator/coordinator.go` — route shell targets to built-in
- `internal/coordinator/resolve.go` — handle shell actions in Resolve()

**ActionSpec extension (protocol.go):**
```go
type ActionSpec struct {
    ID        string            `json:"id"`
    Command   []string          `json:"command"`
    Inputs    map[string]string `json:"inputs"`
    Outputs   []string          `json:"outputs"`
    DependsOn []string          `json:"depends_on,omitempty"`
    Env       map[string]string `json:"env,omitempty"`
    Network   bool              `json:"network,omitempty"`
    WorkDir   string            `json:"work_dir,omitempty"`
    Impure    bool              `json:"impure,omitempty"`  // NEW
}
```

**Action extension (graph.go):**
```go
type Action struct {
    // ... existing fields ...
    Impure bool // skip CAS cache when true
}
```

**Executor change (executor.go, in executeAction):**
```go
// Before execution, check cache — but only for pure actions.
if !action.Impure {
    if result, err := e.Store.GetActionResult(ctx, actionKey); err == nil && result != nil {
        // cache hit — restore outputs, return cached status
    }
}

// After execution — store result only for pure actions.
if !action.Impure {
    e.Store.PutActionResult(ctx, actionKey, &result)
}
```

**Shell plugin (shell.go):**
```go
// ShellPlan synthesizes an ActionSpec from a shell target's config.
// No external plugin process is needed.
func ShellPlan(target config.Target) ([]plugin.ActionSpec, map[string]string, error)
```

Shell target config schema:
```json
{
  "target": "//infra/deploy",
  "toolchain": "shell",
  "sources": ["deploy.sh", "config.yaml"],
  "config": {
    "command": ["./deploy.sh", "--env", "production"],
    "env": {"AWS_REGION": "us-east-1"},
    "network": true,
    "impure": true,
    "outputs": ["result.json"]
  }
}
```

Shell plugin behavior:
- `config.command` is `[]string` (required). If a single string is provided,
  wrap as `["sh", "-c", <string>]`.
- `config.env` is `map[string]string` (optional, merged with action env).
- `config.network` is bool (optional, default false).
- `config.impure` is bool (optional, default true for shell targets).
- `config.outputs` is `[]string` (optional, declared output files).
- Sources from the target become action inputs (hashed for key computation).
- Single action emitted with ID `"run"`.

**Coordinator routing (coordinator.go):**
In the planning loop, before sending to the plugin manager:
```go
if target.Toolchain == "shell" {
    actions, outputs, err := ShellPlan(target)
    // ... add to graph ...
    continue
}
// Otherwise: send to plugin manager as before
```

**Tests:**
- Impure actions skip CAS cache lookup
- Impure actions skip CAS cache storage
- Pure actions (existing behavior) still cache normally
- Shell target with `command: ["echo", "hello"]` emits correct action
- Shell target with string command wraps in sh -c
- Shell target with env merges into action env
- Shell target with network: true sets action network flag
- Shell target with sources hashes them as inputs
- Shell target with outputs declares them
- Shell target defaults to impure: true
- Shell target without bb toolchain configured works
- No regression in existing plugin-based targets

**Acceptance criteria:**
- [ ] `ActionSpec.Impure` field exists and propagates to `dag.Action`
- [ ] Executor skips cache for impure actions
- [ ] `ShellPlan()` synthesizes ActionSpec from target config
- [ ] Coordinator routes `toolchain: "shell"` to built-in handler
- [ ] Shell targets work without bb toolchain
- [ ] String commands are wrapped in `sh -c`
- [ ] Default impure: true for shell targets
- [ ] Tests pass

---

### Phase 4: Observe Protocol Method + `mu observe` CLI

**Goal:** Add `observe` as a third NDJSON method alongside `discover` and
`plan`. Add `mu observe` CLI command. Coordinator handles graceful fallback
for plugins that don't support observe.

**Files:**
- `internal/plugin/protocol.go` — add ObserveResponse type, NewObserveRequest
- `internal/plugin/manager.go` — add Observe() method
- `internal/plugin/process.go` — add sendObserve() with error fallback
- `internal/coordinator/coordinator.go` — add Observe() method
- `cmd/mu/observe.go` — new file: mu observe command
- `cmd/mu/main.go` — wire observe subcommand

**Protocol types (protocol.go):**
```go
// ObserveResponse is returned by plugins for method "observe".
type ObserveResponse struct {
    State   string         `json:"state"`             // "converged", "drifted", "unknown"
    Current map[string]any `json:"current,omitempty"` // plugin-specific current state
    Diff    string         `json:"diff,omitempty"`    // human-readable diff (kubectl diff, terraform plan output)
    Error   string         `json:"error,omitempty"`
}

func NewObserveRequest(target *TargetInfo, toolchainArtifacts map[string]string) *Request {
    return &Request{
        Method:             "observe",
        Target:             target,
        ToolchainArtifacts: toolchainArtifacts,
    }
}
```

The `observe` method reuses the existing `Request` struct (Method, Target,
ToolchainArtifacts). The `Deps` field is unused for observe.

**Manager.Observe() (manager.go):**
```go
// Observe sends an observe request to the plugin that handles the given
// toolchain. Returns ObserveResponse. If the plugin does not support
// observe (returns an error response), returns {State: "unknown"}.
func (m *Manager) Observe(ctx context.Context, toolchain string, req *Request) (*ObserveResponse, error)
```

**Graceful fallback (process.go):**
```go
func (p *Process) sendObserve(ctx context.Context, req *Request) (*ObserveResponse, error) {
    // Send request, read response.
    // If response contains "error" key matching "unknown method",
    // return &ObserveResponse{State: "unknown"}, nil
    // Otherwise parse as ObserveResponse.
}
```

Timeout for observe: 5 minutes (same as plan — observe may run kubectl diff
or terraform plan which can be slow).

**Coordinator.Observe() (coordinator.go):**
```go
// ObserveResult holds per-target observation results.
type ObserveResult struct {
    Target string
    State  string // "converged", "drifted", "unknown"
    Diff   string // human-readable diff if drifted
}

// Observe checks the current state of the given targets by sending
// observe requests to their plugins.
func (c *Coordinator) Observe(ctx context.Context, targets []string) ([]ObserveResult, error)
```

Flow:
1. Build toolchains from scratch (needed for plugin runtime)
2. Resolve plugins, start plugin manager
3. Resolve target graph
4. For each target: send observe request to its plugin
5. Collect results
6. Shut down plugins

For shell targets (built-in): return `{State: "unknown"}` — shell commands
have no observation capability unless a separate observe command is configured.

**CLI (observe.go):**
```go
func runObserve(args []string) int
```

Flags: `--json`, `--config`, `--verbose`

**Human-readable output:**
```
mu observe //k8s/api //k8s/worker
  //k8s/api        converged
  //k8s/worker     drifted
    - replicas: 3
    + replicas: 2

1 converged, 1 drifted
```

**Exit codes:** 0 if all converged/unknown. 1 if any drifted. 2 for usage error.

**Tests:**
- Observe with plugin that supports observe returns state
- Observe with plugin that doesn't support observe returns "unknown"
- Observe with drifted target exits 1
- Observe with all converged exits 0
- Observe with mix of converged and unknown exits 0
- Observe --json produces valid JSON
- Observe with missing target errors
- Plugin crash during observe produces error (not panic)
- Shell targets return "unknown" for observe
- Timeout kills plugin, returns error

**Acceptance criteria:**
- [ ] `ObserveResponse` type defined in protocol.go
- [ ] `Manager.Observe()` sends observe request to correct plugin
- [ ] Graceful fallback: error responses become `{State: "unknown"}`
- [ ] `Coordinator.Observe()` orchestrates full observe pipeline
- [ ] `mu observe //target` prints state to stdout
- [ ] Exit 0 for converged, 1 for drifted, 2 for usage error
- [ ] `--json` output supported
- [ ] Existing plugins are not broken (no update required)
- [ ] Shell targets return "unknown"
- [ ] Tests pass

---

### Phase 5: Kubernetes Convergence Plugin

**Goal:** Babashka plugin that converges Kubernetes resources via kubectl.
Supports plan (apply), observe (diff), and impure actions.

**Files:**
- `plugins/k8s/plugin.bb` — new plugin
- `examples/k8s/mu.json` — example configuration

**Config schema:**
```json
{
  "target": "//k8s/api-deployment",
  "toolchain": "k8s",
  "sources": ["manifests/api-deployment.yaml"],
  "config": {
    "namespace": "production",
    "context": "prod-cluster",
    "kubeconfig": "~/.kube/config",
    "server_side": true,
    "prune": false,
    "dry_run": false
  }
}
```

| Config field | Type | Default | Description |
|-------------|------|---------|-------------|
| `namespace` | string | (from manifest) | Kubernetes namespace |
| `context` | string | (current) | kubectl context |
| `kubeconfig` | string | `~/.kube/config` | path to kubeconfig |
| `server_side` | bool | true | use server-side apply |
| `prune` | bool | false | prune resources not in manifest |
| `dry_run` | bool | false | kubectl --dry-run=server |

**discover response:**
```json
{
  "name": "k8s",
  "version": "0.1.0",
  "protocol_version": 1,
  "consumes": ["source:yaml", "source:json"],
  "produces": ["k8s_resource"]
}
```

**plan response:**
Single action: `kubectl apply -f <manifest>` with appropriate flags.

```clojure
(defn handle-plan [req]
  ;; Emits a single impure action: kubectl apply
  ;; network: true (talks to API server)
  ;; impure: true (external side effects)
  ...)
```

Action: `kubectl apply -f <manifest> --namespace <ns> --context <ctx>`
- `network: true`
- `impure: true`
- Inputs: the manifest file(s) from sources
- Outputs: none (side-effect only)
- If `server_side: true`: add `--server-side`
- If `dry_run: true`: add `--dry-run=server`

**observe response:**
Run `kubectl diff -f <manifest>` to check for drift.

```clojure
(defn handle-observe [req]
  ;; Run kubectl diff -f <manifest>
  ;; Exit 0 = no diff = converged
  ;; Exit 1 = diff exists = drifted (capture diff output)
  ;; Exit >1 = error (resource doesn't exist, etc.)
  ...)
```

Return:
- `{"state": "converged"}` if kubectl diff exits 0
- `{"state": "drifted", "diff": "<kubectl diff output>"}` if exits 1
- `{"state": "drifted", "diff": "resource does not exist"}` if resource
  is absent (new deployment)

**Edge cases:**
- Multiple resources in one YAML (separated by `---`): single kubectl apply
  handles this natively.
- Resource doesn't exist yet: kubectl diff fails, treat as drifted.
- kubeconfig not found: return error response.
- Context not found: return error response.

**Tests:**
- discover returns correct metadata
- plan emits kubectl apply action with correct flags
- plan respects namespace, context, kubeconfig config
- plan sets network: true, impure: true
- plan with server_side: true adds --server-side flag
- plan with dry_run: true adds --dry-run=server flag
- observe returns converged when kubectl diff exits 0
- observe returns drifted with diff when kubectl diff exits 1
- plan includes manifest sources as inputs

**Acceptance criteria:**
- [ ] k8s plugin responds to discover, plan, and observe methods
- [ ] kubectl apply action has network: true, impure: true
- [ ] observe uses kubectl diff for drift detection
- [ ] All config fields respected in emitted commands
- [ ] Example mu.json provided
- [ ] Tests pass

---

### Phase 6: Terraform Convergence Plugin

**Goal:** Babashka plugin that converges Terraform-managed infrastructure.
Supports plan (init + plan + apply chain), observe (plan for drift), and
impure actions.

**Files:**
- `plugins/terraform/plugin.bb` — new plugin
- `examples/terraform/mu.json` — example configuration

**Config schema:**
```json
{
  "target": "//infra/vpc",
  "toolchain": "terraform",
  "sources": ["infra/vpc/main.tf", "infra/vpc/variables.tf"],
  "config": {
    "dir": "infra/vpc",
    "var_file": "prod.tfvars",
    "backend_config": {"bucket": "tf-state", "key": "vpc/terraform.tfstate"},
    "auto_approve": true,
    "parallelism": 10
  }
}
```

| Config field | Type | Default | Description |
|-------------|------|---------|-------------|
| `dir` | string | `.` | Terraform working directory |
| `var_file` | string | (none) | Path to .tfvars file |
| `backend_config` | map | (none) | Backend config key=value pairs |
| `auto_approve` | bool | true | Whether to include apply step |
| `parallelism` | int | (terraform default) | Max concurrent operations |

**discover response:**
```json
{
  "name": "terraform",
  "version": "0.1.0",
  "protocol_version": 1,
  "consumes": ["source:terraform", "source:hcl"],
  "produces": ["terraform_state"]
}
```

**plan response (auto_approve: true):**
Three-action chain: init → plan → apply

```
terraform-init ──► terraform-plan ──► terraform-apply
```

- All actions: `network: true`, `impure: true`
- `work_dir` set to `config.dir`
- init: `terraform init -input=false [-backend-config=key=value ...]`
- plan: `terraform plan -input=false -out=tfplan [-var-file=X] [-parallelism=N]`
  - Depends on: init
  - Output: `tfplan` (plan file)
- apply: `terraform apply -input=false -auto-approve tfplan [-parallelism=N]`
  - Depends on: plan
  - Input: `tfplan` from plan step

**plan response (auto_approve: false):**
Two-action chain: init → plan (no apply). This is a plan-only mode.

**observe response:**
Run `terraform plan -detailed-exitcode` in the target directory.

```clojure
(defn handle-observe [req]
  ;; Run terraform init (quiet) then terraform plan -detailed-exitcode
  ;; Exit 0 = no changes = converged
  ;; Exit 2 = changes detected = drifted (capture plan output)
  ;; Exit 1 = error
  ...)
```

Observation requires running init first (to configure backend), then plan.
The observe handler runs both as subprocess calls within the plugin process
(not as mu actions) to keep the response self-contained.

Return:
- `{"state": "converged"}` if plan exits 0
- `{"state": "drifted", "diff": "<terraform plan output>"}` if exits 2
- Error response if exits 1

**Edge cases:**
- `.terraform/` directory management: terraform init creates `.terraform/`
  with provider binaries. Since actions run in the project directory (not
  sandboxed for impure actions), this persists between runs. If sandboxing
  is enabled later, init must re-download providers each time.
- State locking: terraform handles state locking internally. Parallel mu
  actions targeting the same state will be serialized by terraform's lock.
- Backend credentials: rely on ambient credentials (AWS_PROFILE, etc.).
  These should be in the target's `config.env` or the shell environment.

**Tests:**
- discover returns correct metadata
- plan with auto_approve: true emits 3-action chain (init → plan → apply)
- plan with auto_approve: false emits 2-action chain (init → plan, no apply)
- plan actions have correct dependency order
- plan respects var_file, backend_config, parallelism
- All actions have network: true, impure: true
- observe returns converged when terraform plan exits 0
- observe returns drifted when terraform plan exits 2
- work_dir is set to config.dir on all actions

**Acceptance criteria:**
- [ ] terraform plugin responds to discover, plan, and observe methods
- [ ] Auto-approve controls whether apply action is emitted
- [ ] Actions form correct dependency chain
- [ ] All actions have network: true, impure: true
- [ ] observe uses terraform plan -detailed-exitcode for drift detection
- [ ] Backend config flags correctly passed to init
- [ ] Example mu.json provided
- [ ] Tests pass

---

## Alternative Approaches Considered

**Shell plugin as Babashka script instead of Go built-in.** Rejected — would
require bb toolchain for the simplest use case, defeating the "escape hatch"
purpose. Users who only need shell commands shouldn't need to configure
Babashka.

**observe as a flag on build (`mu build --observe`) instead of a separate
subcommand.** Rejected — observe has fundamentally different semantics (read
vs write). Conflating them in one command creates confusing flag interactions.

**Structured diff format instead of string.** Considered — would enable pudl
to parse diffs programmatically. Rejected for v1 — diff formats are
tool-specific (kubectl diff vs terraform plan), and forcing a common schema
adds complexity without clear benefit. The diff string is sufficient for
human review and pudl can treat it as opaque metadata.

**Always cache convergence actions with TTL.** Considered — would allow
short-circuiting repeated convergence within a time window. Rejected —
introduces temporal coupling and false confidence. Impure flag is simpler
and more honest.

**gRPC for observe instead of NDJSON.** Rejected — adds proto dependency,
inconsistent with existing protocol. NDJSON request/response works fine.

## Acceptance Criteria

### Functional Requirements

- [ ] `mu build --plan //target` shows DAG without executing
- [ ] `mu build --emit-manifest //target` emits structured JSON manifest
- [ ] Shell targets (`toolchain: "shell"`) work without Babashka
- [ ] `mu observe //target` reports converged/drifted/unknown state
- [ ] k8s plugin converges Kubernetes resources via kubectl
- [ ] terraform plugin converges Terraform infrastructure
- [ ] Convergence actions (impure) bypass CAS cache
- [ ] Existing plugins/builds are unaffected (backward compatible)

### Non-Functional Requirements

- [ ] `--plan` completes within 5s for projects with cached toolchains
- [ ] Manifest JSON is <1KB for simple builds (not bloated)
- [ ] observe timeout is 5 minutes (matches plan timeout)
- [ ] Shell plugin adds zero external dependencies

### Quality Gates

- [ ] All packages have tests
- [ ] `go vet` clean
- [ ] No data races (`go test -race`)
- [ ] Example configs for k8s and terraform plugins
- [ ] JSON schemas are versioned

## Dependencies & Prerequisites

- Go 1.25+ (already in go.mod)
- Babashka (for k8s and terraform plugins — already a toolchain)
- kubectl (for k8s plugin — user-provided, not managed by mu)
- terraform (for terraform plugin — user-provided, not managed by mu)
- No new Go dependencies for core changes (shell plugin, plan/execute split,
  manifest, impure actions)

## Risk Analysis & Mitigation

| Risk | Likelihood | Impact | Mitigation |
|------|-----------|--------|------------|
| Impure cache bypass confuses users | Medium | Low | Clear output: "skipped cache (impure action)" |
| terraform state corruption | Low | High | Actions run in project dir, terraform handles locking |
| kubectl diff unreliable for new resources | Medium | Medium | Treat exit >1 as "drifted" with clear messaging |
| Plan/Execute split breaks Build() | Low | High | Build() delegates to Plan() + Execute(), tested |
| Manifest schema needs revision | High | Low | Versioned from day one, pudl can adapt |
| Shell injection via string commands | Medium | Medium | Require []string only, no sh -c wrapping |

## Future Considerations (Post-implementation)

- **NDJSON build events** — `--build-event-file` or stderr NDJSON stream
  for real-time action progress (inspired by Bazel BEP).
- **Convergence plan output** — `mu build --plan` for convergence targets
  could show "what would be applied" by running terraform plan / kubectl diff
  as part of planning, not just showing the command.
- **Observe in CI** — `mu observe --json //... | pudl ingest` as a cron job
  for continuous drift detection.
- **Impure action dedup** — if multiple targets trigger the same impure
  action (e.g., same kubectl apply), deduplicate.
- **Manifest diffing** — `mu manifest diff old.json new.json` to see what
  changed between builds.
- **Remote convergence** — run convergence actions on remote machines
  (e.g., SSH to a bastion, then kubectl apply).
- **Management policies** — per-target `management_policy` field restricting
  lifecycle operations (e.g., `["observe"]` for read-only,
  `["observe", "create", "update"]` for no-destroy).
- **TTL-based caching** — middle ground between always-cache and never-cache
  for convergence actions that are expensive but time-bounded.
- **`mu converge`** — convenience command combining observe + selective build
  of drifted targets in one invocation.

## References & Research

### Internal
- Plan: `docs/plans/2026-02-18-feat-mu-v1-build-coordinator-plan.md`
- Architecture: `docs/architecture/mu-conceptual-model.md`
- pudl integration: `docs/pudl-integration.md`
- Go plugin design: `docs/brainstorms/2026-02-28-go-toolchain-plugin-design.md`

### Key Files
- Coordinator: `internal/coordinator/coordinator.go:42-150`
- DAG executor: `internal/dag/executor.go:40-199`
- Plugin protocol: `internal/plugin/protocol.go:10-59`
- CLI build: `cmd/mu/build.go:20-132`
- Action key: `internal/dag/actionkey.go:21-53`
- Resolve: `internal/coordinator/resolve.go:23-83`

### External
- BRICK/ACUTE model: `/Users/chazu/opp/defn-dev/m/c/README.md`
- IDEA ontology: `/Users/chazu/opp/defn-dev/IDEA.md`
- kubectl diff: https://kubernetes.io/docs/reference/kubectl/generated/kubectl_diff/
- terraform plan -detailed-exitcode: https://developer.hashicorp.com/terraform/cli/commands/plan
- Bazel BEP: https://bazel.build/remote/bep
- Terraform JSON format: https://developer.hashicorp.com/terraform/internals/json-format
- Crossplane managed reconciler: https://pkg.go.dev/github.com/crossplane/crossplane-runtime/pkg/reconciler/managed
- Pulumi provider protocol: https://www.pulumi.com/docs/iac/guides/building-extending/providers/implementers/protocol-reference/
- Google AIP-180 backward compat: https://google.aip.dev/180
- Nix impure derivations: https://nix.dev/manual/nix/2.30/development/experimental-features.html

---

## Enhancement Summary

**Deepened on:** 2026-03-24
**Agents used:** Architecture Strategist, Performance Oracle, Security Sentinel,
Code Simplicity Reviewer, Pattern Recognition Specialist, Agent-Native Reviewer,
Go HPC Architect, Best Practices Researcher (8 parallel agents)

### Design Revisions (from agent feedback)

The following changes to the original plan are recommended based on the
multi-agent review. These supersede the original design decisions where they
conflict.

#### R1: Simplify PlanResult — close plugins inside Plan()

**Original:** `PlanResult` carries `*plugin.Manager` and `Cleanup func()`.
**Revised:** Plugins are only used during planning (confirmed by code review
of `executor.go` — no `mgr` calls during execution). Close plugins at the
end of `Plan()`. `PlanResult` becomes:

```go
type PlanResult struct {
    Graph *dag.Graph
}

func (p *PlanResult) Close() error { return nil } // no-op, plugins already closed
```

On error, `Plan()` cleans up internally (close manager before returning error,
return nil PlanResult). On success, the graph is all that's needed.

**Source:** Go HPC Architect, Code Simplicity Reviewer, Architecture Strategist

#### R2: Remove sh -c string wrapping from shell plugin

**Original:** If `command` is a single string, wrap as `["sh", "-c", <string>]`.
**Revised:** Require `command` to always be `[]string`. No magic wrapping.
Users who want shell interpretation write `["sh", "-c", "echo hello"]`
explicitly.

**Rationale:** The `sh -c` wrapping is a textbook command injection vector
(Security Sentinel rated CRITICAL). Since mu.json can be generated from
upstream CUE via pudl, a compromised CUE input could inject arbitrary
commands. Requiring `[]string` forces explicit intent and prevents shell
metacharacter interpretation.

**Source:** Security Sentinel (Finding 1), Code Simplicity Reviewer

#### R3: Add capabilities to DiscoverResponse

**Original:** Observe fallback matches on error message strings ("unknown method").
**Revised:** Extend `DiscoverResponse` with a `Capabilities []string` field:

```go
type DiscoverResponse struct {
    // ... existing fields ...
    Capabilities []string `json:"capabilities,omitempty"` // e.g. ["discover","plan","observe"]
}
```

The coordinator checks capabilities before sending observe. If `"observe"`
is not listed, skip the call entirely. Old plugins that don't include
`capabilities` default to `["discover", "plan"]`.

**Rationale:** String-matching on error messages is fragile — different plugin
implementations phrase errors differently. Capability negotiation is the
standard approach (MCP, gRPC reflection, HTTP OPTIONS).

**Source:** Architecture Strategist, Best Practices Researcher (Google AIP-180)

#### R4: Add --json to mu build (not just --plan/--emit-manifest)

**Original:** `--json` only proposed for `--plan` output.
**Revised:** Add `--json` as a flag on `mu build` itself. When set, emit a
structured JSON build summary to stdout (same shape as ManifestSummary).
This is consistent with every other subcommand (`cache ls`, `target list`,
`verify`) which all support `--json`.

**Source:** Agent-Native Reviewer (Critical finding)

#### R5: Emit partial manifests on failure

**Original:** "Failed build does not emit manifest output."
**Revised:** Emit the manifest even on partial failure. Per-target `state`
fields reflect "completed" vs "failed". The `ManifestSummary` already has
`failed` and `cancelled` counts. Let consumers decide what to do with
partial results.

**Rationale:** In multi-target builds, suppressing all output because one
target failed prevents pudl from learning about the 9 that succeeded.

**Source:** Agent-Native Reviewer, Pattern Recognition Specialist (Bazel BEP
emits events for all actions including failures)

#### R6: Add WorkDir path traversal validation

**Original:** Not addressed.
**Revised:** Add path traversal check in `resolve.go` for WorkDir:

```go
if !strings.HasPrefix(filepath.Clean(workDir), filepath.Clean(projectRoot)+string(filepath.Separator)) {
    return nil, fmt.Errorf("work_dir %q escapes project root", spec.WorkDir)
}
```

**Rationale:** Existing gap in `resolve.go:65-68`. For convergence actions
running bare (not sandboxed), a malicious `work_dir: "../../.."` allows
arbitrary directory access.

**Source:** Security Sentinel (Finding 7)

#### R7: Handle cached action outputs in ActionStatus

**Original:** Plan mentions extending ActionStatus with Outputs but doesn't
address cache-hit path.
**Revised:** When `executeAction` returns early on cache hit (executor.go:152),
populate `ActionStatus.Outputs` from `cached.Outputs`. Without this,
manifests will have empty output digests for cached actions.

```go
if err := e.restoreOutputs(ctx, a, cached); err == nil {
    return ActionStatus{
        ID: a.ID, Cached: true, ExitCode: cached.ExitCode,
        Outputs: cached.Outputs, // NEW: carry through for manifest
    }
}
```

**Source:** Performance Oracle, Go HPC Architect

#### R8: Parallelize plugin startup

**Original:** Not addressed (existing sequential startup).
**Revised:** Change `Manager.Start()` to start all plugin processes
concurrently using `errgroup.Group`. Benefits both `Build()` and `Observe()`.

**Rationale:** A project with 5 plugins pays 5-15 seconds of sequential JVM
cold starts. Parallelizing reduces this to 1-3 seconds.

**Source:** Performance Oracle (Critical Issue 1)

#### R9: Simplify Manifest type

**Original:** `Manifest`, `TargetResult`, `ActionResult`, `ManifestSummary`
(4 nested types).
**Revised:** Drop `TargetResult` (per-target tracking doesn't exist in the
codebase today). Reuse `BuildResult` for summary. Rename manifest's
`ActionResult` to `ManifestAction` to avoid collision with `cas.ActionResult`.
Use `time.Time`/`time.Duration` internally, serialize at output time.

```go
type Manifest struct {
    Version   int              `json:"version"`
    Type      string           `json:"type"`      // "mu.build.manifest/v1"
    Timestamp time.Time        `json:"timestamp"`
    Duration  time.Duration    `json:"duration"`
    Actions   []ManifestAction `json:"actions"`
    Summary   BuildResult      `json:"summary"`
}
```

**Source:** Code Simplicity Reviewer, Go HPC Architect, Pattern Recognition
Specialist (OCI manifest conventions)

#### R10: Add observe_command to shell target config

**Original:** Deferred to "Future Considerations".
**Revised:** Include `observe_command` in the shell config schema from Phase 3.
If set, `mu observe` runs it (exit 0 = converged, nonzero = drifted with
stdout as diff). Without this, shell targets are invisible to the ACUTE loop.

```json
{
  "toolchain": "shell",
  "config": {
    "command": ["kubectl", "apply", "-f", "manifest.yaml"],
    "observe_command": ["kubectl", "diff", "-f", "manifest.yaml"],
    "network": true
  }
}
```

**Source:** Agent-Native Reviewer (Warning 3)

#### R11: Never pass nil to cmd.Env

**Original:** Not addressed.
**Revised:** `buildEnv()` in executor.go currently returns nil for empty maps,
which causes the child process to inherit the parent's full environment
(including AWS credentials, SSH keys, etc.). For convergence actions, always
construct an explicit minimal environment. Add a `required_env` config field
for credential scoping.

**Source:** Security Sentinel (Finding 2)

#### R12: Include WorkDir in ComputeActionKey

**Original:** Not addressed.
**Revised:** `ComputeActionKey` at `actionkey.go:21-53` does not include
WorkDir. Two actions with the same command but different working directories
produce the same cache key. Fix regardless of this plan.

**Source:** Architecture Strategist (must-fix #3)

### Additional Insights

**From Best Practices Research:**

- **Bazel BEP pattern:** Consider NDJSON build event stream (`--build-event-file`)
  as a future enhancement. Each line is `{"type":"action_completed","id":"...","cached":true}`.
  The monolithic manifest can be derived from the event stream.
- **Terraform plan JSON format:** mu's plan JSON should include `format_version`
  and cache-hit prediction per action (probe CAS during plan).
- **Crossplane ExternalClient:** The observe-diff-converge pattern should
  use `ResourceState` with `exists`, `up_to_date`, and `diff` fields rather
  than a flat `state` string.
- **Nix impure derivation constraint:** Actions with `impure: true` should
  ideally only be depended upon by other impure actions or terminal actions,
  preventing non-deterministic results from poisoning pure cache entries.

**From Pattern Recognition:**

- **BuiltinPlugin interface:** Instead of a hard-coded `if target.Toolchain == "shell"`
  branch, define a `BuiltinPlugin` interface (`Discover`, `Plan`, `Observe`
  methods). Register the shell handler through this interface. The coordinator's
  planning loop stays uniform. If future built-ins are added (copy, download),
  they use the same mechanism.
- **Manifest should include `depends_on` edges** on actions for provenance
  graph reconstruction. Near-zero cost, enables pudl to understand causality.

**From Security Review:**

- **Blocking before shipping convergence:** (1) Require command as `[]string`,
  (2) WorkDir path traversal check, (3) explicit minimal env for bare actions.
- **Before production observe usage:** (4) Validate observe `State` against
  enum, (5) exclude env vars from manifest, (6) add `--observe-strict` mode.
- **Near-term hardening:** Network namespace isolation on Linux, plugin script
  integrity verification via CAS digest.

**From Performance Review:**

- **Selective plugin startup for observe:** Only start plugins for toolchains
  referenced by requested targets. Filter after `resolveTargets()`.
- **Skip ComputeActionKey for impure actions** unless verbose/debug logging
  is enabled. The key is computed but never used for cache lookup.
- **Document impure action concurrency constraints.** Users must express
  cross-target ordering via `depends_on` for convergence actions targeting
  the same external resource.
