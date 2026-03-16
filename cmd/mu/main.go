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
  version    Print the mu version`)
}
