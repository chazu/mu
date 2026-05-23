mu guide sandbox — hermetic execution environments

ISOLATION LEVELS

  mu auto-detects the strongest isolation available on the platform:

  Level      Platform   Enforcement
  ─────      ────────   ───────────
  Copy       all        Temp directory with restricted PATH/env. No kernel
                        enforcement — undeclared host paths are accessible.

  Seatbelt   macOS      sandbox-exec with a deny-default SBPL profile.
                        Kernel-enforced: file writes restricted to output/tmp/work,
                        network denied when hermetic, read access to system libs only.

  Namespace  Linux      User/mount/PID/IPC/UTS/network namespaces via clone().
                        pivot_root to tmpfs, minimal /dev, read-only rootfs.
                        Full filesystem and network isolation.

WHAT'S ENFORCED

  Property            Copy    Seatbelt   Namespace
  ────────            ────    ────────   ─────────
  Restricted PATH     yes     yes        yes
  Restricted TMPDIR   yes     yes        yes
  File write deny     no      yes        yes
  File read deny      no      partial    yes
  Network deny        no      yes        yes
  PID isolation       no      no         yes
  UID isolation       no      no         yes

NETWORK FLAG

  Actions declare Network: true/false. With Seatbelt or Namespace isolation,
  Network: false is enforced by the kernel (not honor-system).

  - Seatbelt: SBPL rules deny network-outbound/inbound/bind
  - Namespace: CLONE_NEWNET creates isolated network stack (loopback down)
  - Copy: Network flag is advisory only

PLATFORM NOTES

  macOS Seatbelt:
    sandbox-exec is deprecated since ~2016 but still used by Apple internally
    (mDNSResponder, system daemons) and by Nix, Bazel, Chromium. Cannot nest
    sandbox-exec inside sandbox-exec — falls back to Copy in sandboxed CI.

  Linux Namespaces:
    Requires unprivileged user namespaces. Check:
      cat /proc/sys/kernel/unprivileged_userns_clone
    Value "0" means disabled (Debian/Ubuntu default). mu falls back to Copy.
    Ubuntu 24.04+ may also restrict via AppArmor.

  The re-exec pattern (used by Docker, Bazel, bubblewrap): mu re-executes
  itself as PID 1 inside new namespaces. The sentinel __sandbox_init__ in
  os.Args triggers the init path in cmd/mu/main.go.

BENCHMARKS (Apple M3, sandbox package)

  Copy:      ~2.5 ms/action, 26 KB, 268 allocs
  Seatbelt:  ~7.1 ms/action, 36 KB, 353 allocs

  Overhead: ~4.5 ms per action for kernel-enforced isolation.

QUERYING ISOLATION LEVEL

  The Sandbox.Level() method returns the actual isolation achieved.
  Build manifests can include this for downstream attestation.
