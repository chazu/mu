package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "build":
		os.Exit(runBuild(os.Args[2:]))
	case "scratch":
		os.Exit(runScratch(os.Args[2:]))
	case "cache":
		os.Exit(runCache(os.Args[2:]))
	case "target":
		os.Exit(runTarget(os.Args[2:]))
	case "plugin":
		os.Exit(runPlugin(os.Args[2:]))
	case "observe":
		os.Exit(runObserve(os.Args[2:]))
	case "verify":
		os.Exit(runVerify(os.Args[2:]))
	case "guide":
		os.Exit(runGuide(os.Args[2:]))
	case "migrate":
		os.Exit(runMigrate(os.Args[2:]))
	case "version":
		fmt.Println("mu v0.1.0")
	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `usage: mu <command> [arguments]

Commands:
  build      Build one or more targets
  scratch    Build toolchains from scratch (override with MU_SCRATCH)
  cache      Inspect the local CAS cache
  target     List and inspect targets
  plugin     List and inspect plugins
  observe    Check current state of targets (drift detection)
  verify     Re-hash CAS blobs and report corruption
  guide      Quick-reference guides for mu features
  migrate    Convert mu.json to mu.cue
  version    Print the mu version`)
}
