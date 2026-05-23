mu guide plugin scratch — toolchain downloader and registrar

(Implemented in Go using sdk/muplugin; the original Babashka version
 at plugin.bb remains in-tree for one release cycle. See 'mu guide sdk'.)

Downloads, verifies, extracts, and registers toolchains from source
archives. This is the plugin implementation of 'mu scratch'.

USAGE IN mu.json

  Typically used via the toolchains array rather than as a direct target:

    {
      "toolchains": [
        {
          "toolchain": "go",
          "from": "scratch",
          "config": {
            "version": "1.25.8",
            "url": "https://go.dev/dl/go1.25.8.darwin-arm64.tar.gz",
            "sha256": "abc123..."
          }
        }
      ]
    }

  Can also be used as a direct target:

    {
      "target": "//toolchains/bb",
      "toolchain": "scratch",
      "sources": [],
      "config": {
        "url": "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-macos-aarch64.tar.gz",
        "sha256": "91499b3f...",
        "version": "1.12.216"
      }
    }

CONFIG FIELDS

  url              Download URL for the toolchain archive (required).
  sha256           Expected SHA-256 hash of the download (required).
  version          Version string for cache key and display (required).
  strip_prefix     Strip this top-level directory from the archive (optional).
                   e.g. "go" strips the go/ prefix from Go tarballs.

WORKFLOW

  1. fetch      Downloads the archive and verifies SHA-256 checksum.
  2. extract    Unpacks the archive (tar.gz, tar.xz, zip, or raw binary).
  3. verify     Runs '<name> --version' to confirm the binary works.
  4. register   Generates manifest.json with artifact metadata.

  Supports tar.gz, .tgz, .zip, and single binary downloads.

CAPABILITIES

  discover, plan
