mu guide plugin go — Go toolchain plugin

Compiles Go packages into executables, archives, or shared libraries.

USAGE IN mu.cue

  targets: [{
    target: "//cmd/myapp"
    toolchain: "go"
    sources: ["go.mod", "go.sum", "cmd/myapp/*.go", "internal/**/*.go"]
    config: {
      output: "myapp"
      pkg: "./cmd/myapp"
      trimpath: true
    }
  }]

CONFIG FIELDS

  output      Output binary name (default: last segment of target name).
  pkg         Package path to build (default: ".").
  goos        GOOS cross-compilation target (e.g. "linux").
  goarch      GOARCH cross-compilation target (e.g. "amd64").
  cgo         Enable CGO (default: false).
  tags        Build tags as array (e.g. ["netgo", "osusergo"]).
  ldflags     Linker flags string (e.g. "-s -w").
  gcflags     Compiler flags string.
  trimpath    Strip file paths from binary (default: true).
  race        Enable race detector (default: false).
  buildmode   Go build mode (default: "exe"). Options: exe, c-archive, c-shared.

EXAMPLES

  Minimal build:
    {"output": "myapp", "pkg": "./cmd/myapp"}

  Cross-compile for Linux:
    {"output": "myapp", "pkg": "./cmd/myapp", "goos": "linux", "goarch": "amd64"}

  Build with flags:
    {"output": "myapp", "pkg": ".", "ldflags": "-s -w -X main.version=1.0",
     "tags": ["netgo"], "cgo": false, "trimpath": true}

  Shared library:
    {"output": "libfoo", "pkg": ".", "buildmode": "c-shared", "cgo": true}

REQUIREMENTS

  The project must declare a `from: "scratch"` toolchain named "go" whose
  version satisfies go.mod (see `mu guide toolchains`):

    toolchains: [{
      toolchain: "go"
      from:      "scratch"
      config: {
        version:      "1.26.10"
        url:          "https://go.dev/dl/go1.26.10.darwin-arm64.tar.gz"
        sha256:       "<sha256 from https://go.dev/dl/?mode=json>"
        strip_prefix: "go"
      }
    }]

  No `go` on the host PATH is needed or used.

ACTIONS GENERATED

  build       One pure action naming the "go" toolchain, so it runs in mu's
              sandbox: bin/go is first on PATH and Go finds GOROOT from it.
              The sandbox's module cache starts empty, so with go.mod present
              the action has network: true and `go build` downloads its
              modules itself (verified against go.sum).

ENVIRONMENT

  The action declares only config-derived values in env: CGO_ENABLED,
  GOOS, GOARCH, plus GOTOOLCHAIN=local (never fetch another toolchain) and
  GOFLAGS=-modcacherw (keep the module cache removable). HOME is the
  sandbox's own empty directory, so GOCACHE, GOPATH and GOMODCACHE default
  under it and nothing from the host's Go setup is visible. Module-proxy
  settings are passed by name with env_passthrough, so their values never
  enter the plan or the cache key:

    GOPROXY GOPRIVATE GONOPROXY GONOSUMDB GOSUMDB GOINSECURE

  cgo builds need a C toolchain inside the sandbox; grant it with the
  action-level host_paths or use a scratch toolchain.

CAPABILITIES

  discover, plan
