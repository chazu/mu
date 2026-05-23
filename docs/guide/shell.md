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
