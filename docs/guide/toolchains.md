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
