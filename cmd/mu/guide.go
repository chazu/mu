package main

import (
	"encoding/json"
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
	case "mu.json":
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
	case "toolchains":
		printGuideToolchains()
	case "shell":
		printGuideShell()
	case "protocol":
		printGuideProtocol()
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

Topics:

  mu guide mu.json       How to write and structure mu.json config files
  mu guide build         Building targets: flags, plan mode, manifests
  mu guide plugins       Writing, loading, and distributing plugins
  mu guide protocol      The NDJSON plugin protocol (discover, plan, observe)
  mu guide observe       Drift detection: observing current state
  mu guide pudl          How mu and pudl work together
  mu guide cache         Content-addressed storage and action caching
  mu guide secrets       Sealed inputs and secret resolution
  mu guide toolchains    Bootstrapping toolchains from scratch
  mu guide shell         The built-in shell toolchain
  mu guide plugin <name> Show guide text bundled with a plugin
`)
}

func printGuideMuJSON() {
	fmt.Print(`mu guide mu.json — configuration file reference

mu.json is the project configuration file. mu discovers it by walking up
from the current directory. Subdirectories may contain their own mu.json
files — they are merged automatically.

MINIMAL EXAMPLE

  {
    "plugins": [
      {"name": "go", "script": "plugins/go/plugin.bb"}
    ],
    "targets": [
      {
        "target": "//cmd/myapp",
        "toolchain": "go",
        "sources": ["cmd/myapp/*.go", "internal/**/*.go"],
        "config": {"output": "myapp", "pkg": "./cmd/myapp"}
      }
    ]
  }

TOP-LEVEL KEYS

  targets       Array of build targets (see below).
  plugins       Array of plugin definitions (see 'mu guide plugins').
  toolchains    Array of toolchain bootstrap definitions (see 'mu guide toolchains').
  cache         Cache configuration (optional).
  preprocessor  Config preprocessor for non-JSON formats (optional).

TARGET FIELDS

  target          Target name. Convention: "//path/to/name".
  toolchain       Which plugin handles this target (e.g. "go", "file", "shell").
  sources         Array of source file paths or globs (e.g. "src/**/*.go").
  deps            Array of target names this depends on (optional).
  config          Toolchain-specific config object (optional).
  sealed_inputs   Secret references: {"ENV_VAR": "scheme:path"} (optional).
  kind            BRICK classification: "relationship", "interface", "component", "kit" (optional).
  implements      Interface this component satisfies (optional).

TARGET NAMING

  Targets are named "//path/name". Subdirectory targets are auto-prefixed:
  a target "mylib" in foo/mu.json becomes "//foo/mylib".

CONFIG MERGING

  mu walks up to find the project root mu.json, then recursively discovers
  mu.json files in subdirectories. Targets from subdirectories are merged
  with paths rebased relative to the project root. Globs in sources are
  expanded. Hidden directories (.git, .claude, etc.) and testdata/ are skipped.

PREPROCESSOR

  For non-JSON config formats (CUE, YAML, TOML), define a preprocessor:

  {
    "preprocessor": {
      "extension": ".cue",
      "command": ["cue", "export", "--out", "json"]
    }
  }

  mu pipes files with matching extension through the command before parsing.

CACHE CONFIG

  {
    "cache": {
      "backends": [
        {"type": "disk", "path": "~/.mu/cache", "max_size": "10GB"},
        {"type": "oci", "registry": "ghcr.io/org/cache"}
      ],
      "write_through": true,
      "read_repair": true
    }
  }
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
  --config PATH     Path to mu.json (default: discover by walking up).
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
  6. Merge action subgraphs into a unified DAG.
  7. Execute DAG: topological sort, worker pool, cache check, run, store.

ACTION CACHING

  Each action's cache key is computed from: command, sorted input digests,
  env vars, and network flag. Sealed inputs are excluded from cache keys.
  On cache hit, outputs are restored from CAS without re-execution.

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

     Also works with a directory containing mu.json with a "plugin" key:
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
  Optionally it can implement "observe" and "resolve_secret".

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
      mu.json        Declares plugin metadata and build target.
      plugin.bb      The plugin script.
      GUIDE.md       Optional guide text (shown by 'mu guide plugin myplugin').

  The mu.json has a "plugin" key:

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
                          in mu.json. After this, the plugin can be referenced
                          by digest for reproducible builds.

  mu plugin list                 List plugins defined in mu.json.
  mu plugin list --discover      Start plugins and show capabilities.
  mu plugin list --cached        Show all plugins stored in ~/.mu/plugins/.
  mu plugin list --json          Output as JSON.

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
  --config    Path to mu.json.
  --verbose   Show plugin I/O.

EXAMPLES

  mu observe //infra/aws-inventory
  mu observe --json //infra/aws-inventory
  mu observe --ndjson //infra/aws-inventory | pudl import --stdin

HOW IT WORKS

  1. mu loads the target from mu.json.
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

mu and pudl are decoupled tools that communicate through mu.json.

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

     This produces a mu.json with desired-state targets:
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

KEY DESIGN PRINCIPLE

  pudl emits desired state, not drift diffs. The file plugin receives
  {"path": "...", "content": "..."} — it doesn't know about pudl, CUE,
  or drift reports. It just makes the file match the config. Any mu plugin
  works whether the target came from pudl or a hand-written mu.json.

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

  Each build action has a cache key computed from:
  - Command (argv)
  - Sorted input digests (sha256 of each input file)
  - Environment variables
  - Network flag

  Sealed inputs (secrets) are deliberately excluded from cache keys.
  Impure actions skip the cache entirely.

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
	fmt.Print(`mu guide secrets — sealed inputs and secret resolution

Sealed inputs allow targets and actions to use secrets (API keys, tokens)
without exposing them in the build graph, cache, or logs.

DECLARING SEALED INPUTS

  In mu.json targets:

  {
    "target": "//deploy/app",
    "toolchain": "k8s",
    "sources": ["deploy/*.yaml"],
    "sealed_inputs": {
      "KUBECONFIG_TOKEN": "pass:deploy/k8s-token",
      "AWS_SECRET_KEY":   "pass:aws/secret-key"
    }
  }

  Format: "ENV_VAR_NAME": "scheme:path"
  The scheme maps to the plugin that resolves the secret.

SECRET RESOLUTION

  1. During planning, sealed_inputs are recorded but NOT resolved.
  2. Before execution (or observation), mu finds the plugin matching
     the scheme prefix (e.g. "pass" → pass plugin).
  3. mu sends a "resolve_secret" request to that plugin:
     {"method": "resolve_secret", "secret_ref": "deploy/k8s-token"}
  4. The plugin returns: {"value": "the-actual-secret"}
  5. mu injects the secret as an environment variable during action execution.

SECURITY GUARANTEES

  - Secrets are NEVER stored in CAS.
  - Secrets are NEVER included in action cache keys.
  - Secrets are NEVER logged or included in build manifests.
  - Secrets are only held in memory during execution.

THE PASS PLUGIN

  The bundled "pass" plugin resolves secrets via password-store (pass).
  It implements the "resolve_secret" capability.

  {"name": "pass", "script": "plugins/pass/plugin.bb"}

  References: "pass:path/to/secret" → runs 'pass show path/to/secret'.

WRITING A SECRET PROVIDER PLUGIN

  Add "resolve_secret" to capabilities in discover, then handle the method:

    "capabilities" ["discover" "resolve_secret"]

    "resolve_secret" (let [ref (get req "secret_ref")]
                       {"value" (fetch-secret-from-vault ref)})
`)
}

func printGuideToolchains() {
	fmt.Print(`mu guide toolchains — bootstrapping toolchains from scratch

mu can download, verify, and bootstrap toolchains (compilers, runtimes)
from scratch, ensuring hermetic builds with known-good tool versions.

DECLARING A TOOLCHAIN

  In mu.json:

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

ACTION SPEC FIELDS

  id            Unique within the subgraph.
  command       []string — the command to execute.
  inputs        map[name]path — input files (paths resolved to CAS digests).
  outputs       []string — declared output file paths.
  depends_on    []string — IDs of actions this depends on (within subgraph).
  env           map[string]string — environment variables (optional).
  sealed_inputs map[string]string — secret refs, resolved at runtime (optional).
  network       bool — whether action needs network (honor system).
  work_dir      string — working directory relative to project root (optional).
  impure        bool — skip CAS cache (optional).

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
	fmt.Fprintf(os.Stderr, "  plugins/%s/mu.json → {\"plugin\": {\"guide\": \"GUIDE.md\", ...}}\n", name)
	return 1
}

// findGuideInDir looks for a guide file in a plugin directory.
// It checks the manifest first (for the declared guide path), then
// falls back to conventional filenames.
func findGuideInDir(dir string) string {
	// Try manifest-declared guide path.
	manifestPath := filepath.Join(dir, "mu.json")
	if data, err := os.ReadFile(manifestPath); err == nil {
		var manifest struct {
			Plugin *struct {
				Guide string `json:"guide"`
			} `json:"plugin"`
		}
		if json.Unmarshal(data, &manifest) == nil && manifest.Plugin != nil && manifest.Plugin.Guide != "" {
			guidePath := filepath.Join(dir, manifest.Plugin.Guide)
			if _, err := os.Stat(guidePath); err == nil {
				return guidePath
			}
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
