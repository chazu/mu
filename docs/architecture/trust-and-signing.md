# Trust and Signing

**Date:** 2026-05-24
**Status:** Design — not yet implemented

## Goal

Give mu a first-class signing/verification layer with sigstore as the
default well-supported backend, while keeping the door open for users
who can't (or won't) depend on sigstore's public infrastructure.

Two properties we want simultaneously:

1. **Sigstore "just works."** A user who writes `trust: "auto"` in
   `mu.cue` gets keyless signing, transparency-log inclusion, and
   identity-pinned verification with zero further configuration.
2. **No lock-in.** The same code paths drive a self-hosted Fulcio +
   Rekor, a plain x509 keypair, or a custom in-toto attestation
   backend. Sigstore is one implementation behind an interface, not a
   hard dependency of the design.

This document specifies the interfaces, configuration surface, default
behaviors, and integration points. It does not specify the code layout
beyond what is necessary to make the contract clear.

## Non-Goals

- Replacing content-addressed hashing. Hashes prove integrity; signatures
  prove provenance. Both are kept.
- Signing the mu binary itself. That is a release-engineering concern,
  not a build-coordinator concern.
- Key management UX. mu consumes credentials; it does not generate, rotate,
  or store long-lived signing keys. KMS / OIDC / file-based key sources
  are handled outside mu.

## Conceptual Model

### What is signed

Every action result. An action result manifest already exists in the
OCI cache as `action-<inputHash>`; it records the input digests, output
digests, and metadata for one hermetic transformation. The signature
attaches to that manifest.

Because plugin binaries, toolchain artifacts, and user build outputs
are *all* products of actions, signing action results signs everything
mu produces uniformly. There is no separate "publish" step and no
distinction between plugin signatures and artifact signatures.

Toolchain *downloads* (external materials fetched by scratch plugins)
are signed separately when a `vendor` claim is configured, because
they did not originate from a mu action. Downloads are still pinned by
hash; the signature is an additional vendor-identity claim, not a
replacement for the hash pin.

### Where signatures live

Signatures are stored as OCI 1.1 referrer artifacts pointing at the
action-result manifest digest. This:

- Travels with the blob automatically through `oci push` / `oci pull`.
- Costs nothing for users who never enable signing (no referrer = no
  signature, verification skipped).
- Works identically against local OCI layout and remote registries.
- Lets `mu verify` discover signatures without a side-channel index.

The bundle format is the sigstore v0.3 protobuf bundle even for
non-sigstore backends. The bundle is a flexible envelope (signature +
certificate chain or public key + optional log entries); reusing it
keeps one parser path. Backends that don't produce log entries simply
omit that field.

### When signing happens

By default, every action completion signs its result. There are three
modes:

| Mode      | Behavior                                              |
|-----------|-------------------------------------------------------|
| `all`     | Sign every action result as it completes.             |
| `leaves`  | Sign only outputs that are not consumed by another    |
|           | action in the same build (i.e., the "exported"        |
|           | artifacts).                                            |
| `none`    | Do not sign.                                          |

`leaves` exists for builds where per-action signing latency is
prohibitive (e.g., keyless sigstore at 10k+ actions, where each
signature is a Fulcio + Rekor round-trip). Most users should use
`all`; `leaves` is an escape hatch.

### When verification happens

| Mode          | Behavior                                                |
|---------------|---------------------------------------------------------|
| `all`         | Verify every blob loaded from cache, local or remote.   |
| `remote-only` | Verify blobs pulled from a remote cache; trust local    |
|               | builds without verification.                            |
| `none`        | Never verify.                                           |

`remote-only` is the recommended default. Local builds produced
signatures one instruction ago; re-verifying them in the same process
buys nothing. Remote-cache pulls (CI artifact sharing, team caches)
are the boundary where trust actually matters.

## The Signer / Verifier Interface

```go
// internal/sign

type Bundle = sigstore_bundle.Bundle // sigstore v0.3 protobuf

type Signer interface {
    Sign(ctx context.Context, target v1.Hash, payload []byte) (*Bundle, error)
}

type Verifier interface {
    Verify(ctx context.Context, target v1.Hash, bundle *Bundle) (Identity, error)
}

type Identity struct {
    Subject string         // e.g. SAN URI for sigstore, CN for x509
    Issuer  string         // OIDC issuer or CA distinguished name
    Claims  map[string]any // backend-specific extras
}

type Policy interface {
    Check(Identity) error
}

type Backend interface {
    Signer
    Verifier
    Name() string
}
```

A backend implements both halves; configuration may disable one side
(e.g., a verify-only consumer of a remote cache holds no signing
material).

### Built-in Backends

All ship in tree. Selection by `trust.backend` string.

| Name              | Description                                             |
|-------------------|---------------------------------------------------------|
| `none`            | No-op. Sign returns empty bundle; Verify accepts all.   |
| `sigstore`        | Sigstore unified. Public-good if URLs omitted; self-    |
|                   | hosted Fulcio/Rekor/TUF when URLs provided.             |
| `x509`            | Plain x509 keypair. Sign with private key file or KMS   |
|                   | URI; verify against configured CA bundle. No log.       |
| `in-toto`         | In-toto attestation envelope. Key source pluggable.     |

`sigstore` is one backend, not two. Public vs private is a matter of
which URLs you provide, not which backend you select. This avoids the
question "is `sigstore-public` a different code path from
`sigstore-private`?" — it is not.

## Configuration Surface

All configuration lives under a top-level `trust:` block in `mu.cue`.

### The `auto` shorthand

```cue
trust: "auto"
```

Resolves at config-load time based on environment:

| Environment                                | Resolution                                       |
|--------------------------------------------|--------------------------------------------------|
| `GITHUB_ACTIONS=true`                      | `sigstore` backend, public-good, identity pinned |
|                                            | to `$GITHUB_REPOSITORY` workflow OIDC subject.   |
| `GITLAB_CI=true`                           | `sigstore` backend, public-good, identity pinned |
|                                            | to GitLab CI OIDC subject for `$CI_PROJECT_PATH`.|
| `BUILDKITE_BUILD_ID` set                   | `sigstore` backend, public-good, identity pinned |
|                                            | to Buildkite OIDC for `$BUILDKITE_PIPELINE_SLUG`.|
| TTY attached, no CI env vars               | `none`. Local dev. Hash integrity only.          |
| CI env detected but no OIDC available      | `none` with warning logged.                      |

The resolved configuration is printed to stderr once at startup so
users see what `auto` chose:

```
trust: auto resolved to backend=sigstore identity=https://github.com/chazu/mu/.github/workflows/build.yml@refs/heads/main
```

### Explicit backend block

```cue
trust: {
    backend: "sigstore"

    // Sigstore endpoints. Omit any field to fall back to public-good.
    fulcio:   "https://fulcio.sigstore.dev"
    rekor:    "https://rekor.sigstore.dev"
    tuf_root: "./trust/root.json"  // omit → embedded public-good root

    sign:   "all"          // all | leaves | none
    verify: "remote-only"  // all | remote-only | none

    policy: {
        require_identity:      "https://github.com/myorg/*"
        require_issuer:        "https://token.actions.githubusercontent.com"
        require_log_inclusion: true
    }
}
```

Public sigstore is `trust: { backend: "sigstore" }` with the URL
fields omitted. Self-hosted is the same shape with URLs filled in.

### Minimal policies

Three tiers of effort, decreasing strictness:

**Tier 0 — `trust: "auto"`** (recommended default for most users)

Detects environment, picks sigstore in CI with identity pinned to the
current repo, picks `none` locally. Zero further config.

**Tier 1 — `trust: { backend: "sigstore" }`** (honest default for users
who want signatures but don't want to think about policy)

Signs and verifies via public-good sigstore. Bundle cryptography and
Rekor log inclusion are checked, but **no identity is pinned**. This
catches tampering but does not catch a malicious signer with a valid
GitHub account. The startup log emits a warning to that effect so
users are not misled.

**Tier 2 — explicit policy** (real production trust)

Add `policy.require_identity` (and ideally `require_issuer`). This is
what actually pins trust to a known principal. Anything less is
integrity-only.

### Omitting `trust:` entirely

Equivalent to `trust: { backend: "none" }`. Current behavior preserved.
No signatures produced, no signatures checked. Hash integrity still
enforced by the existing `mu verify` command.

## Integration Points

### Action execution

The coordinator calls `Signer.Sign` after an action result manifest is
written to the cache. The bundle is stored as a referrer to that
manifest. If `sign: "leaves"`, signing is deferred until the build
completes and the leaf set is known.

### Cache load

The coordinator calls `Verifier.Verify` when loading an action result
from the cache, gated by the `verify` mode. A missing bundle when
verification is required is a verification failure, not a silent pass.
Failures surface as build errors with the offending digest and the
backend's reason string.

### `mu verify`

Today: walks the blob tree, rehashes, compares against filenames.

Extended: with `--trust`, walks the same blobs, resolves referrers,
runs `Verifier.Verify`, applies `Policy.Check`. JSON output gains:

```json
{
  "signatures": {
    "ok": 1234,
    "untrusted": 0,
    "missing": 5,
    "invalid": 0,
    "policy_failed": 0
  }
}
```

Exit nonzero on any `invalid` or `policy_failed`. `missing` is nonzero
exit only when `verify: "all"` is configured; under `remote-only` it
is informational.

### `mu plugin resolve`

Plugin binaries are action outputs like any other, so plugin trust
falls out of the general action-result trust path. The resolver
verifies the plugin blob's signature against the same `trust.policy`
block before exec. There is no plugin-specific policy.

A plugin that fails verification refuses to load with a clear error
naming the digest and the policy that rejected it. The user can
override per-invocation with `--insecure-plugin <digest>` for local
debugging; the override is logged loudly and is not honored in
non-interactive runs.

## Build Tag

Sigstore-go pulls in a meaningful dependency surface (protobuf,
cryptobyte, TUF, OIDC). Users who only want `none` or `x509` should
not pay for it.

`internal/sign/sigstore` is gated behind the `mu_sigstore` build tag.
The default `go install` build includes it. A minimal build
(`go build -tags=""`) excludes it; selecting `backend: "sigstore"`
in such a build is a config-load error with a clear message.

`none`, `x509`, and `in-toto` backends are always compiled in.

## Sequencing

Suggested implementation order. Each step is independently shippable.

1. `internal/sign` interface, `none` backend, plumbing through
   coordinator with `sign: "none", verify: "none"` defaults. No
   behavior change.
2. `x509` backend. Provides a working non-sigstore path before any
   sigstore code lands; useful for testing the interface contract.
3. `mu verify --trust` flag, operating only against `x509` and
   `none`. Establishes the verification CLI surface.
4. `sigstore` backend behind `mu_sigstore` build tag. Public-good
   endpoints first; self-hosted URLs require no extra code, just
   configuration.
5. `trust: "auto"` resolution. Last, because it depends on having
   real backends to resolve to.
6. `in-toto` backend. Optional; ship when a user asks for it.

## Open Questions

- **Referrer storage in non-OCI caches.** mu's CAS is OCI-shaped, but
  the on-disk layout is ours. Confirm the referrer-as-blob convention
  round-trips cleanly through `oci push` to a real registry.
- **Bundle size budget.** Sigstore bundles with embedded Rekor entries
  are 4–8 KB. At 10k actions that is ~50 MB of signature data per
  build. Acceptable, but worth measuring before committing to `sign:
  "all"` as the default for `auto`.
- **Offline verification.** Sigstore-go can verify bundles offline if
  the bundle embeds the Rekor entry. Confirm this is the case for the
  bundles we produce, so `mu verify --trust` works on an airgapped
  host.
- **Policy language scope.** `require_identity` as a glob is enough
  for v1. CEL or a richer expression language is probably warranted
  later but out of scope for the initial design.

## Summary

- One `Backend` interface, swappable implementations.
- Sigstore is one backend among several; public vs self-hosted is a
  URL choice, not a code path choice.
- Sign every action result by default; verify on remote-cache load by
  default.
- `trust: "auto"` covers the zero-config CI case.
- `trust: { backend: "sigstore" }` covers users who want signatures
  without writing policy, with a startup warning that identity is
  unpinned.
- Bundle format is sigstore v0.3 protobuf across all backends for one
  parser path.
- Sigstore code gated by build tag so minimal builds stay lean.
