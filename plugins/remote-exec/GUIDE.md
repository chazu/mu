# remote-exec plugin

Runs a command on a remote host via SSH. Optional `check` guard skips
execution when a precondition is already met.

## Capabilities

- `discover`
- `plan` — one action: ssh to host, optionally run `check`, run `command`.

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

## Sealed inputs

- `SSH_PASS` — optional; required when `sudo: true`.

## Cache-on-deps pattern

To re-run an exec only when a dep changes:

```json
{
  "target": "//caddy/reload",
  "toolchain": "remote-exec",
  "deps": ["//caddy/config"],
  "config": {
    "host": "example.com",
    "user": "deploy",
    "command": ["systemctl", "reload", "caddy"],
    "sudo": true,
    "impure": false
  },
  "sealed_inputs": {"SSH_PASS": "pass:servers/deploy@example.com"}
}
```

`impure: false` + `deps` means the cache key incorporates the dep's
digest. The action re-runs exactly when the Caddyfile changes.

## check-based idempotency

```json
{
  "target": "//install/jq",
  "toolchain": "remote-exec",
  "config": {
    "host": "example.com",
    "user": "deploy",
    "command": ["apt-get", "install", "-y", "jq"],
    "check":   ["which", "jq"],
    "sudo": true
  },
  "sealed_inputs": {"SSH_PASS": "pass:servers/deploy@example.com"}
}
```
