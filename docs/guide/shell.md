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
  network           bool (optional, default false) — allow network access.
  impure            bool (optional, default true) — skip CAS cache.
  outputs           []string (optional) — declared output files.
  host_paths        []string (optional) — absolute host paths (files or
                    directories) the command may read and execute inside
                    the sandbox, e.g. ["/opt/homebrew/bin"] for a pure
                    target running a Homebrew tool. See `mu guide protocol`.
  observe_command   []string (optional) — command for drift detection.

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

IMPURE VS PURE

  Shell targets default to impure (skip cache). Set "impure": false
  for cacheable operations like linting or code generation where the
  output is fully determined by the inputs.
