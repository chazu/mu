mu guide plugin go — Go toolchain plugin

Compiles Go packages into executables, archives, or shared libraries.

USAGE IN mu.json

  {
    "target": "//cmd/myapp",
    "toolchain": "go",
    "sources": ["go.mod", "go.sum", "cmd/myapp/*.go", "internal/**/*.go"],
    "config": {
      "output": "myapp",
      "pkg": "./cmd/myapp",
      "trimpath": true
    }
  }

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

ACTIONS GENERATED

  go-mod-download   Fetches module dependencies (when go.mod present).
                    Runs with network access.
  go-build          Compiles the package. Depends on go-mod-download.

CAPABILITIES

  discover, plan
