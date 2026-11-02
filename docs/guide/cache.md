mu guide cache — content-addressed storage and action caching

mu stores all build artifacts in a local content-addressed store (CAS).
The default location is ~/.mu/cache/ (cache.path). One OCI registry may be
configured as a read-only remote cache (cache.remote); `mu cache push`
publishes local results to a registry (cache.push).

HOW CACHING WORKS

  Each build action has a cache key (sha256) computed from:
  - Command (argv, original order)
  - Inline pith body or ewe program digest, when present
  - Sorted input digests (sha256 of each input file). Inputs produced by
    another action use the digest of what that action actually produced,
    filled in after it runs and before this action's cache lookup.
  - Declared environment variables (sorted); pure defaults are clean, while
    impure bare commands may inherit
  - Sorted env_passthrough names (never their host values)
  - Sorted toolchain artifact paths and digests, including bare vs sandbox mode
  - Sorted declared output paths and sandbox source paths
  - Network flag
  - Impure flag
  - Work directory relative to the project root (slash-separated). The
    project root itself is not hashed, so the same commit checked out at
    different paths, or on different machines, shares cache entries.
  - Sealed-input refs and modes (NOT values)
  - Sealed-output destination refs and effective store modes

  Action-key encoding v3 records the clean-environment and rooted-execution
  contract. v4 hashes the project-relative work directory instead of the
  absolute work directory and project root. Each upgrade invalidates previous
  action results once; content-addressed blobs remain reusable.

  Key encoding is versioned and length-prefixed, so embedded newlines or
  separators in arguments and metadata cannot alias another action. This
  encoding replaces legacy keys: existing action results miss once and
  rebuild; their content-addressed blobs remain reusable.

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

  On cache hit: output blobs are staged beside their destinations, verified
  against their SHA-256 digests, and restored with the recorded permission
  bits. Relative paths resolve against the action's work directory. Every
  output is staged before any is replaced; each rename is atomic, but the
  set of renames is not a filesystem transaction. Failed reads, closes, or
  digest checks leave existing files intact. Symlink destinations are replaced
  rather than followed. POSIX permission bits (0777) are preserved; special
  mode bits, ownership, ACLs, and extended attributes are not restored.

  New execution receipts use version 2 with output_modes. Legacy receipts
  lack original modes, so actions with outputs rebuild once to refresh their
  receipts. The OCI envelope and output-digest shape remain compatible.
  On cache miss: action runs, outputs are hashed and stored in CAS.

  Builds write only to the local store. A local miss falls back to the
  remote cache, if one is configured (see REMOTE CACHE).

LOCAL LAYOUT

  ~/.mu/cache/
    blobs/sha256/<hash>            Content-addressed blobs
    ac/<action-key-hash>           One action result per file
    ingest/                        In-flight temporary files

  An action result file holds the receipt JSON:
    {"version":2, "output_modes":{"name":493}, "outputs": {"name": {"Algorithm":"sha256","Hash":"..."}}, "exit_code": 0}

  Every blob and result is written to a temporary file under ingest/,
  fsynced, and renamed into place. A blob is named by the SHA-256 of the
  bytes actually written. There is no shared index, so any number of mu
  processes (parallel builds, editor-triggered builds, CI jobs on one
  runner) can share ~/.mu/cache without losing each other's results, and a
  crash never leaves a partial blob or result visible.

  Caches written by releases before this layout kept action results in an
  OCI index.json. The first mu command that opens the cache converts those
  entries to ac/ files and renames index.json to index.json.migrated.
  Entries that cannot be read are treated as cache misses and rebuild.

REMOTE LAYOUT

  On an OCI registry, action results are OCI manifests tagged
  action-sha256-<action-key-hash>, with:
  - Config blob: the receipt JSON above
  - Layer blobs: the actual output file contents, sorted by output name so
    repeated publication of identical results keeps the same manifest digest

INSPECTING THE CACHE

  mu cache ls                      List cached action results.
  mu cache ls --toolchains         List cached toolchains.
  mu cache ls --json               Output as JSON.
  mu cache inspect <ref>           Inspect an action, toolchain, or blob by tag/digest.
  mu cache size                    Show total cache disk usage.
  mu cache size --json             Output as JSON.
  mu cache clean                   Remove unreachable blobs (garbage collection)
                                   and stale staging (see below).
  mu cache clean --dry-run         Report what would be removed.

STAGING AND CRASH CLEANUP

  mu writes temporary files next to their destination and publishes them
  atomically: scratch extraction (~/.mu/cache/extract/.extract-*),
  downloads (.fetch-*), cache writes (~/.mu/cache/ingest), plugin bundles (.bundle-*, .plugin-*),
  toolchain binaries (.tool-*), schema installs (.tmp-*), and, in the
  system temp directory, registry upload spools, sandbox roots, and
  sealed-value directories (mu-*). A writer removes its own partial output
  on any failure, including a full disk.

  A process that is killed cannot clean up, so mu sweeps these locations at
  most once an hour when a command starts (and on every 'mu cache clean').
  A leftover is removed only when nothing inside it has been modified for
  24 hours, so a concurrent mu process is never disturbed. Stranded
  sealed-value directories can hold secret material; the sweep reclaims
  them too. Set MU_NO_STAGING_SWEEP=1 to keep a crashed run's leftovers for
  inspection.

  mu does not check free disk space before extracting: a compressed
  stream's expanded size is unknown until read, and free-space checks race
  with other writers. Extraction limits (mu guide toolchains) bound the
  worst case instead.

VERIFYING CACHE INTEGRITY

  mu verify                        Re-hash all blobs, report corruption.
  mu verify --json                 Output as JSON.
  mu verify --fix                  Delete corrupt blobs.

REMOTE CACHE

  A remote cache shares built results across machines. It is one OCI
  repository, read-only to builds:

    cache: {
      path:   "~/.mu/cache"                      // optional; the default
      remote: "registry.example.com/acme/mu-cache"
      push:   {registry: "registry.example.com", repository: "acme/mu-cache"}
    }

  remote is a full repository reference ("host/namespace/repo", no scheme).
  On a local miss, mu looks the action up in the remote. A remote result is
  used only if every output blob it names downloads and hashes to its
  digest; the blobs and the result are then written into the local store and
  served from there, so each entry crosses the network once. Content that
  does not match its digest is rejected, never stored, and the action is
  rebuilt locally. A blob read (mu fetching an input or artifact by digest)
  falls back to the remote the same way.

  Builds never write to the remote. Results reach it only through
  `mu cache push`.

  The remote is optional. The first remote failure (unreachable, timeout,
  auth error) prints one warning and switches the rest of that command to
  local-only; the build continues, rebuilding whatever it cannot find
  locally. The next command tries the remote again. Warm local results never
  contact the remote. --offline skips the remote entirely.

  Earlier releases configured a list under cache.backends, with read_repair
  and write_through flags. Those fields are rejected at load with a hint:
  replace the disk backend with cache.path (or drop it to use the default)
  and the oci backend with cache.remote.

PUSHING

  cache.push names the registry host and repository `mu cache push` writes
  to. Both fields are required for push.

    mu cache push                  Copy cached action results and their output
                                   blobs from the local CAS to cache.push's
                                   registry/repository.
    mu cache push --dry-run        List the action tags that would be pushed.

  The OCI manifest and tag for each local result are built at push time;
  output blobs the registry already has are not uploaded again.
  Tags are derived (action-*); only the registry and repository are
  configurable. To read what you push, set cache.remote to
  "<push.registry>/<push.repository>".

AUTHENTICATION

  mu cache login [host]            Log in to an OCI registry and store the
                                   credential. Host defaults to cache.push.registry.
  mu cache login [host] --username <u> --password-stdin
  mu cache logout [host]           Remove the stored credential.

  Flags: --username, --password (prefer --password-stdin), --password-stdin.
  On a TTY, username/password are prompted interactively when omitted.

  Credential storage is asymmetric:
    Writes (login, cache push, plugin push)
      Stored in ~/.mu/credentials.json (Docker-format JSON, plaintext).
      mu deliberately avoids the Docker credential chain for writes so it
      never needs a credential-helper binary (e.g. docker-credential-desktop).
      The Docker chain (~/.docker/config.json) is consulted as a read-only
      fallback, so existing `docker login` / `oras login` sessions still work.
    Reads (cache.remote)
      Resolve credentials ONLY through the Docker chain (~/.docker/config.json);
      ~/.mu/credentials.json is not consulted. Access is anonymous if none
      is found. localhost / 127.0.0.1 registries use plain HTTP.

  Because of this asymmetry, `mu cache login` covers push but not pull: to
  authenticate a private remote cache you also need `docker login`.

PLUGIN STORAGE

  Plugin scripts are hashed and stored in CAS. When loaded by script path,
  the script is hashed on startup. When loaded by digest, it's fetched from
  CAS directly. Built plugin bundles are extracted to ~/.mu/plugins/<name>/.
  Directory bundles use bundle-<full-sha256> and are published only after
  successful extraction. Interrupted extraction leaves no reusable bundle.
  Older bundles are retained for running processes. Plugin push selects the
  build's resolved digest rather than combining cached versions.

REMOTE METADATA BUDGETS

  OCI manifests and action-result configs are limited to 8 MiB each. Plugin
  configs and plugin indexes are limited to 4 MiB each. These budgets are
  independent of large artifact layer blobs. A representative 20,000-output
  action config fits within its budget.

  Declared oversized descriptors fail before fetch. Actual transfer bytes,
  including trailing whitespace, are also bounded before JSON decoding, so
  inaccurate sizes cannot bypass the limit. Errors identify the metadata kind
  and byte budget; split oversized output sets or plugin indexes into smaller
  artifacts. Native code rejects trailing JSON and closes streams on failure.

REGISTRY OUTAGES AND LOCAL OPERATION

  The remote cache is optional (see REMOTE CACHE). Remote action-result
  lookups have a two-second budget covering auth and metadata reads. The
  first failure disables the remote for the rest of the command with one
  warning; the next command retries it. Warm local results need no registry
  request. Cancelling the command does not count as a remote failure.

  Registry and Mu-managed download clients bound connect/TLS setup to three
  seconds, response-header waits to five seconds, and read/write inactivity
  to thirty seconds. Large uploads/downloads can continue while making progress;
  there is no short whole-transfer timeout. HTTP/1.1 keeps progress deadlines
  attributable to each transfer. Explicit push/publish/login/discovery reports
  failed or incomplete remote work rather than claiming success.

  To bypass registry probes and dependency downloads immediately:

    mu build --offline //target
    mu observe --offline //target
    mu scratch --offline

  --offline uses local/cached CUE modules and source/toolchain artifacts. Missing
  required data fails with its requested identity; another plugin/toolchain
  version is never substituted. Network performed by plugins and actions
  remains their own contract.
  --offline is rejected for registry publication, login, and remote discovery.
  Local inspection and cache-push dry-run remain available.

  --no-cache forces action execution while retaining CAS storage needed by
  plugins and toolchains. It is not an offline switch; combine with --offline
  to rebuild without registry access.
