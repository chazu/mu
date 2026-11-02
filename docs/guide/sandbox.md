mu guide sandbox — hermetic execution environments

WHICH ACTIONS RUN SANDBOXED

  Every pure action runs in the sandbox. Impure actions (side effects on the
  host: converge steps, sealed-output writers, lint fixes) run on the host.
  A pure action that needs a host-installed tool names it in "host_paths"
  or, better, uses a scratch toolchain ("toolchains" in the action spec).
  Since anything a sandboxed action can see is part of its action key, a
  cache hit is only as good as those declarations.

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
    home/            $HOME: empty and writable (tools such as go keep their
                     caches under it); the host HOME is never used
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
                        Kernel-enforced: file writes restricted to the sandbox's
                        work/out/tmp/home, network denied unless network: true,
                        data reads limited to the sandbox, granted host_paths and
                        system trees (/bin, /sbin, /usr except /usr/local,
                        /System, /etc, /dev, Xcode's Developer dir); exec limited
                        to the sandbox, /bin, /usr/bin, /usr/lib and host_paths.
                        Metadata lookup (stat) is allowed everywhere.

  Namespace  Linux      User/mount/PID/IPC/UTS/network namespaces via clone().
                        pivot_root into the sandbox root, minimal /dev,
                        read-only rootfs; /usr, /lib*, /sbin and /etc (and
                        /bin/sh, unless a toolchain provides bin/sh) are
                        bind-mounted read-only. Full filesystem and network
                        isolation. Availability is probed once per process
                        (a user namespace that may not mount, as under
                        Ubuntu's AppArmor restriction, means Copy).

  When only Copy is available, mu prints once per process:
    mu: sandbox: kernel isolation unavailable on this host; network:false
    and filesystem confinement are NOT enforced

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

  - Seatbelt: SBPL rules deny network-outbound/inbound/bind. Network: true
    allows outbound/bind plus what macOS clients need to use it: name
    resolution (mDNSResponder) and TLS certificate verification (trustd,
    Security, and reads of the system keychains and /private/etc)
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

  Build manifests (`mu build --emit-manifest`) record "isolation" per action:
  "seatbelt", "namespace" or "copy" for pure actions, "none" for impure ones.
  For a cached action it is the isolation this host applies to it (nothing
  ran). In Go, Sandbox.Level() returns the level a sandbox achieved.

CONTINUOUS QUALIFICATION

  .github/workflows/ci.yml pins Go 1.26.2 and action revisions and runs Linux
  and macOS unit/race/vet/build gates. Native acceptance separately builds a
  static helper and requires Namespace on Linux or Seatbelt on macOS; it
  fails rather than silently accepting copy isolation. Checks cover writable
  work directories, denied host reads/writes, and denied/allowed networking.

  Run the native gate locally:
    CGO_ENABLED=0 go build -o /tmp/mu-native-helper ./internal/sandbox/testdata/native-helper
    MU_NATIVE_HELPER=/tmp/mu-native-helper MU_REQUIRE_NATIVE_SANDBOX=1 go test -count=1 -v ./internal/sandbox -run '^TestNativeSandboxAcceptance$'

  Executor-level checks (internal/dag TestSandboxNetworkEnforcement and
  TestSandboxFilesystemConfinement) build the helper with `go build` and run
  it as a pure action: network:false cannot dial a local server while
  network:true can, and host files (including ~/.ssh) can be neither read
  nor written. They skip under copy isolation.

  Host-shell unit tests explicitly use copy isolation. They are environment
  tests, not evidence of kernel enforcement. Babashka qualification and real
  PUDL integration remain separate from this Go/native matrix.
