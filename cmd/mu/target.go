package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/chau/mu/internal/config"
)

func runTarget(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: mu target <command>

Commands:
  list      List targets defined in mu.json`)
		return 2
	}

	switch args[0] {
	case "list":
		return runTargetList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mu target: unknown command %q\n", args[0])
		return 2
	}
}

func runTargetList(args []string) int {
	fs := flag.NewFlagSet("target list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFile := fs.String("config", "", "path to mu.json")
	jsonOut := fs.Bool("json", false, "output as JSON")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	var projectRoot string
	if *configFile != "" {
		absConfig, err := filepath.Abs(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu target list: %v\n", err)
			return 2
		}
		projectRoot = filepath.Dir(absConfig)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu target list: %v\n", err)
			return 2
		}
		projectRoot, err = config.FindProjectRoot(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu target list: %v\n", err)
			return 2
		}
	}

	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu target list: %v\n", err)
		return 2
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(cfg.Targets)
		return 0
	}

	if len(cfg.Targets) == 0 {
		fmt.Println("No targets defined.")
		return 0
	}

	fmt.Printf("%-30s %-15s %7s\n", "TARGET", "TOOLCHAIN", "SOURCES")
	for _, t := range cfg.Targets {
		name := t.Name
		if len(name) > 30 {
			name = name[:27] + "..."
		}
		fmt.Printf("%-30s %-15s %7d\n", name, t.Toolchain, len(t.Sources))
	}
	return 0
}
