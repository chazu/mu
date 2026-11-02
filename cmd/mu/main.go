package main

import (
	"fmt"
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
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "version", "guide", "help", "-h", "--help":
	default:
		startStagingSweep()
	}
	switch os.Args[1] {
	case "build", "b":
		os.Exit(runBuild(os.Args[2:]))
	case "scratch":
		os.Exit(runScratch(os.Args[2:]))
	case "cache":
		os.Exit(runCache(os.Args[2:]))
	case "target", "t":
		os.Exit(runTarget(os.Args[2:]))
	case "graph":
		os.Exit(runGraph(os.Args[2:]))
	case "plugin", "p":
		os.Exit(runPlugin(os.Args[2:]))
	case "observe":
		os.Exit(runObserve(os.Args[2:]))
	case "verify":
		os.Exit(runVerify(os.Args[2:]))
	case "guide":
		os.Exit(runGuide(os.Args[2:]))
	case "version":
		fmt.Println("mu " + muVersion)
	default:
		usage()
		os.Exit(2)
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

func usage() {
	fmt.Fprintln(os.Stderr, `usage: mu <command> [arguments]

Commands:
  build (b)  Build one or more targets
  scratch    Build toolchains from scratch
  cache      Inspect the local CAS cache
  target (t) List and inspect targets
  graph      Show the dependency chain of a target
  plugin (p) List and inspect plugins
  observe    Check current state of targets (drift detection)
  verify     Re-hash CAS blobs, check schema namespace conventions, report issues
  guide      Quick-reference guides for mu features
  version    Print the mu version`)
}
