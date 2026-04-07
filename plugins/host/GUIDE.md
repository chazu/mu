mu guide plugin host — remote host observer

Observes the state of a remote host via SSH. Gathers OS info, packages,
services, filesystems, network interfaces, and users.

USAGE IN mu.json

  {
    "target": "//infra/webserver",
    "toolchain": "host",
    "sources": [],
    "config": {
      "host": "192.168.1.100",
      "user": "admin",
      "key": "~/.ssh/id_ed25519",
      "port": 22
    }
  }

CONFIG FIELDS

  host    Hostname or IP address (required).
  user    SSH user (default: "root").
  key     Path to SSH private key (optional).
  port    SSH port (default: 22).

SECRETS (via sealed_inputs)

  For password authentication:

    "sealed_inputs": {"SSH_PASS": "pass:infra/webserver-password"}

  When SSH_PASS is set, the plugin uses sshpass for authentication.
  Otherwise it uses key-based authentication.

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
