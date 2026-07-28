mu guide plugins — writing, loading, and distributing plugins

Plugins extend mu with support for new toolchains and resource types.
They are external processes that communicate via NDJSON (see 'mu guide protocol').

LOADING PLUGINS

  There are four ways to reference a plugin. When a def sets more than
  one, they resolve in this precedence order (url → script → digest →
  command):

  1. Remote URL with SHA256 verification:
     {"name": "file", "url": "https://example.com/file-plugin.bb", "sha256": "abc123..."}

  2. Local script (preferred for development):
     {"name": "file", "script": "plugins/file/plugin.bb"}

     Also works with a directory containing mu.cue with a "plugin" key:
     {"name": "go", "script": "plugins/go"}

  3. CAS digest (for distribution — previously built plugin):
     {"name": "file", "digest": "sha256:abc123..."}

  4. Direct command (escape hatch, not cached):
     {"name": "file", "command": ["python3", "my-plugin.py"]}

WRITING A PLUGIN

  A plugin is any executable that reads NDJSON on stdin and writes NDJSON
  on stdout. It must implement "discover". Plugins that build or converge
  targets implement "plan"; observer-only and secret-provider plugins may omit
  it.
  Optionally it can implement "observe", "resolve_secret", and
  "store_secret".

  Capabilities:
    discover         (required)  Identify the plugin and its protocol version.
    plan             (optional)  Translate a target into action specs.
    observe          (optional)  Report current state for drift detection.
    resolve_secret   (optional)  Read a secret value by ref.
    store_secret     (optional)  Write a secret value by ref.

  resolve_secret + store_secret together make a plugin a full
  bidirectional secret provider. See 'mu guide secret-providers'
  for the plugin-author walkthrough (ref grammars, modes, sealed
  inputs/outputs, forced-impure semantics).

  RECOMMENDED: Go SDK

    Use sdk/muplugin to author plugins in Go. The SDK handles NDJSON,
    capability advertisement, and error envelopes. Capabilities are
    derived from which optional func fields are non-nil — there is
    no separate capabilities list to keep in sync.

      package main

      import (
          "context"
          "github.com/chazu/mu/sdk/muplugin"
      )

      func main() {
          (&muplugin.Plugin{
              Name:     "myplugin",
              Version:  "0.1.0",
              Produces: []string{"myartifact"},
              Plan:     plan,
          }).Main()
      }

      func plan(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
          return muplugin.PlanResponse{
              Actions: []muplugin.ActionSpec{{
                  ID:      "run",
                  Command: []string{"echo", "hello"},
                  Inputs:  map[string]string{},
                  Outputs: []string{},
              }},
              Outputs: map[string]string{},
          }, nil
      }

    Build with `go build -o myplugin .` and reference the binary in
    mu.cue. Full SDK reference: 'mu guide sdk'. Canonical example:
    examples/plugins/hello-go/.

    Reference ports in this repo: plugins/{scratch,file,host,
    keypair-gen,pass,remote-exec,remote-file}/main.go.

  OTHER LANGUAGES

    Any executable that speaks the NDJSON protocol works. Babashka
    (.bb), Python, Rust, shell — all fine. The Go SDK is the
    recommended path because it eliminates manual capability bookkeeping
    and ships with an in-process test harness, but the wire protocol
    is the real contract.

    Minimal plugin in Babashka:

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
          "plan"     {"actions" [{"id"      "run"
                                  "command" ["echo" "hello"]
                                  "inputs"  {}
                                  "outputs" []
                                  "depends_on" []}]
                      "declared_outputs" {}}
          {"error" (str "unknown method: " (get req "method"))}))

      (loop []
        (when-let [line (read-line)]
          (println (json/generate-string (handle-request (json/parse-string line))))
          (flush)
          (recur)))

  PORTING A BB PLUGIN TO GO

    Almost every bb idiom has a one-line Go equivalent:

      bb                                     Go (sdk/muplugin)
      ────────────────────────────────────────────────────────────────
      (case (get req "method") ...)          SDK dispatches automatically
      (defn discover-response [] {...})      Plugin{Name, Version, ...}
      (defn plan-response [req] {...})       Plan: func(ctx, req) {...}
      (defn observe-response [req] {...})    Observe: func(ctx, req) {...}
      (json/generate-string)                 SDK handles all encoding
      (json/parse-string line)               SDK handles all decoding
      (read-line) loop                       SDK dispatch loop
      "capabilities" ["discover" "plan"]     auto-derived from handlers
      (clojure.string/split s #"/")          strings.Split(s, "/")
      cheshire map → JSON object             map[string]any
      throwing on bad config                 return error from handler

    Action shapes (id/command/inputs/outputs/depends_on/env/network)
    map field-for-field via muplugin.ActionSpec.

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

OFFICIAL SOURCE-PACKAGE CATALOG

  The official catalog is published by the public chazu/mu-plugins GitHub
  releases. Search it without a project:

    mu plugin search
    mu plugin search aws

  Install a pinned or latest catalog package from inside a mu project:

    mu plugin install aws
    mu plugin install aws@0.1.0

  Installation downloads the immutable release asset, verifies its SHA-256,
  builds source-only packages when the catalog declares a build command,
  bundles the plugin into the local CAS, extracts it under ~/.mu/plugins/, and
  writes a digest entry into mu.cue. The selected catalog release, asset hash,
  and local bundle digest are recorded in mu.lock. The lock entry also keeps
  the package's vendored wire schemas and PUDL mappings, and installation
  writes the same metadata to `mu-plugin.json` inside the extracted bundle so
  downstream tools can inspect an installed package without its source tree.

  Update one or all catalog-installed plugins:

    mu plugin update aws
    mu plugin update

  Inspect the lockfile with `mu plugin lock` or `mu plugin lock --json`.
  Override the catalog for a mirror or local test server with
  `--catalog URL` or `MU_PLUGIN_CATALOG`. The URL must serve the generated
  catalog JSON; package assets are still fetched from the HTTPS asset URLs
  recorded in that catalog.

  Local `script:` plugins, digest references, direct command plugins, and OCI
  plugin push/pull remain supported. Catalog installation is a source-package
  distribution path layered on top of the same CAS resolver.

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

  A plugin can declare one or more CUE schemas for the data it produces so that
  pudl classifies imports under a meaningful type instead of the
  catchall pudl/core.#Item.

  1. Add output_schema for one default schema, or output_schemas for
     resource-specific schemas, to the discover response:

       "output_schema": {"module":     "mu/aws",
                         "version":    "v1",
                         "definition": "#EC2Instance"}

     For plugins that emit multiple resource types:

       "output_schemas": [{"resource_type": "aws.ec2.instance",
                           "module":        "mu/aws",
                           "version":       "v1",
                           "definition":    "#EC2Instance"},
                          {"resource_type": "aws.ec2.vpc",
                           "module":        "mu/aws",
                           "version":       "v1",
                           "definition":    "#VPC"}]

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
