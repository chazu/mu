mu guide plugin zig — Zig toolchain plugin

Builds Zig projects using either 'zig build' (build.zig) or direct
compilation ('zig build-exe', 'zig build-lib').

USAGE IN mu.json

  {
    "target": "//cmd/myapp",
    "toolchain": "zig",
    "sources": ["src/*.zig", "build.zig"],
    "config": {
      "output": "myapp",
      "mode": "build",
      "optimize": "ReleaseSafe"
    }
  }

CONFIG FIELDS

  output      Output binary/library name (default: target short name).
  mode        Build mode (default: "build"):
              - "build": uses zig build with build.zig
              - "compile": uses zig build-exe/lib/obj directly
  target      Cross-compile target triple (e.g. "x86_64-linux-gnu").
  optimize    Optimization level (default: "Debug"):
              Debug, ReleaseSafe, ReleaseFast, ReleaseSmall.
  step        Zig build step to run (default: "install", build mode only).
  build_file  Path to build.zig (default: "build.zig").
  emit        Output type for compile mode (default: "exe"):
              exe, lib, obj.
  flags       Extra flags as array (e.g. ["-fno-llvm"]).
  pkg         Package path for compile mode (default: first source file).

EXAMPLES

  Build with build.zig:
    {"output": "myapp", "mode": "build", "optimize": "ReleaseFast"}

  Direct compilation:
    {"output": "myapp", "mode": "compile", "emit": "exe",
     "pkg": "src/main.zig", "optimize": "ReleaseSafe"}

  Cross-compile:
    {"output": "myapp", "mode": "build",
     "target": "x86_64-linux-gnu", "optimize": "ReleaseSmall"}

  Build shared library:
    {"output": "libfoo", "mode": "compile", "emit": "lib"}

ACTIONS GENERATED

  zig-build     Runs 'zig build' or 'zig build-exe/lib/obj' depending on mode.
                For build mode, outputs are expected in zig-out/bin/.

CAPABILITIES

  discover, plan
