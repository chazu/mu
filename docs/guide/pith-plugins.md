mu guide pith-plugins — writing inline plugins with pith VM programs

pith is a concatenative (stack-based) VM shared by mu and pudl. Programs
are JSON arrays of words interpreted against a stack. Instead of writing
a full NDJSON plugin binary, you can author plan, transform, and action
logic as inline pith programs in mu.cue.

────────────────────────────────────────────────────────────────────
WHAT PITH IS
────────────────────────────────────────────────────────────────────

Programs are JSON arrays. Each element is a word (dispatched), a literal
(pushed), or a nested array (pushed as a quotation for later execution).

  ["dup", 42, "add", ["len"], "apply"]

Words execute immediately. Quotations are deferred — combinators like
'apply', 'map', 'filter', and 'if' consume them.

String literals use a single-quote prefix to avoid word dispatch:

  ["'running"]       pushes the string "running"
  ["running"]        dispatches the word "running" (fails if not registered)

Full language reference: see the pith-vm.md doc in the pudl repo, or
run 'pudl exec --trace' to explore interactively.

────────────────────────────────────────────────────────────────────
THREE INTEGRATION POINTS
────────────────────────────────────────────────────────────────────

pith programs can appear in three target fields. Each runs in a
different build phase with a different set of available driver words.

1. plan — replaces plugin-based planning

  target: {
      name: "//infra/dns"
      plan: [
          "target/config",
          "dup", "'record_type", "get", "'A", "eq",
          [["'host", "get"], ["'ip", "get"], "dns/create-a"],
          [["'host", "get"], ["'target", "get"], "dns/create-cname"],
          "if",
          "action/emit",
      ]
  }

  The coordinator interprets the plan program, collects ActionSpecs
  emitted via 'action/emit', and merges them into the DAG. No plugin
  process is spawned. Targets with a 'plan' field skip plugin dispatch.

2. transform — reshape dependency outputs before own actions run

  target: {
      name: "//deploy/config"
      deps: ["//infra/vpc"]
      transform: [
          "'//infra/vpc", "target/output",
          "'vpc_id", "get",
      ]
  }

  Transform runs after dependencies complete but before this target's
  actions execute. Results are stored under the '_result' key and
  accessible to downstream targets via 'target/output'.

3. body — on actions, replaces shell commands

  Actions can have a 'body' field (pith program) instead of 'command'
  (shell string). The executor interprets the body with the full
  execute-phase vocabulary:

  action: {
      body: [
          "'https://api.example.com/vms", "http/get",
          "'items", "get",
      ]
      outputs: ["vms.json"]
  }

  Caching works identically: hash(canonical(body) + input_digests).

────────────────────────────────────────────────────────────────────
DRIVER WORDS BY PHASE
────────────────────────────────────────────────────────────────────

Each phase registers a different vocabulary. Words unavailable in a
phase produce "unknown word" errors.

  Word               Plan    Transform   Execute
  ──────────────────  ──────  ─────────   ───────
  action/emit        yes
  target/config      yes     yes
  target/output              yes         yes
  http/get                               yes
  http/post                              yes
  exec/run                               yes
  exec/shell                             yes
  cas/store                              yes
  cas/fetch                              yes
  format/json                            yes
  format/compact                         yes

Plan programs see only the target's config (via 'target/config').
They cannot read dependency outputs or perform side effects.

Transform programs can read dependency outputs ('target/output')
but cannot emit actions or perform side effects.

Execute programs have the full effectful vocabulary but cannot
modify the DAG.

────────────────────────────────────────────────────────────────────
CORE VOCABULARY (AVAILABLE IN ALL PHASES)
────────────────────────────────────────────────────────────────────

Stack:     dup drop swap over nip rot tuck 2dup 2drop
Combs:     apply dip keep bi bi* bi@ each map filter reduce any? all?
Control:   if when unless
Objects:   get set has? keys values path pick omit merge
Compare:   eq neq lt gt lte gte
Logic:     and or not null?
Arith:     add sub mul div mod
Strings:   concat len split
Sequences: group-by flatten

────────────────────────────────────────────────────────────────────
CUE SCHEMA VALIDATION
────────────────────────────────────────────────────────────────────

pith ships a CUE package for validating programs at definition time:

  import "github.com/chazu/pith"

  target: {
      name: "//infra/dns"
      plan: pith.#Program & [
          "target/config", "'type", "get",
          "'A", "eq",
          [["'host", "get"], "action/emit"],
          [["'cname", "get"], "action/emit"],
          "if",
      ]
  }

#Program validates each operation against the full vocabulary. Unknown
words and malformed ops produce CUE unification errors before the
interpreter runs.

────────────────────────────────────────────────────────────────────
WHEN TO USE PITH VS PLUGIN BINARIES
────────────────────────────────────────────────────────────────────

  Use pith for:                     Use a plugin binary for:
  ─────────────────────────         ─────────────────────────────
  API call + JSON transform         Go/Rust/C compilation
  Conditional action selection       Docker/OCI builds
  Config templating                  Terraform apply
  Inter-target data wiring           Subprocess streaming
  Simple observation queries         Persistent state across actions

Rule of thumb: if the logic is "call API, transform data, emit result"
— pith program. If it needs a toolchain, long-running process, or
complex I/O — plugin binary.

A single target can mix both: 'toolchain: "go"' dispatches to the Go
plugin for compilation, while a 'transform' pith program reshapes
the output before downstream targets consume it.

────────────────────────────────────────────────────────────────────
WORKED EXAMPLE
────────────────────────────────────────────────────────────────────

A target that fetches metrics from an API and emits an action to
store the result:

  targets: [{
      target: "//metrics/collect"
      plan: [
          "target/config",
          "dup", "'url", "get",
          "swap", "'output_path", "get",
          {
              "id":      "fetch-metrics",
              "command": ["curl", "-s"],
              "inputs":  {},
              "outputs": [],
          },
          "action/emit",
      ]
      config: {
          url:         "https://api.example.com/metrics"
          output_path: "metrics.json"
      }
  }]

A downstream target with a transform that reads the metrics:

  targets: [{
      target: "//report/summary"
      deps: ["//metrics/collect"]
      transform: [
          "'//metrics/collect", "target/output",
          "'_result", "get",
          "dup", "len",
      ]
      plan: [
          "target/config",
          {"id": "write-report", "command": ["echo", "done"],
           "inputs": {}, "outputs": []},
          "action/emit",
      ]
  }]

────────────────────────────────────────────────────────────────────
SHARED LANGUAGE WITH PUDL
────────────────────────────────────────────────────────────────────

pudl and mu share the same VM and data model (JSON on the stack).
pudl registers read-only words (catalog/query, schema/match, etc.);
mu registers effectful words (http/get, exec/run, action/emit, etc.).

An agent that learns to write programs for pudl queries can
immediately write programs for mu actions — only the driver word
vocabulary differs. The vocabulary is introspectable from CUE via
pith.#Program.

────────────────────────────────────────────────────────────────────
ERROR HANDLING
────────────────────────────────────────────────────────────────────

Errors include the op index for debugging:

  op 3 (get): expected map[string]any, got int
  op 2: unknown word: staet

Use 'pudl exec --trace' to step through programs interactively.
In mu, trace mode is available for development but disabled in
production builds.

────────────────────────────────────────────────────────────────────
SEE ALSO
────────────────────────────────────────────────────────────────────

  mu guide plugins     NDJSON plugin protocol (pith coexists with this)
  mu guide protocol    Wire format for plugin methods
  mu guide pudl        How mu and pudl share pith programs
  pudl exec            Run pith programs against the pudl data lake
