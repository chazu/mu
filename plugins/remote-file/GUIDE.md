# remote-file plugin

Ensures a file on a remote host matches a local source file's bytes,
mode, and owner. SSH-based. Idempotent via `mu build` (always re-asserts).

## Capabilities

- `discover`
- `plan` — emits one action that pushes the source file to the remote path.
- `observe` — returns `{exists, sha256, mode, owner, group}` for the remote path.

## Config

| field | type | default | notes |
|---|---|---|---|
| `host` | string | — (required) | hostname or IP |
| `user` | string | `"root"` | SSH user |
| `port` | int | `22` | SSH port |
| `path` | string | — (required) | absolute remote path |
| `mode` | string | (unset) | e.g. `"0644"` |
| `owner` | string | (unset) | username |
| `group` | string | (unset) | group name |
| `make_parents` | bool | `true` | `install -d` on dirname(path) |
| `sudo` | bool | `false` | pipe SSH_PASS to `sudo -S` for privileged writes |

## Sources

Exactly one file. Zero or more than one → plan error.

## Sealed inputs

- `SSH_PASS` — optional. If absent, falls back to ssh-agent (`BatchMode=yes`).
  Required when `sudo: true`; same value is used as sudo password.

## Example

```json
{
  "target": "//etc/caddy-config",
  "toolchain": "remote-file",
  "sources": ["caddy/Caddyfile"],
  "config": {
    "host": "example.com",
    "user": "deploy",
    "path": "/etc/caddy/Caddyfile",
    "mode": "0644",
    "owner": "root",
    "group": "root",
    "sudo": true
  },
  "sealed_inputs": {
    "SSH_PASS": "pass:servers/deploy@example.com"
  }
}
```

## Observe record schema

```json
{
  "_schema": "remote.file",
  "host": "example.com",
  "path": "/etc/caddy/Caddyfile",
  "exists": true,
  "sha256": "abc123...",
  "mode": "0644",
  "owner": "root",
  "group": "root"
}
```

`exists: false` when the path does not exist.
