# `secret-gen` Toolchain

## Status

Built-in toolchain. Available on every `mu` project — no plugin
registration required.

This document describes how to use `secret-gen` to declaratively mint
secrets and persist them through a `store_secret`-capable provider
plugin (today: `pass`). For the underlying protocol design, see the
brainstorm at
[`brainstorms/2026-04-29-writable-secrets.md`](brainstorms/2026-04-29-writable-secrets.md).

---

## What it's for

You have a target somewhere downstream that consumes a secret as a
`sealed_inputs` ref:

```cue
target: "//zot/htpasswd"
toolchain: "remote-exec"
sealed_inputs: ADMIN_PASS: "pass:registry/admin"
```

Today this only works if somebody has already run `pass insert
registry/admin` out of band. `secret-gen` lets you declare that the
secret should exist, with a derivation that produces it on first build,
so the consumer's `deps` can express the bootstrapping order:

```cue
target: "//secrets/admin-pass"
toolchain: "secret-gen"
config: {
    ref:        "pass:registry/admin"
    derivation: ["openssl", "rand", "-base64", "24"]
}
```

After the first build, the secret exists in pass; subsequent builds
re-run the derivation but the provider treats the store as a no-op
(default mode is `create_if_absent`).

---

## How it works

A `secret-gen` target plans into exactly one action:

1. The action runs `derivation` (an argv array) under bash.
2. Stdout is captured and (by default) the trailing newline is
   stripped.
3. The captured value is written to `$MU_SEALED_OUT_DIR/VALUE` — a
   per-action temporary directory created by the executor.
4. After the action exits successfully, the executor reads
   `VALUE` and routes it through the configured provider plugin's
   `store_secret` method, with the mode you selected.
5. The temp directory is deleted unconditionally; the value bytes are
   zeroed in memory after routing.

The action is always **impure** — caching would skip the
`store_secret` side-effect, which would be wrong if the provider entry
has been wiped. The derivation runs every build; for cheap derivations
(`openssl rand`, `uuidgen`) this is fine, and for `create_if_absent`
mode the wasted work is bounded to the derivation itself.

Values never appear in stdout, the action cache, build manifests, or
verbose logs. The provider ref *is* part of the cache key — changing
the destination invalidates any (hypothetical future) cached entry.

---

## Config reference

```cue
target: "//secrets/some-name"
toolchain: "secret-gen"
config: {
    // Required: destination ref. Must be of the form scheme:path.
    // The scheme must match a registered plugin that declares the
    // store_secret capability (today: "pass").
    ref: "pass:registry/admin"

    // Required: argv whose stdout becomes the stored value.
    derivation: ["openssl", "rand", "-base64", "24"]

    // Optional: store mode. Default "create_if_absent".
    //   - "create"           — fail if the entry already exists.
    //   - "overwrite"        — always set, replacing any prior value.
    //   - "create_if_absent" — no-op if the entry exists; create otherwise.
    mode: "create_if_absent"

    // Optional: extra environment variables for the derivation.
    // Useful for pinning randomness sources or auth tokens for
    // network-fetching derivations (note: network is not currently
    // configurable on secret-gen actions).
    env: {
        SOMETHING: "value"
    }

    // Optional: keep the derivation's trailing newline. Default false
    // (a single trailing newline is stripped). Set true if you're
    // generating something where whitespace is significant — e.g. a
    // PEM block where downstream consumers expect the closing newline.
    keep_trailing_newline: false
}
```

`sources`, `outputs`, and `network` are not used by `secret-gen` and
are silently ignored if present.

---

## Picking a mode

| Mode               | Use when                                                                                  |
| ------------------ | ----------------------------------------------------------------------------------------- |
| `create_if_absent` | Default. Bootstrapping a credential that, once minted, must remain stable across builds.  |
| `overwrite`        | Rotation: the derivation produces a fresh value each run and you want the store to track. |
| `create`           | Strict bootstrap: catch the case where someone seeded a value out of band by hand.        |

If your derivation is non-deterministic (the common case — `openssl
rand`, `uuidgen`), `overwrite` will rotate the secret on every build,
which is almost always not what you want. Stick with `create_if_absent`
unless you have an explicit rotation story.

---

## Worked example: bootstrapping zot's admin password

```cue
plugins: [
    {name: "pass", script: "plugins/pass/plugin.bb"},
    // ...
]

targets: [
    {
        target: "//secrets/zot-admin"
        toolchain: "secret-gen"
        sources: []
        config: {
            ref:        "pass:loosh/registry.loosh.cloud/admin"
            derivation: ["openssl", "rand", "-base64", "32"]
        }
    },
    {
        target: "//zot/htpasswd"
        toolchain: "remote-exec"
        sources: []
        deps: ["//zot/apache2-utils", "//secrets/zot-admin"]
        config: {
            host: "dalian.softchewy.center"
            user: "customer"
            port: 22
            command: ["bash", "-c", "install -d -m 0755 /etc/zot && htpasswd -bBn admin \"$ADMIN_PASS\" > /etc/zot/htpasswd && chmod 0640 /etc/zot/htpasswd"]
            sudo: true
        }
        sealed_inputs: {
            SSH_PASS:   "pass:Servers/customer@dalian.softchewy.center"
            ADMIN_PASS: "pass:loosh/registry.loosh.cloud/admin"
        }
    },
]
```

First `mu build //zot/htpasswd`: `//secrets/zot-admin` runs, mints a
fresh password, stores it under `pass:loosh/registry.loosh.cloud/admin`.
Then `//zot/htpasswd` resolves the same ref via its `sealed_inputs`,
gets the value, runs `htpasswd` on the remote host.

Subsequent builds: `//secrets/zot-admin` runs the derivation (it's
impure), but `pass insert -m -f` is gated by the `create_if_absent`
mode and short-circuits to a no-op because the entry already exists.
The downstream `//zot/htpasswd` action's cache key changes only when
its own non-secret inputs change, so it caches normally.

---

## What `secret-gen` is *not*

- **Not a generic "run-once" toolchain.** It only handles the
  derive-then-store pattern. If you need to mint something that lives
  in the filesystem rather than a secret store, use `shell` with
  `outputs`.
- **Not a way to read a secret.** `sealed_inputs` is still the only
  read path. `secret-gen` only writes.
- **Not aware of the value.** mu sees the value bytes only briefly,
  in memory, between reading `$MU_SEALED_OUT_DIR/VALUE` and routing
  through `store_secret`. It is never written to disk outside the
  per-action temp dir, never logged, never hashed into the cache key.

---

## Provider support

`secret-gen` requires the destination ref's scheme to map to a plugin
declaring the `store_secret` capability. Current support:

| Scheme | Plugin         | Status                         |
| ------ | -------------- | ------------------------------ |
| `pass` | `plugins/pass` | Supported (v0.3.0+).           |

Adding a new backend means implementing the plugin protocol's
`store_secret` method. See `internal/plugin/protocol.go` for the
wire format and `plugins/pass/plugin.bb` for the reference
implementation.
