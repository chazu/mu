mu guide plugin lint — language-agnostic linter wrapper

Wraps any linter command as a mu plugin. `mu build` runs the check
`command` and fails the build when it exits non-zero; it never rewrites
sources unless the target opts in with `fix: true`.

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
  fix            When true, plan fix_command instead of command
                 (optional, default false). Planning fails if fix is
                 true and fix_command is unset.
  env            Complete environment for the command (optional). When
                 unset the action inherits mu's environment (PATH, HOME,
                 ...); when set it replaces it, so include PATH.
  working_dir    Working directory for the command, relative to the
                 project root (optional; becomes the action's work_dir).

EXAMPLES

  Go vet:
    {"command": ["go", "vet", "./..."]}

  Gofmt check (`gofmt -l` exits 0 even when it lists files, so wrap
  it to fail on any output):
    {"command": ["sh", "-c",
       "out=$(gofmt -l .) || exit 1; [ -z \"$out\" ] || { echo \"$out\"; exit 1; }"]}

  Gofmt auto-fix (rewrites files in place; keep it out of //... by
  enabling fix only when you mean it):
    {"command": ["gofmt", "-l", "."],
     "fix_command": ["gofmt", "-w", "."],
     "fix": true}

  ESLint with env:
    {"command": ["eslint", "src/"],
     "fix_command": ["eslint", "--fix", "src/"],
     "env": {"NODE_ENV": "development"}}

  Aggregate lint target (using shell kit):

    targets: [{
      target: "//lint"
      toolchain: "shell"
      sources: []
      deps: ["//lint/go-vet", "//lint/gofmt"]
      config: {command: ["true"], impure: false}
    }]

ACTIONS GENERATED

  lint    Runs command (or fix_command when fix is true) in the
          project tree. The action is impure, so it is never cached and
          re-runs on every build; a non-zero exit fails the build.

CAPABILITIES

  discover, plan
