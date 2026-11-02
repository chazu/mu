mu guide sandbox — hermetic execution environments

WHICH ACTIONS RUN SANDBOXED

  Impure actions always run on the host. A pure action runs in the sandbox
  when it names scratch toolchains ("toolchains" in the action spec), or —
  for every pure action — when MU_SANDBOX=1 is set in mu's environment.
  MU_SANDBOX is off by default until plugins stop relying on host-installed
  tools; it will become the default. Sandboxed and bare runs of the same
  action have different action keys, so toggling it never reuses results.

WHAT A SANDBOXED ACTION SEES

  sandbox root/      unpacked artifacts of the named toolchains
    bin/             toolchain binaries; first on PATH
    work/            mirrors the project root, holding ONLY the action's file
                     inputs at their project-relative paths: project files and
                     upstream outputs (fetched from CAS by digest). Each file
                     is checked against the digest in the action key; a file
                     edited since planning fails the action.
    work/<work_dir>  the command's working directory
    out/             $MU_OUT, for actions with relative outputs
    tmp/             $TMPDIR
    sealed/          file-mode sealed inputs (0600), removed right after exit

  Host paths named in the action's "host_paths" are readable and executable
  at their own absolute paths (read-only); nothing else from the host is,
  apart from the platform's system directories.

  Without a declared PATH the action gets bin/ plus /usr/bin:/bin. After the
  command exits, each declared output is taken from out/ or the working
  directory and copied back to the host work directory. Outputs must be
  relative paths inside the work directory; absolute or escaping outputs are
  an error for sandboxed actions.

ISOLATION LEVELS

  mu auto-detects the strongest isolation available on the platform:

  Level      Platform   Enforcement
  ─────      ────────   ───────────
  Copy       all        Temp directory with restricted PATH/env. No kernel
                        enforcement — undeclared host paths are accessible.

  Seatbelt   macOS      sandbox-exec with a deny-default SBPL profile.
                        Kernel-enforced: file writes restricted to output/tmp/work,
                        network denied when hermetic, data reads limited to sandbox
                        and platform/runtime trees; metadata lookup is allowed.

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
  os.Args triggers package initialization, including in test binaries.

BENCHMARKS (Apple M3, sandbox package)

  Copy:      ~2.5 ms/action, 26 KB, 268 allocs
  Seatbelt:  ~7.1 ms/action, 36 KB, 353 allocs

  Overhead: ~4.5 ms per action for kernel-enforced isolation.

QUERYING ISOLATION LEVEL

  The Sandbox.Level() method returns the actual isolation achieved.
  Build manifests can include this for downstream attestation.

CONTINUOUS QUALIFICATION

  .github/workflows/ci.yml pins Go 1.26.2 and action revisions and runs Linux
  and macOS unit/race/vet/build gates. Native acceptance separately builds a
  static helper and requires Namespace on Linux or Seatbelt on macOS; it
  fails rather than silently accepting copy isolation. Checks cover writable
  work directories, denied host reads/writes, and denied/allowed networking.

  Run the native gate locally:
    CGO_ENABLED=0 go build -o /tmp/mu-native-helper ./internal/sandbox/testdata/native-helper
    MU_NATIVE_HELPER=/tmp/mu-native-helper MU_REQUIRE_NATIVE_SANDBOX=1 go test -count=1 -v ./internal/sandbox -run '^TestNativeSandboxAcceptance$'

  Host-shell unit tests explicitly use copy isolation. They are environment
  tests, not evidence of kernel enforcement. Babashka qualification and real
  PUDL integration remain separate from this Go/native matrix.
