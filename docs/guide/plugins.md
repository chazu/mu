mu guide plugins — writing, loading, and distributing plugins

Plugins extend mu with support for new toolchains and resource types.
They are external processes that communicate via NDJSON (see 'mu guide protocol').

LOADING PLUGINS

  There are four ways to reference a plugin. Each plugins[] entry sets
  exactly one of url, script, digest or command; an entry that sets none,
  or more than one, fails when mu.cue loads.

  1. Remote URL with SHA256 verification:
     {"name": "file", "url": "https://example.com/file-plugin.bb", "sha256": "abc123..."}

  2. Local script (preferred for development):
     {"name": "myplugin", "script": "plugins/myplugin/plugin.bb"}

     Also works with a directory containing mu.cue with a "plugin" key:
     {"name": "go", "script": "plugins/go"}
     {"name": "file", "script": "plugins/file"}   (Go: build the binary first)

  3. CAS digest (for distribution — previously built plugin):
     {"name": "file", "digest": "sha256:<64 lowercase hex characters>"}

     The digest must be a full SHA-256 identity; abbreviated prefixes are
     rejected when mu.cue loads and at resolution. A "url" plugin's
     "sha256" field must likewise be 64 lowercase hex characters (no
     "sha256:" prefix).

  4. Direct command (escape hatch, not cached):
     {"name": "file", "command": ["python3", "my-plugin.py"]}

WRITING A PLUGIN

  A plugin is any executable that reads NDJSON on stdin and writes NDJSON
  on stdout. It must implement "discover". Plugins that build targets
  implement "plan"; secret-provider plugins may omit it.
  Optionally it can implement "resolve_secret" and "store_secret".

  Capabilities:
    discover         (required)  Identify the plugin and its protocol version.
    plan             (optional)  Translate a target into action specs.
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
    mu.cue (see GO PLUGINS below). Full SDK reference: 'mu guide sdk'.
    Canonical example: examples/plugins/hello-go/.

    In-tree Go plugins: plugins/{file,pass,keypair-gen,remote-exec,
    remote-file}.

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
      (json/generate-string)                 SDK handles all encoding
      (json/parse-string line)               SDK handles all decoding
      (read-line) loop                       SDK dispatch loop
      "capabilities" ["discover" "plan"]     auto-derived from handlers
      (clojure.string/split s #"/")          strings.Split(s, "/")
      cheshire map → JSON object             map[string]any
      throwing on bad config                 return error from handler

    Action shapes (id/command/inputs/outputs/depends_on/env/network)
    map field-for-field via muplugin.ActionSpec.

  SEALED ACTION ROUTING

    TargetInfo carries sealed_inputs, sealed_input_modes, sealed_outputs,
    and sealed_output_modes. Emit explicit action claims using the exact
    target refs and effective modes. Each declared input must be claimed by
    at least one action; each output by exactly one. A single-action plan
    may leave its claims empty and mu fills them from the target maps;
    multi-action plans get nothing implicitly. Do not resolve provider
    values in the plugin planner: mu resolves inputs only when the claiming
    action is about to execute.

PLUGIN DIRECTORY STRUCTURE

  For bundling and distribution, plugins use this layout:

    plugins/myplugin/
      mu.cue        Declares plugin metadata.
      plugin.bb      The plugin script.
      GUIDE.md       Optional guide text (shown by 'mu guide plugin myplugin').

  The mu.cue has a "plugin" key:

    {
      "plugin": {
        "entrypoint": "plugin.bb",
        "toolchain": "bb",
        "files": ["plugin.bb"],
        "guide": "GUIDE.md"
      }
    }

  A project plugin entry with `script:` pointing into the directory is
  bundled into the local CAS on demand when a build resolves it.

GO PLUGINS

  mu never compiles a plugin. A Go plugin is run as a prebuilt
  executable; how that executable gets built depends on the path:

  Local plugin directory ("script": "plugins/<name>"):
    The manifest's entrypoint names the compiled binary and omits
    "toolchain" (no toolchain = execute directly):

      plugin: {
        entrypoint: "plugin"
        files: ["plugin", "GUIDE.md"]
        guide: "GUIDE.md"
      }

    Build the binary into the directory before mu resolves the plugin;
    resolution fails with "entrypoint ... not found" until you do:

      go build -trimpath -o plugins/<name>/plugin ./plugins/<name>

    mu then bundles the listed files (binary included) into a
    deterministic tar, stores it in the CAS and runs the extracted
    copy under ~/.mu/plugins/<name>/bundle-<hash>/. The plugin identity
    is that bundle digest, so it changes exactly when the binary (or
    another bundled file) changes; -trimpath keeps rebuilds of the same
    source with the same Go version byte-identical across checkouts.
    The in-tree convention is to name the binary "plugin" (ignored by
    .gitignore).

  Catalog install ('mu plugin install <name>'):
    The catalog entry declares the entrypoint and a build command, e.g.
    {"entrypoint": "plugin", "build": {"command": ["go", "build",
    "-trimpath", "-o", "plugin", "."]}}. If the entrypoint is missing
    from the downloaded source package, mu runs the build command in
    the package directory (Go must be installed), then bundles the
    directory as above and pins the digest in mu.cue and mu.lock.

  Direct command ("command": ["/path/to/binary"]):
    Not bundled or cached; useful while iterating on a plugin.

  Plugins written with sdk/muplugin are unit-tested in-process with
  muplugin.Exchange; see plugins/pass/main_test.go for an end-to-end
  test that registers a plugin directory the way a project does.

MANAGING PLUGINS

  mu plugin list [--json]        One table of the project's declared plugins
                                 and every version installed under
                                 ~/.mu/plugins. STATE reconciles a declared
                                 digest with the install cache: ok, missing,
                                 stale (other versions installed), or local
                                 (script/url/command; not checked).
                                 Installed-only plugins appear with no type.
  mu plugin info [--json] [--digest <sha256:...>] <name>
                                 Start one plugin, run discover, and show its
                                 capabilities, config schema, digest, and
                                 path. Project plugins (digest, url, script,
                                 or command) resolve exactly as in a build.
                                 Works outside any mu project.
  mu plugin install <name[@version]>
                                 Install a catalog plugin (below).

  list and info honour --config; a project mu.cue that fails to load is an
  error, not a silent fallback to installed plugins.

OFFICIAL SOURCE-PACKAGE CATALOG

  The official catalog is published by the public chazu/mu-plugins GitHub
  releases. Install a pinned or latest catalog package from inside a mu
  project:

    mu plugin install k8s
    mu plugin install k8s@0.4.0

  Installation downloads the immutable release asset, verifies its SHA-256,
  builds source-only packages when the catalog declares a build command,
  bundles the plugin into the local CAS, extracts it under ~/.mu/plugins/, and
  writes a digest entry into mu.cue. The selected catalog release, asset hash,
  and local bundle digest are recorded in mu.lock. Installation also writes
  the package's name, version, bundle digest and entrypoint to
  `mu-plugin.json` inside the extracted bundle so other tools can inspect an
  installed package without its source tree.

  To update a plugin, install the newer version: `mu plugin install k8s`
  installs the latest release and rewrites its mu.cue and mu.lock entries.
  Override the catalog for a mirror or local test server with
  `--catalog URL` or `MU_PLUGIN_CATALOG`. The URL must serve the generated
  catalog JSON; package assets are still fetched from the HTTPS asset URLs
  recorded in that catalog.

  The catalog is the only plugin distribution channel. Local `script:`
  plugins, digest references, and direct command plugins resolve through the
  same CAS resolver.

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
  The metadata side (capabilities, consumes/produces, config schema) is
  surfaced by 'mu plugin info' automatically — your GUIDE.md should
  cover everything info CAN'T tell them. Use this skeleton:

    mu guide plugin <name> — <one-line role: "k8s applier", "pass secret provider", ...>

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
      - State which actions claim which sealed names (mu rejects
        undeclared or unclaimed names at plan time).

    EXAMPLES
      One worked end-to-end example per major use case (one's fine
      if there's only one). Show the target, the plugins[] entry,
      and the 'mu build' invocation.

    TROUBLESHOOTING (optional but appreciated)
      The two or three failure modes you keep seeing in support.
      A grep-able error string + one-line fix is gold.

  Keep the guide short and copy-paste friendly. If a section has
  nothing to say for your plugin, drop it entirely — empty headings
  are noise. Live examples in this repo:

    plugins/pass/GUIDE.md         secret provider, ref grammar, modes
    plugins/sops/GUIDE.md         file-based provider, write policy
    plugins/remote-exec/GUIDE.md  consumer + emitter, sealed_output_files
    plugins/keypair-gen/GUIDE.md  generator, two-name sealed_outputs contract

CACHED VERSION SELECTION

  Project-based info/guide uses the configured plugin identity. Outside a
  project, a sole cached identity can be selected automatically. If several
  versions exist, info and guide report ambiguity instead of choosing a hash
  by directory order. Select a full digest explicitly:

    mu plugin info --digest sha256:<64 hex characters> <name>
    mu guide plugin --digest sha256:<64 hex characters> <name>
    mu plugin list --json

  --digest selects an already cached version, even inside a project; it does
  not fetch another version. Listing exposes all available identities.
  Entries left by older releases under a 12-hex short name (bundle-<12 hex>)
  are ignored: they are never listed or selected, and `mu cache clean`
  deletes them. Re-resolve such a plugin from its project (mu build) to
  materialize it under its full digest.

  Standalone plugin files publish through verified atomic replacement at
  plugin-<full digest>.<extension>. Failed copy/close/digest checks leave no
  advertised file. Older versions are retained for running processes.

LOCAL DEPENDENCIES DURING REGISTRY OUTAGES

  Planning resolves only plugins and toolchains needed by the selected closed
  target graph. An unused remote plugin does not block a local target. Provider
  plugins introduced by action-level sealed claims are resolved during planning,
  retained by exact identity, and started for execution without refetching.

  Full-digest extracted bundles and verified standalone files are checked before
  CAS/network probes. A cached native digest-only plugin no longer implies a
  Babashka download. Extracted toolchain binaries use verified full-digest paths,
  so a cached file of another version cannot be selected by name alone.

  mu plugin info --offline <name>
  mu guide plugin --offline <name>
  mu plugin list --json

  Catalog install supports --offline with a local file:// catalog and
  already-resolved artifacts. It fails rather than downloading missing
  package data.
