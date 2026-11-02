package main

import (
	"fmt"
	"io"
	"os"
	"time"

	"github.com/chazu/mu/internal/sandbox"
	"github.com/chazu/mu/internal/staging"
)

// muVersion is the CLI version string, kept in sync with the release tag.
const muVersion = "v0.3.5"

func main() {
	if sandbox.IsSandboxInit() {
		sandbox.RunInit()
		return
	}

	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(exitUsage)
	}
	switch os.Args[1] {
	case "version", "guide", "help", "-h", "-help", "--help":
	default:
		startStagingSweep()
	}
	os.Exit(run(os.Args[1], os.Args[2:]))
}

// run dispatches one mu command and returns its exit code. "help <command>
// [<subcommand>]" re-dispatches with -h appended, so a command's help is the
// same usage text its own -h prints.
func run(cmd string, args []string) int {
	switch cmd {
	case "help", "-h", "-help", "--help":
		if len(args) > 0 && !isHelpArg(args[0]) {
			return run(args[0], append(append([]string(nil), args[1:]...), "-h"))
		}
		usage(os.Stdout)
		return exitOK
	case "build", "b":
		return runBuild(args)
	case "scratch":
		return runScratch(args)
	case "cache":
		return runCache(args)
	case "targets", "t":
		return runTargets(args)
	case "target", "graph":
		fmt.Fprintf(os.Stderr, "mu: %q was replaced by \"mu targets\" (list: mu targets; dependency chain: mu targets --deps/--rdeps <target>)\n", cmd)
		return exitUsage
	case "plugin", "p":
		return runPlugin(args)
	case "observe":
		return runObserve(args)
	case "verify":
		return runVerify(args)
	case "guide":
		return runGuide(args)
	case "version":
		fmt.Println("mu " + muVersion)
		return exitOK
	default:
		fmt.Fprintf(os.Stderr, "mu: unknown command %q\n\n", cmd)
		usage(os.Stderr)
		return exitUsage
	}
}

// startStagingSweep reclaims staging that crashed mu processes stranded. It
// is rate-limited to once an hour and best effort. It runs synchronously: a
// background goroutine would be killed by short commands after stamping,
// silently skipping the sweep for a whole interval. MU_NO_STAGING_SWEEP=1
// disables it (for example, to inspect a crashed run's leftovers).
func startStagingSweep() {
	if os.Getenv("MU_NO_STAGING_SWEEP") == "1" {
		return
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	staging.MaybeSweep(home, time.Now())
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `usage: mu <command> [arguments]

Commands:
  build (b)  Build one or more targets
  scratch    Build toolchains from scratch (override with MU_SCRATCH)
  cache      Inspect the local CAS cache
  targets    List targets and their dependency chains (alias t)
  plugin (p) List and inspect plugins
  observe    Check current state of targets (drift detection)
  verify     Re-hash CAS blobs, check schema namespace conventions, report issues
  guide      Quick-reference guides for mu features
  version    Print the mu version
  help       Show help for mu or a command (mu help <command> [<subcommand>])

Flags may come before or after positional arguments; "--" ends flags.`)
}
