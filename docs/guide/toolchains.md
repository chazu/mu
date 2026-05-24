mu guide toolchains — bootstrapping toolchains from scratch

mu can download, verify, and bootstrap toolchains (compilers, runtimes)
from scratch, ensuring hermetic builds with known-good tool versions.

DECLARING A TOOLCHAIN

  In mu.cue:

  {
    "toolchains": [
      {
        "toolchain": "go",
        "from": "scratch",
        "config": {
          "version": "1.25.8",
          "url": "https://go.dev/dl/go1.25.8.darwin-arm64.tar.gz",
          "sha256": "abc123...",
          "strip_prefix": "go"
        }
      }
    ]
  }

CONFIG FIELDS

  toolchain       Name (must match plugin names that use this toolchain).
  from            Currently only "scratch" is supported.
  config.version  Version string (for cache key and display).
  config.url      Download URL for the toolchain archive.
  config.sha256   Expected SHA-256 hash of the download.
  config.strip_prefix  Directory prefix to strip from the archive (optional).

BUILD PROCESS

  mu scratch

  1. Check CAS for an existing toolchain manifest (cache hit → skip).
  2. Download archive and verify SHA-256.
  3. Extract (supports tar.gz, tar.xz, zip, and raw binaries).
  4. Verify the binary runs: <name> version or <name> --version.
  5. Walk extracted files, store each in CAS.
  6. Create a ToolchainManifest with an artifact map.
  7. Register in the ToolchainRegistry.

  'mu build' automatically runs scratch builds before planning if
  toolchains are defined.

IMPLICIT CONTRACTS

  The "scratch" bootstrap path enforces conventions, not a build recipe.
  An archive only works if it satisfies all of:

  1. Archive type is .tar.gz, .tgz, .tar.xz, .txz, or .zip (else treated
     as a raw binary).
  2. After extract (with strip_prefix applied), a binary exists at either
     <root>/bin/<toolchain> or <root>/<toolchain>. The basename must match
     the "toolchain" field exactly.
  3. That binary responds with exit 0 to one of: `<bin> version` (Go-style)
     or `<bin> --version` (GNU-style).
  4. Distribution is pre-compiled. There is no post-extract build step —
     ./configure, make, cargo build, etc. are not invoked.

FAILURE MODES

  SHA-256 mismatch
    ForgeFetch aborts. Fix the sha256 in mu.cue.

  Unknown archive extension
    Falls back to extractRawBinary: copies the file to
    <extractDir>/bin/<dirname-without-version-suffix> and chmods +x.
    Useful for single-file releases (e.g. jq).

  Binary not found at conventional path
    verify() errors. Workarounds: rename the toolchain to match the
    binary's name; provide a shim plugin that bypasses scratch; or use
    MU_SCRATCH to delegate.

  Binary rejects both version flags
    Same verify error. Same workarounds.

  Source distribution (no precompiled binary)
    Not supported by the built-in scratch flow. Either pre-build
    externally and host a binary tarball, or set MU_SCRATCH=<command>
    to delegate the whole download/extract/verify pipeline to a builder
    that knows how to compile (Nix, custom script, etc.).

  Toolchain name != binary name
    Verify fails. Use MU_SCRATCH or write a wrapper plugin.

USING TOOLCHAINS IN PLUGINS

  When mu plans a target, it passes toolchain_artifacts to the plugin:

    {"method": "plan", "toolchain_artifacts": {"go": "/path/to/go"}, ...}

  Plugins use these paths instead of system-installed tools.

EXTERNAL SCRATCH BUILDS

  Set MU_SCRATCH=<command> to delegate toolchain bootstrapping to an
  external process (e.g. a Nix-based builder).

COMMANDS

  mu scratch              Build all declared toolchains.
  mu scratch --no-cache   Force re-download and rebuild.
  mu cache ls --toolchains   List cached toolchains.
  mu cache inspect <name>    Inspect a cached toolchain.
