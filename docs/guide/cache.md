mu guide cache — content-addressed storage and action caching

mu stores all build artifacts in a content-addressed store (CAS) using
OCI image layout. The default location is ~/.mu/cache/.

HOW CACHING WORKS

  Each build action has a cache key (sha256) computed from:
  - Command (argv, original order)
  - Sorted input digests (sha256 of each input file)
  - Environment variables (sorted)
  - Network flag
  - Impure flag
  - Work directory (if non-default)
  - Sealed-input refs and modes (NOT values)
  - Sealed-output destination refs

  Cache key INCLUDES (non-secret metadata):
    sealed_input refs       Changing pass:foo/v1 → pass:foo/v2 invalidates.
    sealed_input modes      env vs file is observable (path vs literal).
    sealed_output refs      Changing the destination invalidates.

  Cache key EXCLUDES (secret data):
    sealed_input values     The resolved bytes never enter the key.
    sealed_output values    No value exists at key time anyway.

  This means: two builds with the same ref but different resolved
  values still cache-hit (if the underlying secret rotated, the
  action does not re-run; the consumer sees whatever value was
  resolved at exec time).

  Impure actions skip the cache entirely. Actions with non-empty
  sealed_outputs are forced impure — caching would skip the
  store_secret side-effect.

  On cache hit: outputs are restored from CAS without re-execution.
  On cache miss: action runs, outputs are hashed and stored in CAS.

OCI LAYOUT

  ~/.mu/cache/
    index.json                     OCI index (manifest registry)
    blobs/sha256/<hash>            Content-addressed blobs

  Action results are stored as OCI manifests with:
  - Config blob: {"outputs": {"name": {"Algorithm":"sha256","Hash":"..."}}, "exit_code": 0}
  - Layer blobs: the actual output file contents

INSPECTING THE CACHE

  mu cache ls                      List cached action results.
  mu cache ls --toolchains         List cached toolchains.
  mu cache ls --json               Output as JSON.
  mu cache inspect <ref>           Inspect an action, toolchain, or blob by tag/digest.
  mu cache size                    Show total cache disk usage.
  mu cache size --json             Output as JSON.

VERIFYING CACHE INTEGRITY

  mu verify                        Re-hash all blobs, report corruption.
  mu verify --json                 Output as JSON.
  mu verify --fix                  Delete corrupt blobs.

PLUGIN STORAGE

  Plugin scripts are hashed and stored in CAS. When loaded by script path,
  the script is hashed on startup. When loaded by digest, it's fetched from
  CAS directly. Built plugin bundles are extracted to ~/.mu/plugins/<name>/.
