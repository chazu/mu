package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator"
	"github.com/chau/mu/internal/dag"
	"github.com/chau/mu/internal/scratch"
)

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	jobs := fs.Int("jobs", 0, "max parallel actions (0 = NumCPU)")
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	noDiscoverCache := fs.Bool("no-discover-cache", false, "bypass the plugin discover response cache (force live discover)")
	configFile := fs.String("config", "", "path to mu.json (default: discover by walking up)")
	planOnly := fs.Bool("plan", false, "show planned actions without executing")
	dryRun := fs.Bool("dry-run", false, "alias for --plan")
	jsonOut := fs.Bool("json", false, "output as JSON")
	emitManifest := fs.Bool("emit-manifest", false, "emit build manifest as JSON to stdout")
	_ = fs.Bool("verbose", false, "show plugin I/O")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *dryRun {
		*planOnly = true
	}

	if *emitManifest && *planOnly {
		fmt.Fprintln(os.Stderr, "mu build: --emit-manifest and --plan are mutually exclusive")
		return 2
	}

	targets := fs.Args()
	if len(targets) == 0 {
		fmt.Fprintln(os.Stderr, "mu build: no targets specified")
		return 2
	}

	// Find project root and load config.
	var projectRoot string
	if *configFile != "" {
		// Explicit config: project root is the directory containing the config file.
		absConfig, err := filepath.Abs(*configFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
			return 2
		}
		projectRoot = filepath.Dir(absConfig)
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
			return 2
		}
		projectRoot, err = config.FindProjectRoot(cwd)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
			return 2
		}
	}

	// Load and validate config.
	cfg, err := config.Load(projectRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
		return 2
	}
	if err := config.Validate(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "mu build: %v\n", err)
		return 2
	}

	// Create CAS store. Honors cfg.Cache (tiered composition) when set;
	// otherwise falls back to a single local disk cache at ~/.mu/cache.
	var store cas.Store
	if !*noCache {
		s, err := buildCacheStore(cfg.Cache, false)
		if err != nil {
			fmt.Fprintf(os.Stderr, "mu build: creating cache store: %v\n", err)
			return 2
		}
		store = s
	}

	// Build with cancellation on SIGINT.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	registry := coordinator.NewToolchainRegistry(store)
	home, _ := os.UserHomeDir()

	c := &coordinator.Coordinator{
		ProjectRoot:       projectRoot,
		Config:            cfg,
		Store:             store,
		ToolchainRegistry: registry,
		Workers:           *jobs,
		NoDiscoverCache:   *noDiscoverCache,
	}

	if len(cfg.Toolchains) > 0 && store != nil {
		c.Builder = &scratch.Builder{
			Store:    store,
			Registry: registry,
			CacheDir: filepath.Join(home, ".mu", "cache"),
		}
	}

	// --plan mode: plan only, print the DAG, exit.
	if *planOnly {
		fmt.Fprintf(os.Stderr, "mu build --plan %s\n", strings.Join(targets, " "))

		plan, err := c.Plan(ctx, targets)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  error: %v\n", err)
			return 1
		}

		if *jsonOut {
			return printPlanJSON(plan.Graph, targets)
		}
		return printPlanHuman(plan.Graph, targets)
	}

	// Normal build: Plan + Execute.
	fmt.Fprintf(os.Stderr, "mu build %s\n", strings.Join(targets, " "))
	fmt.Fprintln(os.Stderr, "  building...")

	start := time.Now()
	result, err := c.Build(ctx, targets)
	elapsed := time.Since(start)

	if err != nil {
		fmt.Fprintf(os.Stderr, "  error: %v\n", err)
		return 1
	}

	if *emitManifest {
		manifest := coordinator.NewManifest(result, result.ExecResult, result.Targets, elapsed)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(manifest); encErr != nil {
			fmt.Fprintf(os.Stderr, "mu build: encoding manifest: %v\n", encErr)
			return 1
		}
	}

	if *jsonOut && !*emitManifest {
		printBuildJSON(result, elapsed)
	}

	if result.Failed > 0 {
		fmt.Fprintf(os.Stderr, "  \u2717 %d failed, %d cancelled\n", result.Failed, result.Cancelled)
		return 1
	}

	total := result.Completed + result.Cached
	fmt.Fprintf(os.Stderr, "  \u2713 %d completed (%d cached), %d failed in %.1fs\n",
		total, result.Cached, result.Failed, elapsed.Seconds())
	return 0
}

// printPlanJSON emits the planned action DAG as JSON to stdout.
func printPlanJSON(g *dag.Graph, targets []string) int {
	type planAction struct {
		ID        string            `json:"id"`
		Command   []string          `json:"command"`
		Inputs    map[string]string `json:"inputs"`
		Outputs   []string          `json:"outputs"`
		DependsOn []string          `json:"depends_on"`
		Env       map[string]string `json:"env,omitempty"`
		Network   bool              `json:"network,omitempty"`
		WorkDir   string            `json:"work_dir,omitempty"`
	}

	actions := g.Actions()
	out := make([]planAction, 0, len(actions))
	for _, a := range actions {
		inputs := make(map[string]string, len(a.Inputs))
		for k, v := range a.Inputs {
			inputs[k] = v.String()
		}
		deps := a.DependsOn
		if deps == nil {
			deps = []string{}
		}
		outputs := a.Outputs
		if outputs == nil {
			outputs = []string{}
		}
		out = append(out, planAction{
			ID:        a.ID,
			Command:   a.Command,
			Inputs:    inputs,
			Outputs:   outputs,
			DependsOn: deps,
			Env:       a.Env,
			Network:   a.Network,
			WorkDir:   a.WorkDir,
		})
	}

	plan := map[string]any{
		"version": 1,
		"targets": targets,
		"actions": out,
		"summary": map[string]int{
			"total": len(out),
		},
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(plan); err != nil {
		fmt.Fprintf(os.Stderr, "mu build: encoding plan: %v\n", err)
		return 1
	}
	return 0
}

// printPlanHuman prints the planned action DAG in human-readable format to stdout.
func printPlanHuman(g *dag.Graph, targets []string) int {
	actions := g.Actions()
	fmt.Printf("  planned %d actions for %d targets\n\n", len(actions), len(targets))

	for _, a := range actions {
		fmt.Printf("  %s\n", a.ID)
		fmt.Printf("    command:  %s\n", strings.Join(a.Command, " "))
		if len(a.Inputs) > 0 {
			names := make([]string, 0, len(a.Inputs))
			for k := range a.Inputs {
				names = append(names, k)
			}
			fmt.Printf("    inputs:   %s\n", strings.Join(names, ", "))
		}
		if len(a.Outputs) > 0 {
			fmt.Printf("    outputs:  %s\n", strings.Join(a.Outputs, ", "))
		}
		if len(a.DependsOn) > 0 {
			fmt.Printf("    depends:  %s\n", strings.Join(a.DependsOn, ", "))
		}
		if a.Network {
			fmt.Printf("    network:  true\n")
		}
		if a.WorkDir != "" {
			fmt.Printf("    work_dir: %s\n", a.WorkDir)
		}
		fmt.Println()
	}
	return 0
}

// printBuildJSON emits a structured build summary to stdout.
func printBuildJSON(result *coordinator.BuildResult, elapsed time.Duration) {
	summary := map[string]any{
		"version":   1,
		"completed": result.Completed,
		"cached":    result.Cached,
		"failed":    result.Failed,
		"cancelled": result.Cancelled,
		"duration":  elapsed.Seconds(),
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(summary)
}
