package main

import (
	"fmt"
	"os"

	"github.com/chazu/mu/internal/sandbox"
)

// muVersion is the CLI version string, kept in sync with the release tag.
const muVersion = "v0.3.2"

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

func usage() {
	fmt.Fprintln(os.Stderr, `usage: mu <command> [arguments]

Commands:
  build (b)  Build one or more targets
  scratch    Build toolchains from scratch (override with MU_SCRATCH)
  cache      Inspect the local CAS cache
  target (t) List and inspect targets
  graph      Show the dependency chain of a target
  plugin (p) List and inspect plugins
  observe    Check current state of targets (drift detection)
  verify     Re-hash CAS blobs, check schema namespace conventions, report issues
  guide      Quick-reference guides for mu features
  version    Print the mu version`)
}
