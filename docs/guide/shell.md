mu guide shell — the built-in shell toolchain

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
  network           bool (optional, default false) — allow network access
                    (enforced only for pure targets, in the sandbox).
  impure            bool (optional, default true) — run on the host and skip
                    the CAS cache; false runs in the sandbox and caches.
  outputs           []string (optional) — declared output files.
  host_paths        []string (optional) — absolute host paths (files or
                    directories) the command may read and execute inside
                    the sandbox, e.g. ["/opt/homebrew/bin"] for a pure
                    target running a Homebrew tool. See `mu guide protocol`.

  config is required. Any other field, or a field of the wrong type
  (network: "no"), is a load error.

  observe_command was removed along with mu observe; a config that still
  sets it fails to load.

AGGREGATION TARGETS

  A shell target with deps and a no-op command groups other targets
  under one name:

  {
    target:    "//lint"
    toolchain: "shell"
    sources: []
    deps: ["//lint/go-vet", "//lint/gofmt"]
    config: {
      command: ["true"]
      impure:  false
    }
  }

  Building //lint builds every dep; the "true" action itself does
  nothing.

  A target runs only after all of its deps complete: every action of
  //lint that has no predecessor inside //lint waits for every action of
  each dep (and, through them, of their deps). Listing a target in deps
  is enough to order it; the target need not read the dep's outputs.

IMPURE VS PURE

  Shell targets default to impure: the command runs on the host, sees the
  whole filesystem and network, and is never cached. Set "impure": false
  for cacheable operations like linting or code generation where the
  output is fully determined by the inputs. A pure shell target runs in the
  sandbox (`mu guide sandbox`): it sees only its sources, the system
  directories and its host_paths, has no network unless "network": true,
  and must declare relative outputs. A shell target cannot name a scratch
  toolchain, so a tool outside /bin and /usr/bin needs host_paths.
