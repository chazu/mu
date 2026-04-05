# mu + pudl Integration

mu and pudl work together to detect and converge infrastructure drift.

- **pudl** observes actual state, compares it to CUE definitions, and reports drift.
- **mu** takes the desired state and converges it using resource-type plugins.

Neither tool depends on the other at the code level. They communicate through
mu.json — a standard mu configuration file that pudl generates.

## How It Works

```
CUE definitions (desired state)
        │
        ▼
pudl import (observe actual state)
        │
        ▼
pudl drift check (compute diff)
        │
        ▼
pudl export-actions --all (emit mu.json with desired-state targets)
        │
        ▼
mu build --config converge.json (plan via plugins, execute DAG)
```

### Step by step

1. **Define desired state** in CUE (pudl's schema system):

```cue
package definitions

import "file"

nginx_conf: file.#Config & {
    path:    "/etc/nginx/nginx.conf"
    content: """
        server {
            listen 80;
            root /var/www/html;
        }
        """
    mode: "0644"
}
```

2. **Import actual state** into pudl and check for drift:

```bash
pudl import --path /etc/nginx/nginx.conf
pudl drift check nginx_conf
```

3. **Export a mu config** from the drift report:

```bash
pudl export-actions --definition nginx_conf > /tmp/converge.json
```

This produces a mu.json like:

```json
{
  "targets": [
    {
      "target": "//nginx_conf",
      "toolchain": "file",
      "sources": ["definitions/nginx.cue"],
      "config": {
        "path": "/etc/nginx/nginx.conf",
        "content": "server {\n    listen 80;\n    root /var/www/html;\n}",
        "mode": "0644"
      }
    }
  ]
}
```

4. **Converge** with mu:

```bash
mu build --config /tmp/converge.json //nginx_conf
```

mu's file plugin writes the file with the correct content and permissions.

## No Coupling

The key design decision: pudl emits **desired state**, not drift diffs.

- The file plugin receives `{"path": "...", "content": "...", "mode": "..."}`. It doesn't know about pudl, drift reports, or CUE. It just makes the file match the config.
- pudl doesn't know how plugins converge resources. It just translates CUE definitions into mu target configs.
- Any mu plugin works — whether the target came from pudl or a hand-written mu.json.

## Resource Type Mapping

pudl maps CUE schema references to mu toolchain names:

| Schema prefix | mu toolchain |
|--------------|-------------|
| `file.*`, `config.*` | `file` |
| `ec2.*`, `s3.*`, `iam.*`, `aws.*` | `aws` |
| `k8s.*`, `kubernetes.*` | `k8s` |
| (unknown) | `generic` |

Custom mappings can be added in pudl's configuration.

## Available Plugins

| Plugin | What it converges |
|--------|------------------|
| `file` | Local files: write content, copy source, symlink, delete, chmod, chown |

More resource-type plugins (k8s, aws, etc.) can be added following the same
pattern — a bb script that speaks mu's NDJSON protocol and plans convergence
actions for its resource type.

## Writing a Resource Plugin

A resource plugin receives a target with desired state in `config` and emits
actions to converge the actual state. See `plugins/file/plugin.bb` for a
complete example.

The plugin needs to implement two NDJSON methods:

- **discover**: declare what resource types it handles
- **plan**: given desired state, emit shell commands to converge

The plugin does NOT need to know about pudl, drift, or CUE. It only sees
the desired state as a plain JSON config object.
