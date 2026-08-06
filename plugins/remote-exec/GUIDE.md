# remote-exec plugin

> Implemented in Go using `sdk/muplugin`; the original Babashka version
> at `plugin.bb` remains in-tree for one release cycle. See
> `mu guide sdk`.

Runs a command on a remote host via SSH. Optional `check` guard skips
execution when a precondition is already met.

## Capabilities

- `discover`
- `plan` — one action: ssh to host, optionally run `check`, run `command`,
  optionally fetch `sealed_output_files` back into `$MU_SEALED_OUT_DIR`.

No `observe`. A command isn't a resource with state.

## Config

| field | type | default | notes |
|---|---|---|---|
| `host` | string | — (required) | |
| `user` | string | `"root"` | SSH user |
| `port` | int | `22` | SSH port |
| `command` | []string | — (required) | argv run remotely |
| `check` | []string | (unset) | if exits 0, command is skipped |
| `env` | map[string]string | (unset) | exported before command |
| `work_dir` | string | (unset) | `cd` before command |
| `sudo` | bool | `false` | pipe SSH_PASS to `sudo -S` |
| `impure` | bool | `true` | pass-through; set `false` to enable cache-on-deps |
| `sealed_output_files` | map[string]string | (unset) | sealed_output NAME → absolute remote path; see Sealed outputs |

## Sealed inputs

- `SSH_PASS` — optional; required when `sudo: true`. Single-line; use `env`
  delivery (the default).
- Any other env vars passed via `sealed_inputs` are forwarded to the remote
  command transparently.

### Multi-line / binary secrets (`sealed_input_modes: file`)

For SSH private keys, GPG keys, JSON service-account blobs, kubeconfigs,
and other secrets that don't fit comfortably in a shell env var, declare
the input with `file` mode:

```json
"sealed_inputs":      {"DEPLOY_KEY": "pass:raw:hosts/dalian/key"},
"sealed_input_modes": {"DEPLOY_KEY": "file"}
```

The runner writes the resolved bytes to a 0600 temp file and exports
`$DEPLOY_KEY` as the **path** to that file. The remote-exec wrapper
forwards the value (the path string) like any other env var, so the
remote command sees a path local to the **builder**, not the remote — for
SSH keys you typically use the file on the builder side (e.g.
`ssh -i "$DEPLOY_KEY" ...` from within `command`). The temp directory is
removed when the action exits regardless of success.

`file` mode is rejected in toolchain-pinned (sandbox) actions; remote-exec
runs bare and supports it.

## Sealed outputs

Use `sealed_outputs` when the remote command produces a secret that
should land in a secret store (pass / vault / sops) instead of CAS —
join tokens, ephemeral admin credentials, generated key material, etc.

Pair `sealed_outputs` (target-level) with `config.sealed_output_files`
(plugin-specific) to tell the wrapper which remote file backs each
named output:

```cue
targets: [{
  target: "//bootstrap/k8s-join-token"
  toolchain: "remote-exec"
  config: {
    host: "control.example.com"
    user: "root"
    command: ["bash", "-c", "kubeadm token create --print-join-command > /tmp/join"]
    sealed_output_files: JOIN: "/tmp/join"
  }
  sealed_inputs: SSH_PASS: "pass:servers/root@control"
  sealed_outputs: JOIN: "pass:bootstrap/k8s-join"
  sealed_output_modes: JOIN: "create_if_absent"
}]
```

After the remote command succeeds, the wrapper opens a second ssh
connection per output and runs `cat <remote-path>`, redirecting the
bytes to `$MU_SEALED_OUT_DIR/<NAME>` on the builder (chmod 0600). The
runner then routes each file through the configured secret provider's
`store_secret` and removes the temp dir.

**Constraints**

- Keys in `config.sealed_output_files` must match exactly the keys in
  `sealed_outputs`. The plugin rejects mismatches at plan time.
- The remote command is responsible for *producing* each declared file
  before exiting. The wrapper does not retry; a missing file becomes
  `cat: No such file or directory` and the action fails.
- Add a matching entry under `secrets.writable_refs` in your project's
  `mu.cue`, e.g. `"pass:bootstrap/*"`. Refs not allow-listed are
  rejected at plan time.
- Actions with non-empty `sealed_outputs` are forced impure — the cache
  is skipped so the `store_secret` side-effect always runs. Combining
  `sealed_outputs` with `impure: false` is meaningless.


## Cache-on-deps pattern

To re-run an exec only when a dep changes:

```cue
targets: [{
  target: "//caddy/reload"
  toolchain: "remote-exec"
  deps: ["//caddy/config"]
  config: {
    host: "example.com"
    user: "deploy"
    command: ["systemctl", "reload", "caddy"]
    sudo: true
    impure: false
  }
  sealed_inputs: SSH_PASS: "pass:servers/deploy@example.com"
}]
```

`impure: false` + `deps` means the cache key incorporates the dep's
digest. The action re-runs exactly when the Caddyfile changes.

## check-based idempotency

```cue
targets: [{
  target: "//install/jq"
  toolchain: "remote-exec"
  config: {
    host: "example.com"
    user: "deploy"
    command: ["apt-get", "install", "-y", "jq"]
    check: ["which", "jq"]
    sudo: true
  }
  sealed_inputs: SSH_PASS: "pass:servers/deploy@example.com"
}]
```
