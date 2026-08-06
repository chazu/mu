package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/dag"
	"github.com/chazu/mu/internal/scratch"
)

func runBuild(args []string) int {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("build", fs)
	jobs := fs.Int("jobs", 0, "max parallel actions (0 = NumCPU)")
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	noDiscoverCache := fs.Bool("no-discover-cache", false, "bypass the plugin discover response cache (force live discover)")
	planOnly := fs.Bool("plan", false, "show planned actions without executing")
	dryRun := fs.Bool("dry-run", false, "alias for --plan")
	emitManifest := fs.Bool("emit-manifest", false, "emit build manifest as JSON to stdout")
	expectPlanSHA256 := fs.String("expect-plan-sha256", "", "execute only if the single in-process plan has this SHA-256")
	publish := fs.Bool("publish", false, "after a successful build, publish each target's outputs as an artifact (config.publish)")
	var attach stringSliceFlag
	fs.Var(&attach, "attach", "attach a file to the published artifact as a referrer: <artifactType>=<path> (repeatable; requires --publish)")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if len(attach) > 0 && !*publish {
		return cli.fail(exitUsage, "--attach requires --publish")
	}

	if *dryRun {
		*planOnly = true
	}
	if *emitManifest && *planOnly {
		return cli.fail(exitUsage, "--emit-manifest and --plan are mutually exclusive")
	}
	if *publish && *planOnly {
		return cli.fail(exitUsage, "--publish and --plan are mutually exclusive")
	}
	if *expectPlanSHA256 != "" && *planOnly {
		return cli.fail(exitUsage, "--expect-plan-sha256 and --plan are mutually exclusive")
	}
	if *expectPlanSHA256 != "" && *publish {
		return cli.fail(exitUsage, "--expect-plan-sha256 and --publish are mutually exclusive")
	}
	if *expectPlanSHA256 != "" {
		decoded, err := hex.DecodeString(*expectPlanSHA256)
		if err != nil || len(decoded) != sha256.Size {
			return cli.fail(exitUsage, "--expect-plan-sha256 requires a 64-character hexadecimal SHA-256")
		}
	}

	targets := fs.Args()
	if len(targets) == 0 {
		return cli.fail(exitUsage, "no targets specified")
	}

	if code, ok := cli.Resolve(resolveOpts{
		NeedConfig:     true,
		NeedStore:      true,
		NoCache:        *noCache,
		ValidateConfig: true,
	}); !ok {
		return code
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	registry := coordinator.NewToolchainRegistry(cli.Store)
	home, _ := os.UserHomeDir()

	c := &coordinator.Coordinator{
		ProjectRoot:       cli.ProjectRoot,
		Config:            cli.Config,
		Store:             cli.Store,
		ToolchainRegistry: registry,
		Workers:           *jobs,
		NoDiscoverCache:   *noDiscoverCache,
		Verbose:           cli.Verbose,
	}

	// When emitting a manifest to stdout, redirect action subprocess
	// stdout to stderr so colored tofu/terraform logs don't contaminate
	// the JSON manifest downstream pipelines consume.
	if *emitManifest {
		c.SubprocessStdout = os.Stderr
	}

	if len(cli.Config.Toolchains) > 0 && cli.Store != nil {
		c.Builder = &scratch.Builder{
			Store:    cli.Store,
			Registry: registry,
			CacheDir: filepath.Join(home, ".mu", "cache"),
		}
	}

	// --plan mode: plan only, print the DAG, exit.
	if *planOnly {
		fmt.Fprintf(os.Stderr, "mu build --plan %s\n", strings.Join(targets, " "))

		plan, err := c.Plan(ctx, targets)
		if err != nil {
			return cli.fail(exitFail, "%v", err)
		}

		if cli.JSON {
			return printPlanJSON(plan, targets)
		}
		return printPlanHuman(plan.Graph, targets)
	}

	// Normal build: Plan + Execute.
	fmt.Fprintf(os.Stderr, "mu build %s\n", strings.Join(targets, " "))
	fmt.Fprintln(os.Stderr, "  building...")

	start := time.Now()
	var validate func(*coordinator.PlanResult) error
	if *expectPlanSHA256 != "" {
		validate = func(plan *coordinator.PlanResult) error {
			return validateExpectedPlan(plan, targets, *expectPlanSHA256)
		}
	}
	result, err := c.BuildValidated(ctx, targets, validate)
	elapsed := time.Since(start)

	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}

	if *emitManifest {
		manifest := coordinator.NewManifest(result, result.ExecResult, result.Targets, elapsed)
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if encErr := enc.Encode(manifest); encErr != nil {
			return cli.fail(exitFail, "encoding manifest: %v", encErr)
		}
	}

	if cli.JSON && !*emitManifest {
		printBuildJSON(result, elapsed)
	}

	if result.Failed > 0 {
		for _, s := range result.ExecResult.Failed {
			fmt.Fprintf(os.Stderr, "  \u2717 %s: %v\n", s.ID, s.Err)
		}
		fmt.Fprintf(os.Stderr, "  \u2717 %d failed, %d cancelled\n", result.Failed, result.Cancelled)
		return exitFail
	}

	total := result.Completed + result.Cached
	fmt.Fprintf(os.Stderr, "  \u2713 %d completed (%d cached), %d failed in %.1fs\n",
		total, result.Cached, result.Failed, elapsed.Seconds())

	if *publish {
		if code := publishTargets(ctx, cli, result, targets, attach); code != exitOK {
			return code
		}
	}
	return exitOK
}

func validateExpectedPlan(plan *coordinator.PlanResult, targets []string, expected string) error {
	for _, identity := range plan.Plugins {
		if identity.Digest == "" {
			return fmt.Errorf("exact plan contains mutable command plugin %q; use a content-addressed script, URL, or digest", identity.Name)
		}
	}
	actual, err := planJSONDigest(plan, targets)
	if err != nil {
		return fmt.Errorf("encoding exact plan identity: %w", err)
	}
	if subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) != 1 {
		return fmt.Errorf("exact plan SHA-256 mismatch: expected %s, planned %s", expected, actual)
	}
	return nil
}

type planAction struct {
	ID                string            `json:"id"`
	ActionKey         string            `json:"action_key"`
	Command           []string          `json:"command"`
	Body              []any             `json:"body,omitempty"`
	EweDigest         string            `json:"ewe_digest,omitempty"`
	Inputs            map[string]string `json:"inputs"`
	Outputs           []string          `json:"outputs"`
	DependsOn         []string          `json:"depends_on"`
	Env               map[string]string `json:"env,omitempty"`
	SealedInputs      map[string]string `json:"sealed_inputs,omitempty"`
	SealedInputModes  map[string]string `json:"sealed_input_modes,omitempty"`
	SealedOutputs     map[string]string `json:"sealed_outputs,omitempty"`
	SealedOutputModes map[string]string `json:"sealed_output_modes,omitempty"`
	Network           bool              `json:"network,omitempty"`
	WorkDir           string            `json:"work_dir,omitempty"`
	Impure            bool              `json:"impure,omitempty"`
	TimeoutS          int               `json:"timeout_s,omitempty"`
	Retries           int               `json:"retries,omitempty"`
	RetryBackoffMs    int               `json:"retry_backoff_ms,omitempty"`
	Toolchain         map[string]string `json:"toolchain,omitempty"`
	Sources           []string          `json:"sources,omitempty"`
}

type planJSONDocument struct {
	Version    int                          `json:"version"`
	PlanSHA256 string                       `json:"plan_sha256,omitempty"`
	Targets    []string                     `json:"targets"`
	Plugins    []coordinator.PluginIdentity `json:"plugins"`
	Actions    []planAction                 `json:"actions"`
	Summary    map[string]int               `json:"summary"`
}

func newPlanJSONDocument(plan *coordinator.PlanResult, targets []string) planJSONDocument {
	actions := plan.Graph.Actions()
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
		toolchain := make(map[string]string, len(a.Toolchain))
		for name, digest := range a.Toolchain {
			toolchain[name] = digest.String()
		}
		eweDigest := ""
		if !a.EweRef.IsZero() {
			eweDigest = a.EweRef.String()
		}
		out = append(out, planAction{
			ID: a.ID, ActionKey: dag.ComputeActionKey(a).Digest.String(),
			Command: a.Command, Body: a.Body, EweDigest: eweDigest,
			Inputs: inputs, Outputs: outputs, DependsOn: deps, Env: a.Env,
			SealedInputs: a.SealedInputs, SealedInputModes: a.SealedInputModes,
			SealedOutputs: a.SealedOutputs, SealedOutputModes: a.SealedOutputModes,
			Network: a.Network, WorkDir: a.WorkDir, Impure: a.Impure,
			TimeoutS: a.TimeoutS, Retries: a.Retries, RetryBackoffMs: a.RetryBackoffMs,
			Toolchain: toolchain, Sources: a.Sources,
		})
	}

	plugins := append([]coordinator.PluginIdentity(nil), plan.Plugins...)
	if plugins == nil {
		plugins = []coordinator.PluginIdentity{}
	}
	return planJSONDocument{
		Version: 2, Targets: targets, Plugins: plugins, Actions: out,
		Summary: map[string]int{"total": len(out)},
	}
}

func planJSONDigest(plan *coordinator.PlanResult, targets []string) (string, error) {
	document := newPlanJSONDocument(plan, targets)
	encoded, err := json.Marshal(document)
	if err != nil {
		return "", err
	}
	var canonical any
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.UseNumber()
	if err := decoder.Decode(&canonical); err != nil {
		return "", err
	}
	payload, err := json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

// printPlanJSON emits the planned action DAG as JSON to stdout. plan_sha256 is
// the digest of the same document with that self-referential field omitted.
func printPlanJSON(plan *coordinator.PlanResult, targets []string) int {
	document := newPlanJSONDocument(plan, targets)
	digest, err := planJSONDigest(plan, targets)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu build: encoding plan identity: %v\n", err)
		return 1
	}
	document.PlanSHA256 = digest

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(document); err != nil {
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
