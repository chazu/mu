mu guide sandbox — hermetic execution environments

WHICH ACTIONS RUN SANDBOXED

  Every pure action (impure: false and no sealed_outputs) runs in the
  sandbox. Impure actions run directly on the host, with the host's
  filesystem and network, and are never cached: converge steps, lint fixes,
  and every action with sealed_outputs (which is forced impure). The plan
  (`mu build --plan --json`) marks each sandboxed action "sandboxed": true.

  A pure action that needs a tool outside the system directories names a
  scratch toolchain ("toolchains" in the action spec) or, when the tool
  cannot be one, grants its host path in "host_paths". Toolchains exist only
  inside the sandbox: an impure action that names one fails planning.

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
  apart from the platform's system directories (see ISOLATION LEVELS).
  Under copy isolation none of this is enforced.

  Environment: the declared env (empty when absent), the env_passthrough
  variables that are set on the host, $MU_OUT and env-mode sealed inputs;
  never the rest of the host environment. HOME and TMPDIR are always the
  sandbox's own, overriding any declared or passed-through value. PATH is
  bin/ followed by the declared PATH, or by /usr/bin:/bin when none is
  declared. A passed-through value naming a host path (GOCACHE,
  SSH_AUTH_SOCK) is usable only if that path is visible. Under Linux
  namespaces the sandbox root is "/", so these paths read /work, /out, /tmp,
  /home and /sealed.

  After the command exits, each declared output is taken from out/ or the
  working directory and copied back to the host work directory. Outputs
  must be relative paths inside the work directory; absolute or escaping
  outputs are an error for sandboxed actions.

WHAT THE ACTION KEY COVERS

  The action key (`mu guide cache`) hashes what the plan declares: each
  input's digest and its path in work/, every toolchain file's digest, mode
  and symlink, the network flag, and each host_paths grant. A granted file
  is hashed by content at plan time; a granted directory contributes only
  its path, so its contents are trusted, not hashed. Not in the key: the
  system directories, the host values of env_passthrough variables (only
  their names), whatever a network: true action downloads, and, under copy
  isolation, any undeclared host file the command reads. A cache hit is only
  as good as those declarations.

ISOLATION LEVELS

  mu auto-detects the strongest isolation available on the platform:

  Level      Platform   Enforcement
  ─────      ────────   ───────────
  Copy       all        Temp directory with restricted PATH/env. No kernel
                        enforcement: relative paths land in the sandbox, but
                        the command can read and write any host path the user
                        can and use the network.

  Seatbelt   macOS      sandbox-exec with a deny-default SBPL profile.
                        Kernel-enforced: file writes restricted to the sandbox's
                        work/out/tmp/home, network denied unless network: true,
                        data reads limited to the sandbox, granted host_paths and
                        system trees (/bin, /sbin, /usr except /usr/local,
                        /System, /etc, /dev, Xcode's Developer dir); exec limited
                        to the sandbox, /bin, /usr/bin, /usr/lib and host_paths.
                        Metadata lookup (stat) is allowed everywhere.

  Namespace  Linux      User/mount/PID/IPC/UTS namespaces via clone(), plus a
                        network namespace unless network: true. pivot_root
                        into the sandbox root (it becomes "/"), minimal /dev,
                        private /proc. /usr, /lib*, /sbin and /etc are
                        bind-mounted read-only; /bin is the toolchain bin/
                        (holding the host /bin/sh unless a toolchain provides
                        bin/sh); host_paths are bind-mounted read-only at
                        their own paths. Everything but /work, /out, /tmp,
                        /home and /dev is read-only. Availability is probed
                        once per process (a user namespace that may not
                        mount, as under Ubuntu's AppArmor restriction, means
                        Copy). Untested on Linux so far: this code builds for
                        Linux but has not yet been run on a Linux host.

  When only Copy is available, mu prints once per process:
    mu: sandbox: kernel isolation unavailable on this host; network:false
    and filesystem confinement are NOT enforced

WHAT'S ENFORCED

  Property            Copy    Seatbelt   Namespace
  ────────            ────    ────────   ─────────
  Restricted PATH     yes     yes        yes
  Restricted TMPDIR   yes     yes        yes
  File write deny     no      yes        yes
  File read deny      no      partial*   yes
  Network deny        no      yes        yes
  PID isolation       no      no         yes
  UID isolation       no      no         yes

  * Seatbelt hides file contents outside the sandbox, granted host_paths and
    the system trees, but stat() of any host path still succeeds.

NETWORK FLAG

  Pure actions declare network: true/false (default false). With Seatbelt or
  Namespace isolation, network: false is enforced by the kernel. Impure
  actions run on the host, where the flag restricts nothing; it only enables
  retries.

  - Seatbelt: SBPL rules deny network-outbound/inbound/bind. Network: true
    allows outbound/bind plus what macOS clients need to use it: name
    resolution (mDNSResponder) and TLS certificate verification (trustd,
    Security, and reads of the system keychains and /private/etc)
  - Namespace: CLONE_NEWNET creates isolated network stack (loopback down);
    network: true shares the host's network namespace
  - Copy: the network flag is not enforced

PLATFORM NOTES

  macOS Seatbelt:
    sandbox-exec is deprecated since ~2016 but still used by Apple internally
    (mDNSResponder, system daemons) and by Nix, Bazel, Chromium. Detection
    only checks that sandbox-exec is on PATH: on a host that refuses to apply
    the profile, sandboxed actions fail rather than falling back to Copy.

  Linux Namespaces:
    Requires unprivileged user namespaces. Check:
      cat /proc/sys/kernel/unprivileged_userns_clone
    Value "0" means disabled, and mu uses Copy. Ubuntu 24.04+ may also
    restrict them via AppArmor, which the probe detects.

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
  ran). `mu build --plan --json` marks sandboxed actions "sandboxed": true.
  In Go, Sandbox.Level() returns the level a sandbox achieved.

CONTINUOUS QUALIFICATION

  .github/workflows/ci.yml pins Go 1.26.2 and action revisions and runs Linux
  and macOS unit/race/vet/build gates. Native acceptance separately builds a
  static helper and requires Namespace on Linux or Seatbelt on macOS; it
  fails rather than silently accepting copy isolation. Checks cover writable
  work directories, denied host reads/writes, and denied/allowed networking.
  So far these checks have run on macOS only (see ISOLATION LEVELS).

  Run the native gate locally:
    CGO_ENABLED=0 go build -o /tmp/mu-native-helper ./internal/sandbox/testdata/native-helper
    MU_NATIVE_HELPER=/tmp/mu-native-helper MU_REQUIRE_NATIVE_SANDBOX=1 go test -count=1 -v ./internal/sandbox -run '^TestNativeSandboxAcceptance$'

  Executor-level checks (internal/dag TestSandboxNetworkEnforcement and
  TestSandboxFilesystemConfinement) build the helper with `go build` and run
  it as a pure action: network:false cannot dial a local server while
  network:true can, and host files (including ~/.ssh) can be neither read
  nor written. They skip under copy isolation.

  Host-shell unit tests explicitly use copy isolation. They are environment
  tests, not evidence of kernel enforcement. Babashka qualification remains
  separate from this Go/native matrix.
