package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"

	"github.com/chau/mu/internal/config"
)

func runGraph(args []string) int {
	fs := flag.NewFlagSet("graph", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("graph", fs)
	reverse := fs.Bool("reverse", false, "show what depends on the target instead of what it depends on")
	dot := fs.Bool("dot", false, "emit Graphviz DOT format")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}

	rest := fs.Args()
	if len(rest) != 1 {
		return cli.fail(exitUsage, "usage: mu graph <target>")
	}
	target := rest[0]

	if code, ok := cli.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return code
	}

	byName := make(map[string]*config.Target, len(cli.Config.Targets))
	for i := range cli.Config.Targets {
		t := &cli.Config.Targets[i]
		byName[t.Name] = t
	}
	if _, ok := byName[target]; !ok {
		return cli.fail(exitFail, "unknown target %q", target)
	}

	edges := buildEdges(cli.Config.Targets, *reverse)

	if cli.JSON {
		return printGraphJSON(target, edges, byName, *reverse)
	}
	if *dot {
		printGraphDOT(target, edges)
		return exitOK
	}
	printGraphTree(target, edges, byName)
	return exitOK
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
func printGraphTree(target string, edges map[string][]string, byName map[string]*config.Target) {
	fmt.Println(target)
	walked := map[string]bool{target: true}
	walkTree(target, edges, byName, walked, "")
}

func walkTree(node string, edges map[string][]string, byName map[string]*config.Target, onPath map[string]bool, prefix string) {
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
			fmt.Printf("%s%s%s  ↺\n", prefix, branch, label)
			continue
		}
		fmt.Printf("%s%s%s\n", prefix, branch, label)

		onPath[child] = true
		walkTree(child, edges, byName, onPath, prefix+next)
		delete(onPath, child)
	}
}

func printGraphDOT(target string, edges map[string][]string) {
	visited := map[string]bool{}
	var stack []string
	stack = append(stack, target)
	fmt.Println("digraph mu {")
	fmt.Println(`  rankdir="LR";`)
	fmt.Printf("  %q [style=filled,fillcolor=lightblue];\n", target)
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if visited[n] {
			continue
		}
		visited[n] = true
		for _, c := range edges[n] {
			fmt.Printf("  %q -> %q;\n", n, c)
			if !visited[c] {
				stack = append(stack, c)
			}
		}
	}
	fmt.Println("}")
}

func printGraphJSON(target string, edges map[string][]string, byName map[string]*config.Target, reverse bool) int {
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

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(map[string]any{
		"version": 1,
		"root":    target,
		"reverse": reverse,
		"nodes":   out,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "mu graph: %v\n", err)
		return exitFail
	}
	return exitOK
}
