mu guide plugin lint — language-agnostic linter wrapper

Wraps any linter command as a mu plugin. Supports running linters,
auto-fix commands, and observing lint status.

USAGE IN mu.cue

  targets: [{
    target: "//lint/go-vet"
    toolchain: "lint"
    sources: ["cmd/**/*.go", "internal/**/*.go"]
    config: command: ["go", "vet", "./..."]
  }]

CONFIG FIELDS

  command        Linter command as array (required).
                 e.g. ["golangci-lint", "run"], ["eslint", "src/"]
  fix_command    Auto-fix command as array (optional).
                 e.g. ["gofmt", "-w", "."], ["eslint", "--fix", "src/"]
  env            Environment variables as map (optional).
  working_dir    Working directory for the command (optional).

EXAMPLES

  Go vet:
    {"command": ["go", "vet", "./..."]}

  Gofmt with auto-fix:
    {"command": ["gofmt", "-l", "."],
     "fix_command": ["gofmt", "-w", "."]}

  ESLint with env:
    {"command": ["eslint", "src/"],
     "fix_command": ["eslint", "--fix", "src/"],
     "env": {"NODE_ENV": "development"}}

  Aggregate lint target (using shell kit):

    targets: [{
      target: "//lint"
      toolchain: "shell"
      kind: "kit"
      sources: []
      deps: ["//lint/go-vet", "//lint/gofmt"]
      config: {command: ["true"], impure: false}
    }]

OBSERVATION

  mu observe //lint/go-vet

  Runs the lint command and returns exit code and output. Convergence
  decisions are made by the caller — the plugin just reports results.

ACTIONS GENERATED

  lint    Runs fix_command if available, otherwise the regular command.
          Sources are used as inputs for cache keying.

CAPABILITIES

  discover, plan, observe
