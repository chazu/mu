package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chau/mu/internal/config"
)

func runGuide(args []string) int {
	if len(args) == 0 {
		printGuideIndex()
		return 0
	}

	switch args[0] {
	case "overview":
		printGuideOverview()
	case "mu.cue":
		printGuideMuJSON()
	case "plugins":
		printGuidePlugins()
	case "build":
		printGuideBuild()
	case "observe":
		printGuideObserve()
	case "pudl":
		printGuidePudl()
	case "cache":
		printGuideCache()
	case "secrets":
		printGuideSecrets()
	case "secret-gen":
		printGuideSecretGen()
	case "toolchains":
		printGuideToolchains()
	case "shell":
		printGuideShell()
	case "protocol":
		printGuideProtocol()
	case "secret-providers":
		printGuideSecretProviders()
	case "plugin":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "usage: mu guide plugin <name>")
			return 2
		}
		return printGuideForPlugin(args[1])
	default:
		fmt.Fprintf(os.Stderr, "mu guide: unknown topic %q\n", args[0])
		fmt.Fprintln(os.Stderr, "Run 'mu guide' for a list of topics.")
		return 2
	}
	return 0
}

func printGuideIndex() {
	fmt.Print(`mu guide — quick-reference for agents and humans

Start here if you're new to mu:

  mu guide overview      What mu is, the mental model, and where to go next

Authoring:

  mu guide mu.cue        How to write and structure mu.cue config files
  mu guide build         Building targets: flags, plan mode, manifests
  mu guide observe       Drift detection: observing current state
  mu guide secrets       Sealed inputs/outputs, secret resolution, write policy

Built-in toolchains:

  mu guide shell         Run an arbitrary shell command as a target
  mu guide secret-gen    Mint a secret and store it via a provider plugin

Plugins and integration:

  mu guide plugins          Writing, loading, and distributing plugins
  mu guide protocol         The NDJSON plugin protocol (discover, plan, observe, ...)
  mu guide secret-providers Authoring secret-aware plugins (resolve/store, sealed_outputs)
  mu guide plugin <name>    Show guide text bundled with a plugin
  mu guide pudl             How mu and pudl work together

Operations:

  mu guide cache         Content-addressed storage and action caching
  mu guide toolchains    Bootstrapping toolchains from scratch
`)
}

func printGuideOverview() {
	fmt.Print(`mu guide overview — what mu is, in 60 seconds

WHAT MU IS

  mu is a build/convergence orchestrator. You declare targets in
  mu.cue; mu resolves them into a DAG of actions, executes the DAG
  with parallel scheduling and content-addressed caching, and
  hands off observability to plugins.

  mu is not opinionated about what you build — Go binaries, OCI
  images, terraform-managed infrastructure, k8s manifests, secrets
  in a password store, and shell pipelines all live in the same
  graph and share the same cache.

THE MENTAL MODEL

  - mu.cue           declares targets (what you want)
  - plugins          translate targets into actions (how to get there)
  - the coordinator  resolves the cross-target action graph
  - the executor     runs the DAG, caches by content
  - the CAS          stores blobs and action results, keyed by sha256
  - sealed_inputs    resolve secrets for actions (read side)
  - sealed_outputs   capture secrets from actions (write side)
  - observe          plugins report current state for drift detection

  Each piece is replaceable. Plugins are external processes speaking
  NDJSON over stdin/stdout (see 'mu guide protocol'). Two toolchains
  are built in: 'shell' and 'secret-gen' (see their guides).

THE DAY-TO-DAY VERBS

  mu build <target>...      Plan + execute the target's action DAG.
  mu build --plan <target>  Show the planned actions, don't run them.
  mu observe <target>...    Ask each plugin to report current state.
  mu cache ls               List cached action results.
  mu plugin list            Show registered plugins.
  mu plugin info <name>     Show capabilities/metadata for one plugin.
  mu target list            Show targets defined in this project.

WHAT TO READ NEXT

  Authoring a project:    mu guide mu.cue → mu guide build
  Writing a plugin:       mu guide plugins → mu guide protocol
  Working with secrets:   mu guide secrets → mu guide secret-gen
  Drift / convergence:    mu guide observe → mu guide pudl
  Hermetic toolchains:    mu guide toolchains
  Caching/CAS internals:  mu guide cache

KEY DESIGN PRINCIPLES

  1. Everything that runs is an action with a deterministic cache key.
  2. Plugins are sensors and planners; they never mutate the graph
     after it's built.
  3. Secrets values never enter the cache, manifests, or logs.
     Refs and modes do — they're non-secret metadata.
  4. mu and pudl are decoupled. mu builds; pudl reasons about state.
  5. mu.cue is the single source of authoring truth.
`)
}

func printGuideMuJSON() {
	fmt.Print(`mu guide mu.cue — configuration file reference

mu.cue is the project configuration file, written in CUE
(cuelang.org). mu discovers it by walking up from the current
directory. Subdirectories may contain their own mu.cue files — they
are merged automatically. CUE accepts JSON-shaped syntax as well, so
existing JSON-style configs keep working.

MINIMAL EXAMPLE

  package mu

  plugins: [{name: "go", script: "plugins/go/plugin.bb"}]

  targets: [{
      target:    "//cmd/myapp"
      toolchain: "go"
      sources:   ["cmd/myapp/*.go", "internal/**/*.go"]
      config: {
          output: "myapp"
          pkg:    "./cmd/myapp"
      }
  }]

TOP-LEVEL KEYS

  targets       Array of build targets (see below).
  plugins       Array of plugin definitions (see 'mu guide plugins').
  toolchains    Array of toolchain bootstrap definitions (see 'mu guide toolchains').
  cache         Cache configuration (optional).
  secrets       Project-wide secret policy, e.g. writable_refs (optional).
                See 'mu guide secrets'.
  preprocessor  Config preprocessor for non-CUE/JSON formats (optional).

TARGET FIELDS

  target               Target name. Convention: "//path/to/name".
  toolchain            Which toolchain handles this target. Built-in:
                       "shell", "secret-gen". Otherwise the name of a
                       registered plugin (e.g. "go", "file", "k8s").
  sources              Array of source file paths or globs ("src/**/*.go").
  deps                 Array of target names this depends on (optional).
  config               Toolchain-specific config object (optional).
  sealed_inputs        {NAME: "scheme:path"} — secret refs to resolve
                       before exec (optional). See 'mu guide secrets'.
  sealed_input_modes   {NAME: "env" | "file"} — delivery per name
                       (optional, default env).
  sealed_outputs       {NAME: "scheme:path"} — destinations to capture
                       from $MU_SEALED_OUT_DIR/<NAME> (optional).
  kind                 BRICK classification: "relationship",
                       "interface", "component", "kit" (optional).
  implements           Interface this component satisfies (optional).

TARGET NAMING

  Targets are named "//path/name". Subdirectory targets are auto-prefixed:
  a target "mylib" in foo/mu.cue becomes "//foo/mylib".

CONFIG MERGING

  mu walks up to find the project root mu.cue, then recursively discovers
  mu.cue files in subdirectories. Targets from subdirectories are merged
  with paths rebased relative to the project root. Globs in sources are
  expanded. Hidden directories (.git, .claude, etc.) and testdata/ are skipped.

SECRETS POLICY

  secrets: writable_refs: ["pass:my-project/*"]

  Allow-list of glob patterns; sealed_outputs whose ref does not
  match is rejected at plan time. Omit the block to allow all
  writes; set to [] for explicit deny-all. See 'mu guide secrets'.

PREPROCESSOR

  For other config formats (YAML, TOML), define a preprocessor:

  preprocessor: {
      extension: ".yaml"
      command: ["yq", "-o", "json"]
  }

  mu pipes files with matching extension through the command before parsing.

CACHE CONFIG

  cache: {
      backends: [
          {type: "disk", path: "~/.mu/cache", max_size: "10GB"},
          {type: "oci", registry: "ghcr.io/org/cache"},
      ]
      write_through: true
      read_repair:   true
  }

SEE ALSO

  docs/cue-conventions.md     Authoring conventions, layout, embed.
`)
}

func printGuideBuild() {
	fmt.Print(`mu guide build — building targets

USAGE

  mu build [flags] <target>...

FLAGS

  --plan            Show planned actions without executing (dry run).
  --dry-run         Alias for --plan.
  --json            Output as JSON.
  --emit-manifest   Emit a build manifest to stdout (for pudl's ACUTE loop).
  --no-cache        Skip cache reads — rebuild everything.
  --jobs N          Max parallel actions (default: NumCPU).
  --config PATH     Path to mu.cue (default: discover by walking up).
  --verbose         Show plugin I/O.

EXAMPLES

  mu build //cmd/myapp                     Build a single target.
  mu build //cmd/myapp //lib/utils         Build multiple targets.
  mu build --plan //cmd/myapp              Preview the action DAG.
  mu build --emit-manifest //cmd/myapp     Build and emit manifest JSON.
  mu build --no-cache //cmd/myapp          Force full rebuild.
  mu build --jobs 4 //cmd/myapp            Limit parallelism.

BUILD PIPELINE

  1. Bootstrap toolchains from scratch (if defined).
  2. Resolve plugins to CAS (hash scripts, fetch URLs).
  3. Start plugin processes and run discover.
  4. Resolve target dependency graph (topological order).
  5. Plan each target via its plugin (plugin emits action specs).
     Built-in toolchains 'shell' and 'secret-gen' bypass plugins.
  6. Merge action subgraphs into a unified DAG.
  7. Resolve sealed_inputs (secret values held in memory only).
  8. Enforce secrets.writable_refs against any sealed_outputs in
     the graph; abort if any ref is not allowed.
  9. Execute DAG: topological sort, worker pool, per-action:
     - check cache (skip if impure or sealed_outputs declared)
     - inject secrets (env or file mode per sealed_input_modes)
     - mint $MU_SEALED_OUT_DIR if sealed_outputs declared
     - run command
     - capture sealed_outputs and route via store_secret
     - hash outputs into CAS, write action result entry

ACTION CACHING

  See 'mu guide cache' for the full cache-key contract. Summary:
  command + sorted input digests + env + network + impure +
  sealed-input refs/modes + sealed-output refs are hashed.
  Sealed-input/output VALUES are never hashed.

OTHER COMMANDS

  mu target list                List all targets in the project.
  mu target list --json         List targets as JSON.
`)
}

func printGuidePlugins() {
	fmt.Print(`mu guide plugins — writing, loading, and distributing plugins

Plugins extend mu with support for new toolchains and resource types.
They are external processes that communicate via NDJSON (see 'mu guide protocol').

LOADING PLUGINS

  There are four ways to reference a plugin, in resolution order:

  1. Local script (preferred for development):
     {"name": "file", "script": "plugins/file/plugin.bb"}

     Also works with a directory containing mu.cue with a "plugin" key:
     {"name": "go", "script": "plugins/go"}

  2. Remote URL with SHA256 verification:
     {"name": "file", "url": "https://example.com/file-plugin.bb", "sha256": "abc123..."}

  3. CAS digest (for distribution — previously built plugin):
     {"name": "file", "digest": "sha256:abc123..."}

  4. Direct command (escape hatch, not cached):
     {"name": "file", "command": ["python3", "my-plugin.py"]}

WRITING A PLUGIN

  A plugin is any executable that reads NDJSON on stdin and writes NDJSON
  on stdout. It must implement at least "discover" and "plan" methods.
  Optionally it can implement "observe", "resolve_secret", and
  "store_secret". Each optional method must be listed in the
  capabilities array returned by discover.

  Capabilities:
    discover         (required)  Identify the plugin and its protocol version.
    plan             (required)  Translate a target into action specs.
    observe          (optional)  Report current state for drift detection.
    resolve_secret   (optional)  Read a secret value by ref.
    store_secret     (optional)  Write a secret value by ref.

  resolve_secret + store_secret together make a plugin a full
  bidirectional secret provider. See 'mu guide secret-providers'
  for the plugin-author walkthrough (ref grammars, modes, sealed
  inputs/outputs, forced-impure semantics).

  Babashka (.bb) is the conventional runtime but any language works.

  Minimal plugin (Babashka):

    #!/usr/bin/env bb
    (require '[cheshire.core :as json])

    (defn handle-request [req]
      (case (get req "method")
        "discover" {"name"             "myplugin"
                    "version"          "0.1.0"
                    "protocol_version" 1
                    "consumes"         []
                    "produces"         ["myartifact"]
                    "capabilities"     ["discover" "plan"]}
        "plan"     (let [target (get req "target")
                         config (get target "config")]
                     {"actions" [{"id"      "run"
                                  "command" ["echo" "hello"]
                                  "inputs"  {}
                                  "outputs" []
                                  "depends_on" []}]
                      "declared_outputs" {}})
        {"error" (str "unknown method: " (get req "method"))}))

    (loop []
      (when-let [line (read-line)]
        (println (json/generate-string (handle-request (json/parse-string line))))
        (flush)
        (recur)))

PLUGIN DIRECTORY STRUCTURE

  For bundling and distribution, plugins use this layout:

    plugins/myplugin/
      mu.cue        Declares plugin metadata and build target.
      plugin.bb      The plugin script.
      GUIDE.md       Optional guide text (shown by 'mu guide plugin myplugin').

  The mu.cue has a "plugin" key:

    {
      "plugin": {
        "entrypoint": "plugin.bb",
        "toolchain": "bb",
        "files": ["plugin.bb"],
        "guide": "GUIDE.md"
      },
      "targets": [
        {
          "target": "build",
          "toolchain": "shell",
          "sources": ["plugin.bb"],
          "config": {"command": ["true"], "impure": false}
        }
      ]
    }

BUILDING AND DISTRIBUTING PLUGINS

  mu plugin add <name>    Build //plugins/<name> and register its CAS digest
                          in mu.cue. After this, the plugin can be referenced
                          by digest for reproducible builds.

  mu plugin list                 List plugins defined in mu.cue.
  mu plugin list --discover      Start plugins and show capabilities.
  mu plugin list --cached        Show all plugins stored in ~/.mu/plugins/.
  mu plugin list --json          Output as JSON.
  mu plugin info <name>          Show capabilities, schemas, digest, and
                                 path for a single plugin (project or
                                 cached). Works outside any mu project.
  mu plugin status               Reconcile declared plugins against the
                                 local cache (ok / missing / stale / local).
  mu plugin push                 Publish a plugin to a configured OCI cache.
  mu plugin test <plugin-path>   Run bundled + testdata/*.yaml scenarios
                                 against a plugin.

PLUGIN GUIDES

  Plugins can include a guide file that describes their usage. Set the
  "guide" field in the plugin manifest to the relative path of the file:

    {"plugin": {"guide": "GUIDE.md", "entrypoint": "plugin.bb", ...}}

  The guide file is automatically bundled with the plugin in CAS, even
  if it is not listed in "files". View a plugin's guide with:

    mu guide plugin <name>

  This searches extracted bundles (~/.mu/plugins/<name>/bundle-*/) first,
  then falls back to local plugin directories (plugins/<name>/).
  Conventional filenames (GUIDE.md, GUIDE, guide.md) are also detected
  without an explicit manifest entry.

  RECOMMENDED GUIDE.md CONTENTS

  A good plugin guide is the user's first stop after 'mu plugin info'.
  The metadata side (capabilities, consumes/produces, schemas) is
  surfaced by 'mu plugin info' automatically — your GUIDE.md should
  cover everything info CAN'T tell them. Use this skeleton:

    mu guide plugin <name> — <one-line role: "AWS observer", "k8s applier", ...>

    <2–4 sentence summary: what it does, what it does NOT do, and the
    stable contract it offers>

    SETUP
      External binaries it shells out to (and the minimum version),
      auth/config files it expects (~/.kube/config, AWS_PROFILE, …),
      OS or platform constraints. Be concrete: 'pass >= 1.7' beats
      'a recent pass'.

    USAGE IN mu.cue
      Smallest working target snippet a user can copy-paste. Include
      the plugins[] entry and at least one target using the plugin.

    CONFIG FIELDS
      One row per field: name, type, required?, default, brief
      meaning. Keep this in sync with the discover config_schema —
      'mu plugin info' renders the schema; the guide explains it.

    SECRETS (omit if the plugin neither reads nor writes secrets)
      - For sealed_inputs: name each env var / file path the plugin
        expects, and which delivery modes it supports (env, file).
        SSH keys, kubeconfigs, JSON service-account blobs almost
        always want 'sealed_input_modes: file'.
      - For sealed_outputs: which logical names the plugin emits,
        the ref scheme expected (e.g. "pass:..."), and a note on
        idempotency mode (create / overwrite / create_if_absent).
      - If the plugin is itself a secret provider (resolve_secret /
        store_secret), describe its ref grammar and any 'raw:' /
        scoping prefixes — see 'mu guide secret-providers'.

    OBSERVATION OUTPUT (omit if no observe capability)
      Sketch the shape of the JSON the plugin returns. If you ship
      a CUE output_schema, name it here and link to the schema file.

    EXAMPLES
      One worked end-to-end example per major use case (one's fine
      if there's only one). Show the target, the plugins[] entry,
      and the 'mu build' / 'mu observe' invocation.

    TROUBLESHOOTING (optional but appreciated)
      The two or three failure modes you keep seeing in support.
      A grep-able error string + one-line fix is gold.

  Keep the guide short and copy-paste friendly. If a section has
  nothing to say for your plugin, drop it entirely — empty headings
  are noise. Live examples in this repo:

    plugins/pass/GUIDE.md         secret provider, ref grammar, modes
    plugins/sops/GUIDE.md         file-based provider, write policy
    plugins/host/GUIDE.md         observer, sealed-input file mode
    plugins/remote-exec/GUIDE.md  consumer + emitter, sealed_output_files
    plugins/keypair-gen/GUIDE.md  generator, two-name sealed_outputs contract

OUTPUT SCHEMAS (optional, for plugins whose output flows into pudl)

  A plugin can declare a CUE schema for the data it produces so that
  pudl classifies imports under a meaningful type instead of the
  catchall pudl/core.#Item.

  1. Add output_schema to the discover response:

       "output_schema": {"module":     "mu/aws",
                         "version":    "v1",
                         "definition": "#EC2Instance"}

  2. Vendor the schema files with the plugin (mirrored layout):

       plugins/aws/
         schemas/mu/aws/ec2.cue          # package aws

  3. Declare the vendored module in mu.cue:

       plugin: {
         schemas: [
           {module: "mu/aws", version: "v1", path: "schemas/mu/aws"},
         ]
       }

  Namespace convention (see 'docs/cue-conventions.md' §6):
    pudl/...         first-party pudl schemas
    mu/<plugin>      schemas authored by the plugin's authors
    anything else    third-party / user-defined

  'mu verify' warns when a plugin claims a mu/<x> namespace whose <x>
  doesn't match its directory name.

  Full plugin-author guide: docs/plugin-output-schemas.md.
`)
}

func printGuideObserve() {
	fmt.Print(`mu guide observe — drift detection

The observe command asks plugins to report the current state of resources.
Plugins act as sensors — they report what exists, not whether it's correct.
Convergence decisions are made by pudl or the operator.

USAGE

  mu observe [flags] <target>...

FLAGS

  --json      Output as JSON array of ObserveResult objects.
  --ndjson    Output flattened records, one per line (for piping to pudl).
  --config    Path to mu.cue.
  --verbose   Show plugin I/O.

EXAMPLES

  mu observe //infra/aws-inventory
  mu observe --json //infra/aws-inventory
  mu observe --ndjson //infra/aws-inventory | pudl import --stdin

HOW IT WORKS

  1. mu loads the target from mu.cue.
  2. If the target has sealed_inputs, secrets are resolved first.
  3. mu sends an "observe" request to the target's plugin with:
     - target info (name, toolchain, config)
     - resolved secrets (never logged or cached)
  4. The plugin queries the real system (AWS API, filesystem, etc.)
     and returns: {"current": {"records": [...]}}
  5. Each record should include "_schema" for pudl routing:
     {"_schema": "aws.ec2.instance", "instance_id": "i-abc", ...}

OUTPUT FORMATS

  Plain text (default):
    //target  {"records":[...]}

  JSON (--json): Full ObserveResult array with target context:
    [{"target": "//target", "current": {"records": [...]}}]

  NDJSON (--ndjson): One record per line, for streaming into pudl:
    {"_schema":"aws.ec2.instance","instance_id":"i-abc",...}
    {"_schema":"aws.ec2.vpc","vpc_id":"vpc-123",...}

WRITING AN OBSERVE PLUGIN

  Add "observe" to your plugin's capabilities list in discover, then
  handle the "observe" method:

    "discover" {"capabilities" ["discover" "plan" "observe"] ...}

    "observe"  (let [config (get-in req ["target" "config"])
                     secrets (get req "secrets")]
                 ;; Query the real system using config + secrets.
                 {"current" {"records" [{...} {...}]}})

  Record conventions:
  - Include "_schema" field for pudl routing (e.g. "aws.ec2.instance").
  - Return arrays even for single resources.
  - Return {"error": "message"} on failure.
`)
}

func printGuidePudl() {
	fmt.Print(`mu guide pudl — how mu and pudl work together

mu and pudl are decoupled tools that communicate through mu.cue.

  pudl: defines desired state (CUE), observes actual state, computes drift.
  mu:   takes desired-state targets and converges them using plugins.

Neither tool imports or depends on the other.

WORKFLOW

  1. Define desired state in CUE (pudl's schema system):

     package definitions
     nginx_conf: file.#Config & {
         path:    "/etc/nginx/nginx.conf"
         content: "server { listen 80; }"
         mode:    "0644"
     }

  2. Observe actual state and check for drift:

     pudl import --path /etc/nginx/nginx.conf
     pudl drift check nginx_conf

  3. Export drifted resources as a mu config:

     pudl export-actions --definition nginx_conf > converge.json

     This produces a mu.cue with desired-state targets:
     {
       "targets": [{
         "target": "//nginx_conf",
         "toolchain": "file",
         "config": {"path": "/etc/nginx/nginx.conf", "content": "...", "mode": "0644"}
       }]
     }

  4. Converge with mu:

     mu build --config converge.json //nginx_conf

  5. Verify convergence (the ACUTE loop):

     mu observe --json //nginx_conf | pudl import --stdin
     pudl drift check nginx_conf  # should report no drift

OBSERVATION PIPELINE

  mu observe --ndjson <targets> | pudl import --stdin

  mu's observe output streams records with _schema fields. pudl routes
  each record by schema to the appropriate CUE definition for comparison.

RESOURCE TYPE MAPPING

  pudl maps CUE schema prefixes to mu toolchain names:

    file.*, config.*            → file
    ec2.*, s3.*, iam.*, aws.*   → aws
    k8s.*, kubernetes.*         → k8s
    (unknown)                   → generic

DATA IMPORT (mu → pudl)

  Beyond the drift loop above, mu plugins can also produce data that
  flows into pudl's catalog. Plugins optionally declare a CUE schema
  for their output so pudl classifies the data under a meaningful
  type instead of the catchall pudl/core.#Item.

  See 'mu guide plugins' (OUTPUT SCHEMAS section) for the plugin-side
  contract, or docs/plugin-output-schemas.md for the full guide.

  On import, pudl auto-detects an envelope JSON with shape
  {"schema": {...}, "definitions": [...], "data": <payload>} and
  classifies the data under the declared schema. Raw JSON (no
  envelope) can still be typed explicitly via
  'pudl import --schema mu/aws@v1#EC2Instance'. Items can satisfy
  multiple schemas; unresolved refs are tagged for later upgrade via
  'pudl reclassify'.

KEY DESIGN PRINCIPLE

  pudl emits desired state, not drift diffs. The file plugin receives
  {"path": "...", "content": "..."} — it doesn't know about pudl, CUE,
  or drift reports. It just makes the file match the config. Any mu plugin
  works whether the target came from pudl or a hand-written mu.cue.

PUDL AS A BUILD TARGET

  pudl can also run inside the build graph as a consumer of another
  target's declared outputs. The common case: run terraform (or any
  toolchain that emits state/artifacts) and feed the result into pudl
  in a single 'mu build' invocation.

    {
      "targets": [
        {
          "target": "//infra/vpc",
          "toolchain": "terraform",
          "sources": ["infra/vpc/*.tf"],
          "config": {"dir": "infra/vpc"}
        },
        {
          "target": "//pudl/vpc-catalog",
          "toolchain": "pudl",
          "deps": ["//infra/vpc"],
          "config": {"from": "terraform_state"}
        }
      ]
    }

  The terraform plugin declares outputs:

    "declared_outputs": {
      "terraform_state":   "infra/vpc/state.json",
      "terraform_outputs": "infra/vpc/outputs.json"
    }

  mu threads those into the pudl plan request as:

    "deps": [{"target": "//infra/vpc",
              "artifacts": {"terraform_state": "infra/vpc/state.json",
                            "terraform_outputs": "infra/vpc/outputs.json"}}]

  A pudl plugin that declares an action with
  inputs = {"state": "infra/vpc/state.json"} gets an implicit
  DependsOn edge to the terraform show action, so the DAG runs the
  producer first and the consumer after.

  See 'mu guide protocol' for the declared_outputs / deps[].artifacts
  contract and how to consume cross-target artifacts from a plugin.
`)
}

func printGuideCache() {
	fmt.Print(`mu guide cache — content-addressed storage and action caching

mu stores all build artifacts in a content-addressed store (CAS) using
OCI image layout. The default location is ~/.mu/cache/.

HOW CACHING WORKS

  Each build action has a cache key (sha256) computed from:
  - Command (argv, original order)
  - Sorted input digests (sha256 of each input file)
  - Environment variables (sorted)
  - Network flag
  - Impure flag
  - Work directory (if non-default)
  - Sealed-input refs and modes (NOT values)
  - Sealed-output destination refs

  Cache key INCLUDES (non-secret metadata):
    sealed_input refs       Changing pass:foo/v1 → pass:foo/v2 invalidates.
    sealed_input modes      env vs file is observable (path vs literal).
    sealed_output refs      Changing the destination invalidates.

  Cache key EXCLUDES (secret data):
    sealed_input values     The resolved bytes never enter the key.
    sealed_output values    No value exists at key time anyway.

  This means: two builds with the same ref but different resolved
  values still cache-hit (if the underlying secret rotated, the
  action does not re-run; the consumer sees whatever value was
  resolved at exec time).

  Impure actions skip the cache entirely. Actions with non-empty
  sealed_outputs are forced impure — caching would skip the
  store_secret side-effect.

  On cache hit: outputs are restored from CAS without re-execution.
  On cache miss: action runs, outputs are hashed and stored in CAS.

OCI LAYOUT

  ~/.mu/cache/
    index.json                     OCI index (manifest registry)
    blobs/sha256/<hash>            Content-addressed blobs

  Action results are stored as OCI manifests with:
  - Config blob: {"outputs": {"name": {"Algorithm":"sha256","Hash":"..."}}, "exit_code": 0}
  - Layer blobs: the actual output file contents

INSPECTING THE CACHE

  mu cache ls                      List cached action results.
  mu cache ls --toolchains         List cached toolchains.
  mu cache ls --json               Output as JSON.
  mu cache inspect <ref>           Inspect an action, toolchain, or blob by tag/digest.
  mu cache size                    Show total cache disk usage.
  mu cache size --json             Output as JSON.

VERIFYING CACHE INTEGRITY

  mu verify                        Re-hash all blobs, report corruption.
  mu verify --json                 Output as JSON.
  mu verify --fix                  Delete corrupt blobs.

PLUGIN STORAGE

  Plugin scripts are hashed and stored in CAS. When loaded by script path,
  the script is hashed on startup. When loaded by digest, it's fetched from
  CAS directly. Built plugin bundles are extracted to ~/.mu/plugins/<name>/.
`)
}

func printGuideSecrets() {
	fmt.Print(`mu guide secrets — sealed inputs/outputs, secret resolution, write policy

mu has a symmetric secret system: actions can READ secrets via
sealed_inputs and WRITE secrets via sealed_outputs, all routed through
the same provider plugins (today: 'pass', 'sops'). Values never enter the
cache, manifests, or logs; refs and modes are non-secret metadata
and are part of the cache key.

This guide is the canonical user-facing reference. Per-feature deep
dives and the plugin-author counterpart:
  mu guide secret-providers      Authoring secret-aware plugins
  docs/secret-gen-toolchain.md
  docs/sealed-input-delivery-modes.md
  docs/secrets-write-policy.md

────────────────────────────────────────────────────────────────────
READING SECRETS — sealed_inputs
────────────────────────────────────────────────────────────────────

DECLARATION

  target: "//deploy/app"
  toolchain: "k8s"
  sealed_inputs: {
      KUBECONFIG_TOKEN: "pass:deploy/k8s-token"
      AWS_SECRET_KEY:   "pass:aws/secret-key"
  }

  Format: NAME: "scheme:path". The scheme prefix selects the plugin
  ("pass" → the pass plugin). The path is sent to that plugin's
  resolve_secret method.

RESOLUTION FLOW

  1. Planning records sealed_inputs but does not resolve them.
  2. Before execution, mu sends each plugin:
       {"method": "resolve_secret", "secret_ref": "deploy/k8s-token"}
  3. The plugin returns {"value": "..."}.
  4. mu delivers the value to the action per its delivery mode.

DELIVERY MODES — sealed_input_modes

  By default, the resolved value is exported as the env var $NAME.
  Set sealed_input_modes to change that per-name:

  sealed_inputs:      SSH_KEY: "pass:raw:hosts/dalian/key"
  sealed_input_modes: SSH_KEY: "file"

  Modes:
    env  (default)  Value exported as $NAME.
    file            Value written to a 0600 temp file under a
                    per-action sealed-input directory; $NAME holds
                    the path. The dir is removed when the action
                    exits regardless of success.

  Sandbox-mode (toolchain-pinned) actions reject file mode because
  the temp file lives outside the sandbox view. shell, secret-gen,
  remote-exec, remote-file all run bare and support file mode.

THE PASS:RAW PREFIX

  By default, "pass:foo/bar" returns the FIRST LINE of 'pass show
  foo/bar' (the conventional "password" line). For multi-line
  secrets — SSH keys, certs, JSON blobs — use the raw: prefix
  inside the ref to get the full content, with trailing newlines
  trimmed:

    sealed_inputs: SSH_KEY: "pass:raw:hosts/dalian/key"

────────────────────────────────────────────────────────────────────
WRITING SECRETS — sealed_outputs
────────────────────────────────────────────────────────────────────

A target can capture an action's emitted value into a secret store.
The action writes the value to a file under $MU_SEALED_OUT_DIR; mu
reads it on success and routes through the provider's store_secret.

DECLARATION

  target: "//secrets/admin-pass"
  toolchain: "shell"
  sealed_outputs: ADMIN_PASS: "pass:registry/admin"
  config: {
      command: ["sh", "-c", ` + "`" + `openssl rand -base64 24 > "$MU_SEALED_OUT_DIR/ADMIN_PASS"` + "`" + `]
  }

PROPERTIES

  - The value is read from $MU_SEALED_OUT_DIR/<NAME> after the
    action exits (success only).
  - Routing failures abort the action.
  - Actions with sealed_outputs are forced impure — caching would
    skip the store side-effect.
  - Values never appear in stdout, the action cache, or manifests.

For a higher-level wrapper that handles the common
"derive-once-and-store" pattern, see 'mu guide secret-gen'. For
two-correlated-output secrets that don't fit secret-gen's single-
stdout shape (SSH keypairs, TLS keypairs), use the keypair-gen
plugin — see plugins/keypair-gen/GUIDE.md.

────────────────────────────────────────────────────────────────────
WRITE-POLICY ALLOW-LIST — secrets.writable_refs
────────────────────────────────────────────────────────────────────

By default any plugin with the store_secret capability can write
to any ref under its scheme. To bound the blast radius, declare a
project-level allow-list at the top of mu.cue:

  secrets: {
      writable_refs: [
          "pass:registry/*",
          "pass:loosh/*",
      ]
  }

  Patterns use Go's path.Match: '*' matches a single path segment
  (does NOT span '/'); literal text matches itself. Multiple
  patterns are OR'd.

THREE STATES

  secrets block omitted             No allow-list (writes unrestricted).
  writable_refs: [...patterns]      Strict allow-list.
  writable_refs: []                 Explicit deny-all (lockdown).

ENFORCEMENT

  Plan time: every sealed_output ref in the graph is checked. A
  forbidden ref aborts Plan() before the provider manager starts.
  Write time: the SealedOutputWriter closure re-checks defensively
  before calling store_secret.

────────────────────────────────────────────────────────────────────
SECURITY MODEL
────────────────────────────────────────────────────────────────────

VALUES never appear in:
  - the CAS or action cache
  - build manifests
  - stdout / stderr captured by the cache layer
  - verbose plugin I/O logs
  - process tables (file mode keeps multi-line secrets out of env)

REFS and MODES are part of the cache key. Changing
"pass:foo/v1" → "pass:foo/v2" or env → file invalidates the
cache, because both can change observable behavior.

The allow-list bounds writes; reads are bounded by what mu.cue
explicitly references. mu cannot stop a target from shelling out
to 'pass insert' directly — only writes routed through
sealed_outputs go through store_secret.

────────────────────────────────────────────────────────────────────
THE PASS PLUGIN
────────────────────────────────────────────────────────────────────

The bundled pass plugin (plugins/pass/plugin.bb, v0.3.0) implements:
  discover, resolve_secret, store_secret

Register it:

  plugins: [{name: "pass", script: "plugins/pass/plugin.bb"}]

Resolution: "pass:foo/bar" → 'pass show foo/bar' (first line).
            "pass:raw:foo/bar" → full content (newline-trimmed).
Storage:    'pass insert -m -f' under the configured mode.
            Modes: create | overwrite | create_if_absent.

Run 'mu guide plugin pass' for the full plugin guide.

────────────────────────────────────────────────────────────────────
WRITING A SECRET PROVIDER PLUGIN
────────────────────────────────────────────────────────────────────

A provider plugin is a normal NDJSON plugin (see 'mu guide protocol')
that adds two methods to its capability list:

  capabilities: ["discover", "resolve_secret", "store_secret"]

resolve_secret request:
  {"method": "resolve_secret", "secret_ref": "deploy/token"}
  reply: {"value": "..."}

store_secret request:
  {"method": "store_secret",
   "secret_ref": "deploy/token",
   "secret_value": "...",
   "secret_mode": "create_if_absent"}
  reply: {} or {"error": "..."}

The plugin is responsible for:
  - Implementing the three modes (create, overwrite, create_if_absent).
  - Returning empty success on no-op (create_if_absent for an
    existing ref).
  - Never logging the resolved or stored value.
`)
}

func printGuideSecretGen() {
	fmt.Print(`mu guide secret-gen — built-in toolchain for minting and storing secrets

The 'secret-gen' toolchain is built into mu — no plugin registration
required. Use it to declaratively bootstrap a secret: run a
derivation command, capture its stdout, route it through a
store_secret-capable provider plugin (today: 'pass', 'sops').

This is the natural complement to sealed_inputs. Where sealed_inputs
consumes a secret that someone has put in the store out of band,
secret-gen lets you say "this secret should exist" with a derivation
that produces it on first build.

BASIC USAGE

  target: "//secrets/admin-pass"
  toolchain: "secret-gen"
  config: {
      ref:        "pass:registry/admin"
      derivation: ["openssl", "rand", "-base64", "24"]
      // mode defaults to "create_if_absent"
  }

CONFIG FIELDS

  ref         (required) Destination secret ref, of the form
              scheme:path. The scheme must resolve to a plugin
              declaring the store_secret capability.
  derivation  (required) []string — argv whose stdout becomes the
              stored value.
  mode        (optional) "create" | "overwrite" | "create_if_absent"
              (default).
  env         (optional) Extra env vars for the derivation.
  keep_trailing_newline  (optional, default false) If false, strip
              a single trailing newline from the derivation's
              stdout before storing (the right thing for openssl
              rand, uuidgen, etc.).

PICKING A MODE

  create_if_absent  Bootstrap. Re-running is a cheap no-op once
                    the entry exists. Default.
  overwrite         Rotation. Always set to the fresh value.
  create            Strict bootstrap. Fail if the entry already
                    exists (catches out-of-band seeding).

  If your derivation is non-deterministic (openssl rand, uuidgen),
  'overwrite' rotates the value on every build — almost never
  what you want. Stick with create_if_absent.

CACHING

  secret-gen actions are always impure. Caching would skip the
  store_secret side-effect, which would be wrong if the provider
  entry has been wiped. The derivation runs every build; under
  create_if_absent the wasted work is bounded to the derivation
  itself.

WORKED EXAMPLE — bootstrap zot's admin password

  plugins: [{name: "pass", script: "plugins/pass/plugin.bb"}]
  secrets: writable_refs: ["pass:registry/*"]

  targets: [
      {
          target:    "//secrets/zot-admin"
          toolchain: "secret-gen"
          sources: []
          config: {
              ref:        "pass:registry/admin"
              derivation: ["openssl", "rand", "-base64", "32"]
          }
      },
      {
          target:    "//zot/htpasswd"
          toolchain: "remote-exec"
          deps: ["//secrets/zot-admin"]
          sealed_inputs: ADMIN_PASS: "pass:registry/admin"
          // ... uses $ADMIN_PASS in its remote command
      },
  ]

  First build: zot-admin runs the derivation, mints a fresh
  password, stores it under pass:registry/admin. The downstream
  htpasswd target's sealed_inputs resolves the same ref and
  consumes the value.

  Subsequent builds: the derivation re-runs, but pass insert is
  short-circuited by create_if_absent. Downstream sealed_inputs
  reads the stable value.

COOKBOOK — common derivations

  All examples below assume mode: create_if_absent (the default) and
  scheme: pass:. Swap "pass:..." for "sops:<file>#<key>" to land in a
  SOPS-encrypted file instead.

  Random password (32 bytes, base64, ~43 chars):
      derivation: ["openssl", "rand", "-base64", "32"]

  Random password (URL-safe, no '+' or '/' or '='):
      derivation: ["sh", "-c",
                   "openssl rand 32 | base64 | tr -d '=+/' | head -c 43"]

  Hex token (32 bytes -> 64 hex chars, e.g. an API key):
      derivation: ["openssl", "rand", "-hex", "32"]

  Short numeric PIN (6 digits):
      derivation: ["sh", "-c",
                   "od -An -N3 -tu4 /dev/urandom | tr -d ' \\n' | cut -c1-6"]

  UUID v4 (e.g. tenant id, idempotency key):
      derivation: ["uuidgen"]                      // macOS / util-linux
      derivation: ["cat", "/proc/sys/kernel/random/uuid"]   // any Linux

  Bcrypt-hashed password (htpasswd line for a webserver):
      // env-supplied plaintext keeps it off the argv. Pair this with a
      // secret-gen target that produces RAW_PASS into pass:foo/raw.
      config: {
          ref:        "pass:registry/htpasswd-line"
          derivation: ["sh", "-c",
                       "htpasswd -nbBC 12 admin \"$RAW_PASS\""]
          env: {RAW_PASS: ""}    // runner overrides via sealed_inputs below
      }
      sealed_inputs:      {RAW_PASS: "pass:registry/raw-pass"}
      sealed_input_modes: {RAW_PASS: "env"}

  Diceware-style passphrase (requires 'diceware' or similar; example
  uses pwgen):
      derivation: ["pwgen", "-s", "-1", "32"]

  Argon2-hashed password (for systems that take pre-hashed values):
      derivation: ["sh", "-c",
                   "echo -n \"$RAW_PASS\" | argon2 \"$(openssl rand -hex 16)\" -id -t 3 -m 16 -p 4 -e"]
      sealed_inputs: {RAW_PASS: "pass:bootstrap/raw-pass"}

  TIPS

  - Wrap multi-step derivations in 'sh -c' and quote carefully — argv
    bypasses shell expansion otherwise.
  - keep_trailing_newline: false (the default) is right for tools that
    print "value\\n". Set to true only if your tool emits a value with
    a meaningful trailing newline (rare).
  - For binary secrets that don't survive being sent over a JSON
    string, base64-encode in the derivation and base64-decode on the
    consumer side. The wire protocol is text-only.
  - Two-output secrets (an SSH keypair, a TLS cert+key) don't fit
    secret-gen — use the dedicated keypair-gen plugin instead.

WHAT SECRET-GEN IS NOT

  - Not a generic "run-once" toolchain. It only handles the
    derive-then-store pattern.
  - Not a way to read a secret. Use sealed_inputs for that.
  - Not aware of the value. mu sees the bytes only briefly,
    in memory, between the side-channel read and store_secret.

SEE ALSO

  mu guide secrets         The full secrets reference.
  docs/secret-gen-toolchain.md   In-depth design notes.
`)
}

func printGuideToolchains() {
	fmt.Print(`mu guide toolchains — bootstrapping toolchains from scratch

mu can download, verify, and bootstrap toolchains (compilers, runtimes)
from scratch, ensuring hermetic builds with known-good tool versions.

DECLARING A TOOLCHAIN

  In mu.cue:

  {
    "toolchains": [
      {
        "toolchain": "go",
        "from": "scratch",
        "config": {
          "version": "1.25.8",
          "url": "https://go.dev/dl/go1.25.8.darwin-arm64.tar.gz",
          "sha256": "abc123...",
          "strip_prefix": "go"
        }
      }
    ]
  }

CONFIG FIELDS

  toolchain       Name (must match plugin names that use this toolchain).
  from            Currently only "scratch" is supported.
  config.version  Version string (for cache key and display).
  config.url      Download URL for the toolchain archive.
  config.sha256   Expected SHA-256 hash of the download.
  config.strip_prefix  Directory prefix to strip from the archive (optional).

BUILD PROCESS

  mu scratch

  1. Check CAS for an existing toolchain manifest (cache hit → skip).
  2. Download archive and verify SHA-256.
  3. Extract (supports tar.gz, tar.xz, zip, and raw binaries).
  4. Verify the binary runs: <name> version or <name> --version.
  5. Walk extracted files, store each in CAS.
  6. Create a ToolchainManifest with an artifact map.
  7. Register in the ToolchainRegistry.

  'mu build' automatically runs scratch builds before planning if
  toolchains are defined.

USING TOOLCHAINS IN PLUGINS

  When mu plans a target, it passes toolchain_artifacts to the plugin:

    {"method": "plan", "toolchain_artifacts": {"go": "/path/to/go"}, ...}

  Plugins use these paths instead of system-installed tools.

EXTERNAL SCRATCH BUILDS

  Set MU_SCRATCH=<command> to delegate toolchain bootstrapping to an
  external process (e.g. a Nix-based builder).

COMMANDS

  mu scratch              Build all declared toolchains.
  mu scratch --no-cache   Force re-download and rebuild.
  mu cache ls --toolchains   List cached toolchains.
  mu cache inspect <name>    Inspect a cached toolchain.
`)
}

func printGuideShell() {
	fmt.Print(`mu guide shell — the built-in shell toolchain

The "shell" toolchain is built into mu — no external plugin needed.
Use it for simple tasks, aggregation targets, and custom scripts.

BASIC USAGE

  {
    "target": "//test",
    "toolchain": "shell",
    "sources": ["test.sh"],
    "config": {
      "command": ["bash", "test.sh"],
      "outputs": ["results.xml"]
    }
  }

CONFIG FIELDS

  command           []string (required) — the command to run.
  env               map[string]string (optional) — environment variables.
  network           bool (optional, default false) — allow network access.
  impure            bool (optional, default true) — skip CAS cache.
  outputs           []string (optional) — declared output files.
  observe_command   []string (optional) — command for drift detection.

KIT TARGETS (AGGREGATION)

  Use kind "kit" with deps to aggregate multiple targets:

  {
    "target": "//lint",
    "toolchain": "shell",
    "kind": "kit",
    "sources": [],
    "deps": ["//lint/go-vet", "//lint/gofmt"],
    "config": {
      "command": ["true"],
      "impure": false
    }
  }

  Kit targets run after all their deps complete. They can observe
  aggregated state from their dependencies.

IMPURE VS PURE

  Shell targets default to impure (skip cache). Set "impure": false
  for cacheable operations like linting or code generation where the
  output is fully determined by the inputs.
`)
}

func printGuideSecretProviders() {
	fmt.Print(`mu guide secret-providers — authoring secret-aware plugins

This is the plugin-author counterpart to 'mu guide secrets'. The user
guide tells target authors how to USE sealed_inputs / sealed_outputs;
this guide tells plugin authors how to IMPLEMENT them correctly.

Two roles a plugin can play, independently:

  1. SECRET PROVIDER — owns a ref scheme (e.g. "pass:") and serves
     resolve_secret and/or store_secret. Today: 'pass', 'sops'.

  2. SECRET CONSUMER / EMITTER — an action plugin (k8s, terraform,
     remote-exec, …) that accepts sealed_inputs as runtime values and
     optionally redirects sensitive outputs through sealed_outputs.

Roles are orthogonal. A plugin can be a pure provider (pass), a pure
consumer (k8s), an emitter (terraform with sensitive outputs), or all
of the above.

────────────────────────────────────────────────────────────────────
ROLE 1 — IMPLEMENTING A SECRET PROVIDER
────────────────────────────────────────────────────────────────────

CAPABILITIES

  Declare what you support in discover.capabilities:

    "capabilities": ["discover", "resolve_secret", "store_secret"]

  A read-only provider declares only "resolve_secret"; a write-only
  provider declares only "store_secret". Most providers do both.

REF GRAMMAR

  Pick a scheme that matches your plugin name. mu strips the scheme
  before calling you — your handler sees the path only.

    User writes:   "pass:deploy/k8s/token"
    Plugin gets:   {"method": "resolve_secret", "secret_ref": "deploy/k8s/token"}

  Document the grammar in your GUIDE.md, including any sub-prefixes
  (e.g. "pass:raw:..." for full-content vs. first-line semantics).
  Treat the grammar as a stable contract — users encode refs into
  mu.cue files, and changes break their builds.

resolve_secret

  Request:   {"method": "resolve_secret", "secret_ref": "<path>"}
  Response:  {"value": "<resolved bytes>"}
  Error:     {"value": "", "error": "<reason>"}

  Return the value as a string. Binary secrets should be base64'd by
  the consumer, not the provider — keep the wire shape simple.

  The value MUST NOT be logged to stderr. Plugin stderr is captured
  in tagged ring buffers and surfaced by 'mu plugin info' / verbose
  builds. Treat any stderr write as public.

store_secret

  Request:   {"method": "store_secret",
              "secret_ref":   "<path>",
              "secret_value": "<bytes>",
              "secret_mode":  "create" | "overwrite" | "create_if_absent"}
  Response:  {} on success
  Error:     {"error": "<reason>"}

  Mode semantics (REQUIRED — runners depend on these being honored):

    create             Fail if the ref already exists.
    overwrite          Always set; create if missing.
    create_if_absent   No-op if exists, create if missing.
                       (This is what secret-gen relies on.)

  The mode comes from the target's sealed_output_modes, defaulting
  to "overwrite". Validate it and reject anything else explicitly.

  Like resolve_secret, never log the value. If your backend's CLI
  echoes it, redirect that output to /dev/null in your wrapper.

WRITE POLICY (writable_refs)

  The runner enforces secrets.writable_refs at plan time and again
  in the writer closure — your plugin does not need to filter. But
  the GUIDE.md SHOULD show users a representative writable_refs
  pattern for your scheme so they know what to allow.

────────────────────────────────────────────────────────────────────
ROLE 2 — CONSUMING SEALED INPUTS IN AN ACTION PLUGIN
────────────────────────────────────────────────────────────────────

WHAT YOU RECEIVE

  In a "plan" request you get the target's declared sealed_inputs
  and sealed_input_modes maps:

    target.sealed_inputs       NAME -> "scheme:path"   (still refs)
    target.sealed_input_modes  NAME -> "env" | "file"

  Refs are NOT resolved at plan time. They are resolved by the
  runner just before each action runs and delivered per-mode. Your
  plan output should pass them through unchanged on the ActionSpec:

    {"sealed_inputs":      {"SSH_PASS": "pass:hosts/foo"},
     "sealed_input_modes": {"SSH_PASS": "env"}}

  The runner then handles resolution + delivery; you only see the
  resolved value via $NAME (env mode) or as a path in $NAME (file
  mode) when your action runs.

DELIVERY MODE GUIDANCE

  - Short single-line secrets (passwords, tokens) → env mode.
  - Multi-line / binary / >4 KB secrets (SSH keys, kubeconfigs,
    GCP service-account JSON) → recommend 'file' in your GUIDE.md.
  - Sandbox-pinned (toolchain-built) actions reject file mode.
    Document this if your action runs sandboxed.

NETWORK + IMPURE

  Sealed inputs do not by themselves force impure. The action stays
  cache-eligible on (refs, modes) — refresh on rotation requires
  changing the ref or marking the action impure.

────────────────────────────────────────────────────────────────────
ROLE 3 — EMITTING SECRETS VIA SEALED OUTPUTS
────────────────────────────────────────────────────────────────────

If your action would otherwise produce sensitive material on stdout
or in a declared output file (a generated key, a bootstrap token,
sensitive Terraform outputs, kubectl-extracted secret data), route
it through sealed_outputs instead so the bytes never enter CAS.

PLAN-TIME

  Pass through the target's sealed_outputs (and sealed_output_modes)
  unchanged on each ActionSpec that produces them:

    {"id": "render-token",
     "command": ["./gen-token.sh"],
     "sealed_outputs":      {"TOKEN": "pass:bootstrap/token"},
     "sealed_output_modes": {"TOKEN": "create_if_absent"}}

  The runner sets $MU_SEALED_OUT_DIR before the action runs. Your
  command must write each named output as $MU_SEALED_OUT_DIR/<name>
  (NOT as a regular file output).

RUNTIME CONTRACT

  - $MU_SEALED_OUT_DIR is a per-action 0700 dir, removed after the
    action regardless of success. Files inside are 0600.
  - Missing files are an error if declared. Extra files are ignored.
  - Actions with non-empty sealed_outputs are FORCED IMPURE — the
    runner skips the cache so the store_secret side-effect always
    runs. Document this in your GUIDE.md if relevant.

────────────────────────────────────────────────────────────────────
WHAT TO DOCUMENT IN GUIDE.md
────────────────────────────────────────────────────────────────────

If your plugin participates in any role above, your GUIDE.md needs a
SECRETS section. Recommended subsections (drop any that don't apply):

  - Sealed inputs accepted: each NAME, what it's for, and supported
    delivery modes (env / file).
  - Sealed outputs emitted: each NAME, the expected ref scheme, and
    which sealed_output_modes are meaningful.
  - Ref grammar (providers only): full syntax, sub-prefixes, examples.
  - A worked writable_refs pattern users can paste into mu.cue.
  - Caveats: forced-impure actions, sandbox + file-mode incompat,
    rotation behavior.

See plugins/pass/GUIDE.md for a provider example, and
plugins/host/GUIDE.md / plugins/remote-exec/GUIDE.md for consumer
examples.

────────────────────────────────────────────────────────────────────
SEE ALSO
────────────────────────────────────────────────────────────────────

  mu guide secrets              User-facing reference.
  mu guide secret-gen           secret-gen toolchain (calls store_secret).
  mu guide protocol             Wire format for resolve_secret / store_secret.
  docs/secrets-write-policy.md  writable_refs allow-list.
  docs/sealed-input-delivery-modes.md   env vs file delivery details.
`)
}

func printGuideProtocol() {
	fmt.Print(`mu guide protocol — the NDJSON plugin protocol

Plugins communicate with mu over stdin/stdout using NDJSON (newline-
delimited JSON). Each line is a complete JSON object.

LIFECYCLE

  1. mu starts the plugin process.
  2. mu sends {"method": "discover"} — plugin replies with metadata.
  3. mu sends plan/observe/resolve_secret requests as needed.
  4. mu closes stdin when done — plugin exits.

METHODS

  discover (required)
    Request:  {"method": "discover"}
    Response: {
      "name": "myplugin",
      "version": "0.1.0",
      "protocol_version": 1,
      "consumes": ["source:any"],
      "produces": ["binary"],
      "capabilities": ["discover", "plan", "observe"],
      "config_schema": {"output": {"type": "string"}}
    }

  plan (required)
    Request:  {
      "method": "plan",
      "target": {"name": "//app", "toolchain": "go", "sources": [...], "config": {...}},
      "deps": [{"target": "//lib", "artifacts": {"binary": "lib/libfoo.a"}}],
      "toolchain_artifacts": {"go": "/path/to/go"}
    }
    Response: {
      "actions": [
        {
          "id": "compile",
          "command": ["go", "build", "-o", "app", "./cmd/app"],
          "inputs": {"main.go": "main.go"},
          "outputs": ["app"],
          "depends_on": [],
          "env": {"GOOS": "linux"},
          "network": false
        }
      ],
      "declared_outputs": {"binary": "app"}
    }

  observe (optional)
    Request:  {
      "method": "observe",
      "target": {"name": "//infra/vpc", "toolchain": "aws", "config": {...}},
      "secrets": {"AWS_SECRET_KEY": "actual-value"}
    }
    Response: {
      "current": {
        "records": [
          {"_schema": "aws.ec2.vpc", "vpc_id": "vpc-123", "cidr_block": "10.0.0.0/16"}
        ]
      }
    }

  resolve_secret (optional)
    Request:  {"method": "resolve_secret", "secret_ref": "deploy/token"}
    Response: {"value": "the-secret-value"}

  store_secret (optional, paired with resolve_secret)
    Request:  {"method": "store_secret",
               "secret_ref": "deploy/token",
               "secret_value": "...",
               "secret_mode": "create" | "overwrite" | "create_if_absent"}
    Response: {} on success, {"error": "..."} on failure.
    The plugin must implement all three modes; create_if_absent
    is a no-op if the ref already exists. Capability:
    "store_secret" must be present in discover.

TARGET INFO FIELDS

  Target info is sent in plan and observe requests. Fields beyond
  the obvious name/toolchain/sources/config:

  sealed_inputs       map[NAME]ref — secret refs to resolve before exec.
  sealed_input_modes  map[NAME]mode — "env" (default) or "file".
                      file mode writes the value to a 0600 temp
                      file under a per-action sealed-input dir
                      and exports the path as $NAME.
  sealed_outputs      map[NAME]ref — destinations to capture
                      after exec. Action writes the value to
                      $MU_SEALED_OUT_DIR/<NAME>; mu routes
                      through the scheme's plugin's store_secret.

  Plugins are responsible for forwarding these fields onto the
  ActionSpecs they emit (TargetInfo carries them; ActionSpec
  honors them at exec time).

ACTION SPEC FIELDS

  id                  Unique within the subgraph.
  command             []string — the command to execute.
  inputs              map[name]path — input files (paths resolved to CAS digests).
  outputs             []string — declared output file paths.
  depends_on          []string — IDs of actions this depends on (within subgraph).
  env                 map[string]string — environment variables (optional).
  sealed_inputs       map[NAME]ref — secret refs, resolved at runtime (optional).
  sealed_input_modes  map[NAME]mode — "env" (default) or "file" (optional).
  sealed_outputs      map[NAME]ref — destinations for $MU_SEALED_OUT_DIR/<NAME>
                      capture (optional). Forces impure.
  sealed_output_modes map[NAME]mode — store mode per output:
                      "create" | "overwrite" | "create_if_absent".
                      Default "overwrite".
  network             bool — whether action needs network (honor system).
  work_dir            string — working directory relative to project root (optional).
  impure              bool — skip CAS cache (optional; forced true
                      when sealed_outputs is non-empty).
  timeout_s           int — per-attempt wall-clock timeout in seconds (optional).
  retries             int — additional attempts on failure (network actions only).
  retry_backoff_ms    int — sleep between retries.

INTER-ACTION REFERENCES

  Inputs can reference outputs of other actions in the same subgraph:
    "inputs": {"lib.a": "{action:compile-lib}"}
  mu resolves these to the actual output digests after execution.

CROSS-TARGET ARTIFACTS

  A plan request includes "deps", one entry per listed dependency:
    "deps": [{"target": "//lib/crypto",
              "artifacts": {"go_library": "lib/crypto/libcrypto.a"}}]
  The artifacts map comes from each dep's declared_outputs (artifact-type
  name → project-relative path). To consume one, declare the path as a
  normal input:
    "inputs": {"cryptolib": "lib/crypto/libcrypto.a"}
  mu detects that the path is produced by an already-planned action,
  defers the input digest, and injects an implicit DependsOn edge to the
  producing action so the consumer runs after the producer.

  See 'mu guide pudl' for the terraform → pudl ingest end-to-end example.
`)
}

// printGuideForPlugin finds and prints the guide text for a named plugin.
// It searches in order:
//  1. Extracted CAS bundles in ~/.mu/plugins/<name>/bundle-*/
//  2. Local plugin directory in the current project (plugins/<name>/)
func printGuideForPlugin(name string) int {
	// 1. Check extracted bundles in ~/.mu/plugins/<name>/bundle-*/.
	home, err := os.UserHomeDir()
	if err == nil {
		bundleDirs, _ := filepath.Glob(filepath.Join(home, ".mu", "plugins", name, "bundle-*"))
		for _, dir := range bundleDirs {
			if path := findGuideInDir(dir); path != "" {
				return printGuideFile(name, path)
			}
		}
	}

	// 2. Check local plugin directory in the current project.
	projectRoot, err := findGuideProjectRoot()
	if err == nil {
		localDir := filepath.Join(projectRoot, "plugins", name)
		if path := findGuideInDir(localDir); path != "" {
			return printGuideFile(name, path)
		}
	}

	fmt.Fprintf(os.Stderr, "mu guide plugin %s: no guide found\n", name)
	fmt.Fprintf(os.Stderr, "\nTo add a guide, create a GUIDE.md file in the plugin directory\n")
	fmt.Fprintf(os.Stderr, "and set \"guide\": \"GUIDE.md\" in the plugin manifest:\n\n")
	fmt.Fprintf(os.Stderr, "  plugins/%s/GUIDE.md\n", name)
	fmt.Fprintf(os.Stderr, "  plugins/%s/mu.cue → {\"plugin\": {\"guide\": \"GUIDE.md\", ...}}\n", name)
	return 1
}

// findGuideInDir looks for a guide file in a plugin directory.
// It checks the manifest first (for the declared guide path), then
// falls back to conventional filenames.
func findGuideInDir(dir string) string {
	if cfg, err := config.LoadPluginManifest(dir); err == nil && cfg.Plugin != nil && cfg.Plugin.Guide != "" {
		guidePath := filepath.Join(dir, cfg.Plugin.Guide)
		if _, err := os.Stat(guidePath); err == nil {
			return guidePath
		}
	}

	// Fall back to conventional filenames.
	for _, name := range []string{"GUIDE.md", "GUIDE", "guide.md", "guide.txt"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// findGuideProjectRoot finds the mu project root for guide lookups.
func findGuideProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return config.FindProjectRoot(cwd)
}

// printGuideFile reads and prints a guide file.
func printGuideFile(pluginName, path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu guide plugin %s: %v\n", pluginName, err)
		return 1
	}

	content := strings.TrimRight(string(data), "\n")
	fmt.Println(content)
	return 0
}
