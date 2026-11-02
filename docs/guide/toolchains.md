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
    The download aborts without retrying. Fix the sha256 in mu.cue.

  Download fails
    Network errors and HTTP 5xx/429 are retried 3 times with backoff;
    any other non-200 status (404, 403, ...) fails at once with the
    status in the error. A download larger than 4 GiB is rejected
    (raise with MU_ARCHIVE_MAX_BYTES, see EXTRACTION LIMITS). Errors
    show the URL without its query string or credentials.

  Unknown archive extension
    Falls back to extractRawBinary: copies the file to
    <extractDir>/bin/<toolchain> and chmods +x. Useful for single-file
    releases (e.g. jq).

  Binary not found at conventional path
    verify() errors. Workarounds: rename the toolchain to match the
    binary's name, or provide a shim plugin that bypasses scratch.

  Binary rejects both version flags
    Same verify error. Same workarounds.

  Source distribution (no precompiled binary)
    Not supported by the scratch flow. Source-only tools: build them in a
    normal target and consume the output (or pre-build externally and
    host a binary tarball).

  Toolchain name != binary name
    Verify fails. Rename the toolchain or write a wrapper plugin.

  Archive exceeds extraction limits
    Extraction stops with "archive exceeds extraction limit" and the
    partial tree is removed. See EXTRACTION LIMITS.

EXTRACTION LIMITS

  Every archive mu expands (scratch toolchains, catalog source archives,
  plugin CAS bundles) goes through one extractor, bounded by entry count,
  per-file expanded bytes, and total expanded bytes. Limits apply to bytes
  actually written, not only to sizes the archive declares, and skipped
  entries still count.

                    entries   per file   total
    toolchains      500,000      4 GiB   16 GiB
    plugins         100,000      1 GiB    2 GiB

  Toolchain defaults leave about 6x headroom over large real toolchains
  (Rust with docs: ~76k entries / 2.3 GB; LLVM: several GB; Go and Zig:
  ~17-19k entries). If a legitimate archive is larger, raise the byte
  limit for that run; it replaces both the per-file and the total limit
  of every profile (a positive integer):

    MU_ARCHIVE_MAX_BYTES=34359738368

  A malformed value is an error, not an ignored limit. Entry counts have
  no override.

EXTRACTION SAFETY

  All writes go through a directory handle rooted at the extraction
  directory, so no entry can create or modify anything outside it.

  - An absolute entry, or one whose path escapes the directory, fails the
    extraction (it is not silently skipped).
  - Hard links, device files, and other special entries are rejected.
  - Toolchain archives may contain symlinks with relative targets that
    stay inside the tree; absolute or escaping targets are rejected, and
    so is a link that resolves outside the tree through another link.
    Catalog and plugin bundles may not contain symlinks at all.
  - Symlinks are not stored in the toolchain manifest (it holds regular
    files only) and are never read through; mu scratch prints a warning
    listing them. A toolchain whose bin/<toolchain> is a symlink therefore
    passes verify but is unusable; repackage it with a regular file.

USING TOOLCHAINS IN PLUGINS

  When mu plans a target, it passes toolchain_artifacts to the plugin:

    {"method": "plan", "toolchain_artifacts": {"go": "/path/to/go"}, ...}

  Plugins use these paths instead of system-installed tools.

COMMANDS

  mu scratch              Build all declared toolchains.
  mu scratch --no-cache   Force re-download and rebuild.
  mu cache ls --toolchains   List cached toolchains.
  mu cache inspect <name>    Inspect a cached toolchain.

LOCAL RECOVERY

  A toolchain result-cache miss or outage can be rebuilt from a downloaded
  archive whose SHA-256 still matches the configuration. The archive is checked
  before contacting its URL. An extracted tree alone is not sufficient evidence
  of that archive identity. --offline fails immediately if no verified archive
  or usable cached manifest/artifacts exist.

  Extracted runtime binaries are verified and published atomically under their
  full artifact digest. Matching legacy name-only binaries can be migrated to
  this path without a registry fetch; mismatched versions are rejected.
