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

  path                Destination file path (required, except in capture mode).
  content             Literal content to write (mutually exclusive with source/symlink).
  source              Source file to copy (mutually exclusive with content/symlink).
  symlink             Create a symlink to this target (mutually exclusive with content/source).
  absent              If true, ensure the file does not exist (default: false).
  mode                File permissions as octal string (default: "0644").
  owner               File owner (optional, requires privilege).
  group               File group (optional, requires privilege).
  sealed_output_files Capture-mode map: sealed_output NAME -> absolute local
                      path. See "Capture mode" below. Mutually exclusive with
                      path/content/source/symlink/absent.

MODES

  Write content:   Set "content" to the desired file body.
  Copy source:     Set "source" to a file path, or list it in "sources".
  Create symlink:  Set "symlink" to the link target path.
  Delete file:     Set "absent": true.
  Capture secret:  Declare "sealed_outputs" + "config.sealed_output_files".

CAPTURE MODE — local file -> sealed output

  Use when an external command has already written sensitive bytes to
  disk (an openssl-generated key, a downloaded credential, a session
  token mounted from somewhere) and you want those bytes routed through
  a secret provider (pass / vault / sops) instead of being copied into
  CAS or another regular file.

  Pair target.sealed_outputs with config.sealed_output_files (NAME ->
  absolute path). The plan emits a single action that copies each path
  to $MU_SEALED_OUT_DIR/NAME (chmod 0600); the runner then routes each
  through the configured provider's store_secret.

    {
      "target": "//capture/server-key",
      "toolchain": "file",
      "config": {
        "sealed_output_files": {"KEY": "/tmp/openssl-server.key"}
      },
      "sealed_outputs":      {"KEY": "pass:tls/server-key"},
      "sealed_output_modes": {"KEY": "create_if_absent"}
    }

  Constraints:
    - Keys in sealed_output_files MUST exactly match sealed_outputs.
      The plugin rejects mismatches at plan time.
    - Capture mode is mutually exclusive with path/content/source/
      symlink/absent. Don't try to materialize a regular file at the
      same time.
    - The matching ref(s) must appear in secrets.writable_refs in the
      project mu.cue.
    - Actions with sealed_outputs are forced impure (the runner skips
      the cache so the store_secret side-effect always runs).
    - Capture mode does not delete the source file. Pair with a second
      target ({"absent": true}) if you want the on-disk copy gone after
      capture.

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
  capture     Copies each sealed_output_files path to
              $MU_SEALED_OUT_DIR/NAME (capture mode).

CAPABILITIES

  discover, plan
