# External Source Inputs

**Date:** 2026-04-25
**Status:** Brainstorm / design note
**Author:** chazu (with claude)
**Scope:** A primitive for letting one mu project consume artifacts from
another mu project that lives at a different URL — without hardcoded
filesystem paths in `mu.cue`.

---

## Motivating concrete

`~/dev/loosh/dev/infra/` is an infra/convergence project. It needs to
deploy `void-server` and `void-admin` binaries to a remote host. Those
binaries are built by `~/dev/go/loosh/` (a separate repo at
`git@github.com:loosh-industries/void.git`), which already has its own
`mu.cue` declaring `//cmd/void-server-linux-amd64` and
`//cmd/void-admin-linux-amd64` go-plugin targets.

Today there is no clean way for infra's `mu.cue` to say "build the
artifact `//cmd/void-server-linux-amd64` from `loosh-industries/void`
and give me the resulting file path." The actual workarounds are all
ugly:

1. Hardcode `/Users/chazu/dev/go/loosh/...` in `sources` — breaks on
   every other machine, requires the consumer to know where the
   producer happens to live.
2. `git clone` into a `.work/` dir under the consumer project and shell
   out to a recursive `mu build` — pollutes the consumer's tree, no
   real caching, fights every layer of mu's source-hash machinery.
3. Pre-build in the producer repo by hand and commit the binary
   somewhere — defeats the build system entirely.

This is the same problem Bazel solved with `git_repository` /
`http_archive`, Nix with flake inputs, Cargo with `[patch]`, and Go
with the module cache + `go.work`. The shape is well-known; the
question is how to express it in mu without spoiling mu's design.

## The design tension

Git is just one kind of source. Plausible others:

- **HTTP archive** (tarball at a URL, optionally with a sha256)
- **OCI artifact** (pull a layer from a registry — relevant given
  loosh's zot work)
- **Another mu project on disk** (sibling repo, monorepo subdir)
- **A specific build output of another mu project** (the artifact, not
  the sources)
- **CAS reference** (content-addressed blob from mu's own cache)

If the new primitive is "git repos," we'll grow N more primitives next
year. If it's "external sources" generically, we have to figure out
the abstraction now.

The clean shape is probably: **sources are a plugin capability.**
Existing capabilities are `discover|plan|observe|resolve_secret`. Add
`fetch_source` (or similar). A `git` plugin, an `http` plugin, an
`oci` plugin, etc., each implement it. The coordinator hands the
plugin a logical reference (`{kind: "git", url, ref}` or
`{kind: "http", url, sha256}`), the plugin returns a path on disk
(into a managed cache) plus a content hash for action-key purposes.

That keeps mu core ignorant of git, http, oci — same way it's ignorant
of go, terraform, k8s today.

## Proposed surface

### `sources` block in `mu.cue`

```cue
sources: "loosh-industries/void": {
    kind: "git"
    url:  "git@github.com:loosh-industries/void.git"
    ref:  "main"          // or a pinned sha for hermeticity
}
```

The key (`loosh-industries/void`) is a logical name local to this
project. `kind` selects which plugin resolves it. The rest of the
fields are plugin-specific config (validated via the same JSON-Schema
mechanism as target `config`).

### Targets reference sources symbolically

```cue
targets: [{
    target: "//void/server-binary"
    toolchain: "mu-subproject"   // new built-in or plugin
    source: "loosh-industries/void"
    build:  "//cmd/void-server-linux-amd64"
}]
```

The `mu-subproject` toolchain resolves the source to a path, runs
`mu build <build>` inside it, and exposes the produced artifact as the
output of *this* target. Action-key inputs include the source's
content hash, so caching works end-to-end.

(Open question: should `mu-subproject` be built-in or a plugin? It's
the one piece that knows about mu itself, so built-in feels right.
Plugins shouldn't have to know mu's CLI.)

### Local overrides

A `mu-overrides.cue` file, resolved in this order:

1. `$PWD/mu-overrides.cue` (repo-level — checked in only if intended
   for everyone, otherwise gitignored).
2. `$HOME/.config/mu/mu-overrides.cue` (user-level — the personal
   "I have this checked out at /Users/me/dev/...").
3. None — fall through to fetched cache.

Example:

```cue
// ~/.config/mu/mu-overrides.cue
package mu

source_overrides: "loosh-industries/void": {
    path: "/Users/chazu/dev/go/loosh"
}
```

When an override is present, the plugin is skipped — mu treats the
overridden path as a live source tree. Its file contents feed the
action-key normally, so editing files in the override path invalidates
caches just like editing files in the consumer project does.

### Cache layout

`$XDG_CACHE_HOME/mu/sources/<kind>/<content-hash>/` — content-addressed
so two projects pinning the same git sha share a single checkout, and
mu can `mu clean` source caches the same way it cleans the action CAS.

## Resolution algorithm (sketch)

```
resolve(name):
    if override(name) exists:
        return LocalPath(override(name).path, hash=hash_tree(path))
    spec = sources[name]
    plugin = plugin_for_kind(spec.kind)
    return plugin.fetch_source(spec)
        // returns {path, content_hash}
```

The `content_hash` is the action-key contribution. For git with a
pinned sha, it's the sha. For git with a floating ref, it's the
resolved sha after fetch. For http with sha256, it's the sha256. For
overrides, it's a tree hash of the local path (expensive but correct).

## Open questions

- **Floating refs vs. pinned shas.** Should `ref: "main"` be allowed
  at all, or should we require pinned shas (with a `mu source
  update` command to bump them)? Pinned is hermetic; floating is
  ergonomic. Probably allow both, warn on floating.
- **Lockfiles.** Cargo/Nix/Go all have lockfiles. Does mu need a
  `mu.lock`? Probably yes once floating refs exist, but the lockfile
  format wants thought — CUE? JSON? A separate `mu-lock.cue`?
- **Cross-project plugin resolution.** If a sub-project's `mu.cue`
  references plugins by relative path (`plugins/foo/plugin.bb`), those
  paths resolve relative to the sub-project, not the consumer.
  Should Just Work as long as `mu-subproject` invokes `mu build` with
  cwd set to the source path.
- **Artifact vs. source consumption.** Sometimes you want the source
  tree (e.g., to run linters across a vendored input). Sometimes you
  want only the built artifact (the binary). Two different toolchains
  (`mu-subproject-source` vs `mu-subproject-artifact`)? Or one
  toolchain with a `mode` field?
- **Garbage collection.** Source caches grow unbounded. `mu clean
  --sources` is straightforward; auto-eviction (LRU? age-based?) is
  the harder question, deferred.
- **Authentication.** SSH-based git URLs need agent access; HTTPS may
  need credential helpers; OCI needs registry auth. Plugins handle
  this themselves, but we should document the contract — probably
  "plugins inherit the user's normal credential environment, mu does
  not vault credentials for sources."
- **Determinism with floating refs + override.** If user A has an
  override and user B fetches `main`, they get different inputs and
  different action-keys. That's correct behavior but worth flagging
  loudly in errors when shared CI caches diverge.

## Why not just shell out to git?

That's option 2 above. It works, but every consumer project reinvents
the same workaround, with subtle differences (clone-and-leave vs.
tmpdir-and-cleanup, fetch-and-reset vs. always-clone, where to put
the artifacts, how to invalidate). The whole point of a build system
is to give that machinery one good implementation. If mu doesn't,
every `mu.cue` that needs cross-project artifacts will grow its own.

## Why not just `go.work`-style workspaces?

A workspace assumes all projects are siblings on the same machine.
That works for a developer's laptop and breaks for CI, for new
contributors who haven't cloned everything, and for anyone trying to
build from a release tag. Workspaces are an *override* mechanism, not
a *source* mechanism — they're the local-development ergonomics
half of this design, not the whole thing. The `mu-overrides.cue`
file plays exactly the workspace role here, layered on top of a real
source-fetching primitive.

## Smallest viable shipping increment

If we want to validate the design before committing to plugin-API
changes, ship in this order:

1. Built-in `mu-subproject` toolchain that takes a literal `path`
   field (no fetching). This is just "run another mu project's build
   and consume an artifact" — useful on its own for monorepos.
2. `mu-overrides.cue` resolution. Adds the override path layered on
   top of `path:` configs.
3. `sources` block + `git` plugin. Fetching becomes possible. The
   override file now overrides `sources` entries by name.
4. Lockfile, `mu source update`, additional source kinds (http, oci).

Steps 1–2 are useful to monorepo users without solving the cross-repo
case. Step 3 is the actual feature. Step 4 is generalization.

---

## Out of scope for this note

- Concrete plugin API for `fetch_source` (response schema, timeout
  bucket, error taxonomy).
- Whether sub-project builds should run in a sandbox isolated from
  the parent build's sandbox. (Probably yes; defer.)
- Interaction with `mu observe` — should observing a `//void/server-
  binary` target observe the producer or the consumer? (Probably
  the consumer; the producer is reachable via `mu --cd <path> observe`.)
