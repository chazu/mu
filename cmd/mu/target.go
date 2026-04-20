package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
)

func runTarget(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, `usage: mu target <command>

Commands:
  list      List targets defined in mu.json`)
		return exitUsage
	}

	switch args[0] {
	case "list":
		return runTargetList(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "mu target: unknown command %q\n", args[0])
		return exitUsage
	}
}

func runTargetList(args []string) int {
	fs := flag.NewFlagSet("target list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	ctx := newCLIContext("target list", fs)
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if code, ok := ctx.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}

	if ctx.JSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(ctx.Config.Targets)
		return exitOK
	}

	if len(ctx.Config.Targets) == 0 {
		fmt.Println("No targets defined.")
		return exitOK
	}

	fmt.Printf("%-30s %-15s %7s\n", "TARGET", "TOOLCHAIN", "SOURCES")
	for _, t := range ctx.Config.Targets {
		name := t.Name
		if len(name) > 30 {
			name = name[:27] + "..."
		}
		fmt.Printf("%-30s %-15s %7d\n", name, t.Toolchain, len(t.Sources))
	}
	return exitOK
}
