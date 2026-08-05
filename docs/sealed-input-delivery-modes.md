# Sealed Input Delivery Modes

## Status

Available in mu now. The default (`env`) is backwards-compatible with
all existing `sealed_inputs` declarations. Sandbox-mode (toolchain)
actions support `env` only — `file` mode requires bare execution.

---

## What this is for

By default, `sealed_inputs` resolves a secret reference and exports
the value as an environment variable in the action's process. That
works fine for short tokens and passwords:

```cue
sealed_inputs: API_TOKEN: "pass:deploy/token"
```

It chafes for two real cases:

- **Multi-line secrets** — SSH private keys, x509 certs, JSON
  service-account blobs. They survive in env vars (the loosh project
  exercises this with `pass:raw:`), but at multi-KB sizes the
  env-var shape is awkward, and many tools that consume them want a
  *file path*, not a literal value: `ssh -i <path>`, `kubectl
  --kubeconfig <path>`, `gcloud auth activate-service-account
  --key-file <path>`.
- **Process-table leakage** — env vars appear in `ps -E` and
  `/proc/<pid>/environ` for the action process and any child it
  forks. Tolerable for short tokens; less so for SSH keys.

`sealed_input_modes` lets you say "this one wants to be a file
instead":

```cue
sealed_inputs: {
    API_TOKEN: "pass:deploy/token"             // env var (default)
    SSH_KEY:   "pass:raw:hosts/dalian/key"     // file (declared below)
}
sealed_input_modes: {
    SSH_KEY: "file"
}
```

---

## How it works

For each name in `sealed_inputs`, the executor looks up the
corresponding entry in `sealed_input_modes`:

| Mode             | Delivery                                                               |
| ---------------- | ---------------------------------------------------------------------- |
| `env` (default)  | Value exported as `$NAME`. Current behavior, byte-for-byte unchanged.  |
| `file`           | Value written to a 0600 temp file under a per-action sealed-input directory; `$NAME` holds the path. The directory is removed unconditionally after the action exits, so the file does not outlive the action. |

The temp directory is per-action (`mu-sealed-in-*` under `$TMPDIR`,
mode 0700), so multiple sealed inputs with `mode=file` share one
parent directory but live as separate files named exactly after the
sealed-input name.

The default mode (when `sealed_input_modes` is unset for that name,
or set to the empty string, or set to `"env"`) is `env`. Existing
configs keep working with no changes.

With `sealed_routing: "strict"`, the action that consumes a name must claim the
same effective mode as its target declaration. Omitting a claim, changing a ref
or mode, or leaving a target input unused fails planning. The check uses refs
and modes only; the provider value is resolved immediately before execution.

---

## Cache key behavior

Sealed input *values* remain excluded from the cache key — that
invariant has not changed. But:

- The destination *ref* is now part of the cache key. Changing
  `pass:deploy/v1` to `pass:deploy/v2` invalidates the cached entry,
  even if the resolved value happens to be identical. Previously the
  cache would serve the stale entry; that was a footgun.
- The *delivery mode* is part of the cache key. Switching a name from
  `env` to `file` invalidates the cache, because the action observes
  a path on disk in one case and a value in env in the other —
  different observable behavior.

---

## Sandbox limitation

`mode: file` is not supported when the action runs in a hermetic
toolchain sandbox. The temp file lives on the host filesystem outside
the sandbox's view, so the path exported as `$NAME` would not
resolve inside the sandbox. The executor errors out at exec time
rather than silently exporting a broken path.

`mode: env` works in both bare and sandboxed actions.

The toolchains that currently use the sandbox are the scratch-built
ones (e.g. `bb`, `go`); shell, secret-gen, remote-exec,
remote-file, and the various plugin-emitted actions that don't pin
a toolchain run bare and fully support `mode: file`.

---

## Worked example: forwarding an SSH key into ssh-agent

The loosh project has a `//void/load-key` target that pulls an SSH
private key out of pass and feeds it into ssh-agent on the build
host. With `mode: file`, the bash gymnastics shrink:

Before (env mode, hand-rolled extraction):

```cue
target: "//void/load-key"
toolchain: "shell"
config: command: ["bash", "-c", """
    printf '%s\\n' "$SSH_KEY" | ssh-add -
"""]
sealed_inputs: SSH_KEY: "pass:raw:loosh/void.loosh.cloud"
```

After (file mode):

```cue
target: "//void/load-key"
toolchain: "shell"
config: command: ["bash", "-c", "ssh-add \"$SSH_KEY\""]
sealed_inputs: SSH_KEY: "pass:raw:loosh/void.loosh.cloud"
sealed_input_modes: SSH_KEY: "file"
```

`$SSH_KEY` now holds a path to a 0600 temp file containing the key.
mu owns the file: created with the right mode, deleted after the
action exits regardless of success.

---

## When to use which

- Default to `env` — short tokens, passwords, anything that already
  works.
- Use `file` for: SSH keys, kubeconfigs, x509 certs, signed JSON
  blobs, anything multi-line, anything you're going to feed to a
  CLI flag like `--key-file <path>`.
- `stdin` mode (pipe value to subprocess stdin) is **not yet
  available**. The piping-into-stdin pattern (`ssh-add -`,
  `pass insert -m`) currently has to read from a file produced by
  `mode: file`, or stay in `mode: env` with the `printf '%s' "$X" |`
  prefix.
