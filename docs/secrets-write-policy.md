# Secrets Write Policy

## Status

Available now. Opt-in: a project with no `secrets` block in its
`mu.cue` keeps the previous behavior (writes are unrestricted). Once
declared, the policy is enforced both at plan time (eagerly, before
any provider plugin starts) and at write time (defense-in-depth, in
the `SealedOutputWriter` closure).

---

## What it's for

The writable-secrets work added a `store_secret` capability and
runner-side `sealed_outputs` plumbing. By default, any plugin
declaring `store_secret` can write to any ref under its scheme — a
`pass`-backed `secret-gen` target with a typo or a malicious upstream
plugin update could in principle clobber any entry in your password
store.

The read side has a natural bound: you only get back what you
explicitly asked for. The write side didn't — until now.

`secrets.writable_refs` is a project-level allow-list. Any
sealed-output write is gated against it. Refs that don't match are
rejected at plan time (so the offending build can't even start a
provider manager).

---

## Configuration

Top-level `secrets` block in your `mu.cue`:

```cue
secrets: {
    // Allow-list of glob patterns. Any sealed_output ref must match
    // at least one pattern. Patterns are matched against the FULL
    // ref including the scheme (e.g. "pass:registry/admin").
    writable_refs: [
        "pass:registry/*",
        "pass:loosh/*",
    ]
}
```

### Pattern syntax

Patterns use Go's `path.Match` semantics:

- `*` — matches any run of characters except `/`
- literal text — matches itself
- `?`, `[a-z]`, `[!abc]` — character classes (rarely useful here)

Notably, `*` does **not** span `/`. To allow nested paths, list each
depth or use `**`-style multi-level globs (which are NOT supported;
list each pattern explicitly):

```cue
writable_refs: [
    "pass:registry/*",            // pass:registry/admin
    "pass:registry/*/htpasswd",   // pass:registry/zone-a/htpasswd
]
```

### Three policy states

| State                                     | Meaning                                      |
| ----------------------------------------- | -------------------------------------------- |
| `secrets` block omitted                   | No allow-list — all writes permitted (back-compat). |
| `secrets: writable_refs: [...patterns]`   | Only matching refs may be written to.        |
| `secrets: writable_refs: []`              | Explicit deny-all — no sealed-output writes. |

The empty-list form is useful as a project-wide lockdown that you
can lift target-by-target by editing the patterns, or in CI configs
that explicitly forbid secret writes.

---

## Enforcement points

`mu` checks the policy in two places:

1. **Plan time.** As soon as the action graph is built, every
   `sealed_output` ref in every action is checked. A non-matching
   ref aborts `Plan()` with an error like:
   ```
   coordinator: action "//secrets/personal:gen": sealed_output ref
   "pass:personal/all-the-things" is not allowed by
   secrets.writable_refs (2 pattern(s))
   ```
   This fires before the provider-only plugin manager spins up, so
   a forbidden write never even gets the chance to run.

2. **Write time.** Inside `Coordinator.Execute`, the
   `SealedOutputWriter` closure re-checks the ref against the policy
   before forwarding to `mgr.StoreSecret`. This is defense in depth
   — if a future refactor routes around the plan-time check, the
   write itself still fails closed.

---

## Worked example

A project that bootstraps a registry admin password but *only*
allows writes under `pass:registry/`:

```cue
plugins: [
    {name: "pass", script: "plugins/pass/plugin.bb"},
]

secrets: {
    writable_refs: ["pass:registry/*"]
}

targets: [
    // OK: matches pass:registry/*
    {
        target:    "//secrets/admin"
        toolchain: "secret-gen"
        config: {
            ref:        "pass:registry/admin"
            derivation: ["openssl", "rand", "-base64", "24"]
        }
    },

    // FAIL at plan time: this ref is outside the allow-list.
    // {
    //     target:    "//secrets/personal-rotation"
    //     toolchain: "secret-gen"
    //     config: {
    //         ref:        "pass:personal/email-app"   // not under pass:registry/
    //         derivation: ["openssl", "rand", "-base64", "32"]
    //     }
    // },
]
```

Running `mu build //secrets/admin` works; uncommenting the second
target fails with a plan-time error before any `pass insert` runs.

---

## What it does *not* protect against

- **Reads.** `sealed_inputs` is unaffected; the read side is bounded
  by which refs your `mu.cue` explicitly references. The write
  policy does not gate `resolve_secret` calls.
- **Out-of-band writes.** If a target shells out to `pass insert`
  directly (or any other tool that talks to the backend without
  going through the `store_secret` plugin protocol), `mu` cannot see
  it. The policy only gates writes that flow through
  `sealed_outputs`.
- **Plugin-vs-plugin isolation.** All registered plugins with the
  `store_secret` capability share the same allow-list. Per-plugin
  scoping is not implemented; if you have multiple write-capable
  schemes (e.g. `pass` + `vault`), the patterns must include each
  scheme prefix.

---

## Suggested defaults

For most projects, an explicit allow-list scoped to the project's
own namespace catches typos and limits blast radius without much
maintenance burden:

```cue
secrets: writable_refs: [
    "pass:<your-project-prefix>/*",
    "pass:<your-project-prefix>/*/*",  // if you use nested paths
]
```

Tighter projects (especially anything with shared password stores
across humans and machines) should pin individual refs:

```cue
secrets: writable_refs: [
    "pass:registry/admin",
    "pass:registry/htpasswd",
]
```
