mu guide plugin host — remote host observer

(Implemented in Go using sdk/muplugin; the original Babashka version
 at plugin.bb remains in-tree for one release cycle. See 'mu guide sdk'.)

Observes the state of a remote host via SSH. Gathers OS info, packages,
services, filesystems, network interfaces, and users.

USAGE IN mu.cue

  targets: [{
    target: "//infra/webserver"
    toolchain: "host"
    sources: []
    config: {
      host: "192.168.1.100"
      user: "admin"
      key: "~/.ssh/id_ed25519"
      port: 22
    }
  }]

CONFIG FIELDS

  host    Hostname or IP address (required).
  user    SSH user (default: "root").
  key     Path to SSH private key (optional).
  port    SSH port (default: 22).

SECRETS (via sealed_inputs)

  For password authentication:

    sealed_inputs: SSH_PASS: "pass:infra/webserver-password"

  When SSH_PASS is set, the plugin uses sshpass for authentication.
  Otherwise it uses key-based authentication. SSH_PASS is short and
  uses env delivery (the default).

  Multi-line / binary secrets — file delivery mode

  For SSH private keys stored in pass (or any multi-line secret), use
  sealed_input_modes to receive the value as a file path rather than
  an env-var literal:

    "sealed_inputs":      {"DEPLOY_KEY": "pass:raw:infra/webserver-key"},
    "sealed_input_modes": {"DEPLOY_KEY": "file"}

  $DEPLOY_KEY then holds the path to a 0600 temp file holding the key
  bytes. The temp dir is removed when the action exits. The host
  plugin runs bare (no sandbox), so file mode works; reach for it any
  time the secret would not survive shell quoting as an env value.

EXAMPLES

  Key-based auth:
    {"host": "10.0.0.5", "user": "deploy", "key": "~/.ssh/deploy_key"}

  Password auth:
    {"host": "10.0.0.5", "user": "admin"}
    + sealed_inputs: {"SSH_PASS": "pass:infra/admin-password"}

OBSERVATION OUTPUT

  mu observe --json //infra/webserver

  Returns structured records with _schema "linux.host" containing:
  - OS info (distro, version, kernel, architecture)
  - Installed packages
  - Running services
  - Filesystem mounts and usage
  - Network interfaces and addresses
  - System users

  SSH options: StrictHostKeyChecking=accept-new, ConnectTimeout=10.

CAPABILITIES

  discover, observe
