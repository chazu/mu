mu guide plugin file — file convergence plugin

Converges files to a desired state: write content, set permissions,
create symlinks, or delete files.

USAGE IN mu.json

  {
    "target": "//etc/nginx-conf",
    "toolchain": "file",
    "sources": [],
    "config": {
      "path": "/etc/nginx/nginx.conf",
      "content": "server { listen 80; root /var/www/html; }",
      "mode": "0644"
    }
  }

CONFIG FIELDS

  path      Destination file path (required).
  content   Literal content to write (mutually exclusive with source/symlink).
  source    Source file to copy (mutually exclusive with content/symlink).
  symlink   Create a symlink to this target (mutually exclusive with content/source).
  absent    If true, ensure the file does not exist (default: false).
  mode      File permissions as octal string (default: "0644").
  owner     File owner (optional, requires privilege).
  group     File group (optional, requires privilege).

MODES

  Write content:   Set "content" to the desired file body.
  Copy source:     Set "source" to a file path, or list it in "sources".
  Create symlink:  Set "symlink" to the link target path.
  Delete file:     Set "absent": true.

EXAMPLES

  Write a config file:
    {"path": "/etc/app.conf", "content": "key=value", "mode": "0600"}

  Copy from source:
    {"path": "/usr/local/bin/script", "source": "scripts/run.sh", "mode": "0755"}

  Create symlink:
    {"path": "/etc/nginx/sites-enabled/default", "symlink": "/etc/nginx/sites-available/app"}

  Ensure file is absent:
    {"path": "/tmp/stale-lock", "absent": true}

ACTIONS GENERATED

  write/copy  Creates parent directories, writes or copies the file.
  chmod       Sets file permissions (runs after write/copy).
  chown       Sets owner/group if specified (runs after write/copy).
  symlink     Creates or updates a symbolic link.
  remove      Deletes the file (for absent: true).

CAPABILITIES

  discover, plan
