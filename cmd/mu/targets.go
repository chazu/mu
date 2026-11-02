package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
)

const targetsSynopsis = "targets [flags] [pattern...]\n       mu targets [flags] --deps <target>\n       mu targets [flags] --rdeps <target>"

// runTargets lists the project's targets (optionally filtered by target
// patterns) or, with --deps/--rdeps, shows one target's dependency chain.
// Output is a table/tree by default, JSON with --json, Graphviz with --dot.
func runTargets(args []string) int {
	fs := flag.NewFlagSet("targets", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("targets", fs)
	deps := fs.String("deps", "", "show the dependency chain of `target` (what it depends on)")
	rdeps := fs.String("rdeps", "", "show the reverse dependency chain of `target` (what depends on it)")
	dot := fs.Bool("dot", false, "emit Graphviz DOT format")
	if code, ok := parseFlags(fs, args, targetsSynopsis); !ok {
		return code
	}
	if *deps != "" && *rdeps != "" {
		return cli.fail(exitUsage, "--deps and --rdeps are mutually exclusive")
	}
	if *cli.flagJSON && *dot {
		return cli.fail(exitUsage, "--json and --dot are mutually exclusive")
	}
	root, reverse := *deps, false
	if *rdeps != "" {
		root, reverse = *rdeps, true
	}
	patterns := fs.Args()
	if root != "" && len(patterns) > 0 {
		return cli.fail(exitUsage, "target patterns cannot be combined with --deps/--rdeps")
	}

	if code, ok := cli.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}

	byName := make(map[string]*config.Target, len(cli.Config.Targets))
	for i := range cli.Config.Targets {
		t := &cli.Config.Targets[i]
		byName[t.Name] = t
	}

	if root != "" {
		if _, ok := byName[root]; !ok {
			return cli.fail(exitFail, "unknown target %q", root)
		}
		edges := buildEdges(cli.Config.Targets, reverse)
		switch {
		case cli.JSON:
			return printGraphJSON(os.Stdout, root, edges, byName, reverse)
		case *dot:
			printGraphDOT(os.Stdout, root, edges)
		default:
			printGraphTree(os.Stdout, root, edges, byName)
		}
		return exitOK
	}

	targets, err := selectTargets(cli.Config.Targets, byName, patterns)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	switch {
	case cli.JSON:
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(targets); err != nil {
			return cli.fail(exitFail, "%v", err)
		}
	case *dot:
		printTargetsDOT(os.Stdout, targets)
	default:
		printTargetsTable(os.Stdout, targets)
	}
	return exitOK
}

// selectTargets returns the configured targets matching patterns, in config
// order; no patterns selects every target. A literal name that is not
// defined, or a wildcard that matches nothing, is an error.
func selectTargets(all []config.Target, byName map[string]*config.Target, patterns []string) ([]config.Target, error) {
	if len(patterns) == 0 {
		return all, nil
	}
	names := make([]string, 0, len(all))
	for _, t := range all {
		names = append(names, t.Name)
	}
	expanded, err := coordinator.ExpandTargetPatterns(patterns, names)
	if err != nil {
		return nil, err
	}
	want := make(map[string]bool, len(expanded))
	for _, n := range expanded {
		if _, ok := byName[n]; !ok {
			return nil, fmt.Errorf("unknown target %q", n)
		}
		want[n] = true
	}
	out := make([]config.Target, 0, len(want))
	for _, t := range all {
		if want[t.Name] {
			out = append(out, t)
		}
	}
	return out, nil
}

func printTargetsTable(w io.Writer, targets []config.Target) {
	if len(targets) == 0 {
		fmt.Fprintln(w, "No targets defined.")
		return
	}
	fmt.Fprintf(w, "%-30s %-15s %7s %5s\n", "TARGET", "TOOLCHAIN", "SOURCES", "DEPS")
	for _, t := range targets {
		name := t.Name
		if len(name) > 30 {
			name = name[:27] + "..."
		}
		fmt.Fprintf(w, "%-30s %-15s %7d %5d\n", name, t.Toolchain, len(t.Sources), len(t.Deps))
	}
}

// printTargetsDOT emits the dependency edges of the given targets.
func printTargetsDOT(w io.Writer, targets []config.Target) {
	fmt.Fprintln(w, "digraph mu {")
	fmt.Fprintln(w, `  rankdir="LR";`)
	for _, t := range targets {
		fmt.Fprintf(w, "  %q;\n", t.Name)
		deps := append([]string(nil), t.Deps...)
		sort.Strings(deps)
		for _, d := range deps {
			fmt.Fprintf(w, "  %q -> %q;\n", t.Name, d)
		}
	}
	fmt.Fprintln(w, "}")
}

// buildEdges returns a map from node → list of children to walk. When reverse
// is false, children are the target's deps. When reverse is true, children are
// targets that depend on this one.
func buildEdges(targets []config.Target, reverse bool) map[string][]string {
	edges := make(map[string][]string, len(targets))
	for _, t := range targets {
		if reverse {
			for _, d := range t.Deps {
				edges[d] = append(edges[d], t.Name)
			}
		} else {
			edges[t.Name] = append(edges[t.Name], t.Deps...)
		}
	}
	for k := range edges {
		sort.Strings(edges[k])
	}
	return edges
}

// printGraphTree prints an ASCII tree rooted at target. Cycles and revisits
// are marked so the walk terminates on bad configs.
func printGraphTree(w io.Writer, target string, edges map[string][]string, byName map[string]*config.Target) {
	fmt.Fprintln(w, target)
	walked := map[string]bool{target: true}
	walkTree(w, target, edges, byName, walked, "")
}

func walkTree(w io.Writer, node string, edges map[string][]string, byName map[string]*config.Target, onPath map[string]bool, prefix string) {
	children := edges[node]
	for i, child := range children {
		last := i == len(children)-1
		branch := "├── "
		next := "│   "
		if last {
			branch = "└── "
			next = "    "
		}

		label := child
		if t, ok := byName[child]; ok && t.Toolchain != "" {
			label = fmt.Sprintf("%s  (%s)", child, t.Toolchain)
		} else if !ok {
			label = child + "  (missing)"
		}
		if onPath[child] {
			fmt.Fprintf(w, "%s%s%s  ↺\n", prefix, branch, label)
			continue
		}
		fmt.Fprintf(w, "%s%s%s\n", prefix, branch, label)

		onPath[child] = true
		walkTree(w, child, edges, byName, onPath, prefix+next)
		delete(onPath, child)
	}
}

func printGraphDOT(w io.Writer, target string, edges map[string][]string) {
	visited := map[string]bool{}
	var stack []string
	stack = append(stack, target)
	fmt.Fprintln(w, "digraph mu {")
	fmt.Fprintln(w, `  rankdir="LR";`)
	fmt.Fprintf(w, "  %q [style=filled,fillcolor=lightblue];\n", target)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[n] {
			continue
		}
		visited[n] = true
		for _, c := range edges[n] {
			fmt.Fprintf(w, "  %q -> %q;\n", n, c)
			if !visited[c] {
				stack = append(stack, c)
			}
		}
	}
	fmt.Fprintln(w, "}")
}

func printGraphJSON(w io.Writer, target string, edges map[string][]string, byName map[string]*config.Target, reverse bool) int {
	type node struct {
		Name      string   `json:"name"`
		Toolchain string   `json:"toolchain,omitempty"`
		Children  []string `json:"children"`
		Missing   bool     `json:"missing,omitempty"`
	}
	nodes := map[string]*node{}
	var visit func(string)
	visit = func(n string) {
		if _, ok := nodes[n]; ok {
			return
		}
		entry := &node{Name: n, Children: edges[n]}
		if entry.Children == nil {
			entry.Children = []string{}
		}
		if t, ok := byName[n]; ok {
			entry.Toolchain = t.Toolchain
		} else {
			entry.Missing = true
		}
		nodes[n] = entry
		for _, c := range edges[n] {
			visit(c)
		}
	}
	visit(target)

	names := make([]string, 0, len(nodes))
	for k := range nodes {
		names = append(names, k)
	}
	sort.Strings(names)
	out := make([]*node, len(names))
	for i, n := range names {
		out[i] = nodes[n]
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"version": 1,
		"root":    target,
		"reverse": reverse,
		"nodes":   out,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "mu targets: %v\n", err)
		return exitFail
	}
	return exitOK
}
