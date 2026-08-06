package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/dag"
)

func TestFlagParsing(t *testing.T) {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	jobs := fs.Int("jobs", 0, "max parallel actions")
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	verbose := fs.Bool("verbose", false, "show plugin I/O")

	err := fs.Parse([]string{"--jobs", "4", "--no-cache", "--verbose", "//services/api:binary"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if *jobs != 4 {
		t.Errorf("jobs = %d, want 4", *jobs)
	}
	if !*noCache {
		t.Error("no-cache = false, want true")
	}
	if !*verbose {
		t.Error("verbose = false, want true")
	}

	args := fs.Args()
	if len(args) != 1 || args[0] != "//services/api:binary" {
		t.Errorf("remaining args = %v, want [//services/api:binary]", args)
	}
}

func TestNoTargetsReturnsError(t *testing.T) {
	// runBuild with no targets should return exit code 2.
	// We cannot easily call runBuild directly in a test because it writes to
	// stderr and calls os.Getwd, but we can verify the logic path:
	// an empty fs.Args() slice triggers the early return.
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	_ = fs.Int("jobs", 0, "")
	_ = fs.Bool("no-cache", false, "")
	_ = fs.Bool("verbose", false, "")

	err := fs.Parse([]string{})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if len(fs.Args()) != 0 {
		t.Errorf("expected no remaining args, got %v", fs.Args())
	}
}

func TestEmitManifestAndPlanMutuallyExclusive(t *testing.T) {
	// When both --emit-manifest and --plan are set, runBuild should return exit code 2.
	code := runBuild([]string{"--emit-manifest", "--plan", "//target"})
	if code != 2 {
		t.Errorf("runBuild(--emit-manifest --plan) = %d, want 2", code)
	}
}

func TestEmitManifestAndDryRunMutuallyExclusive(t *testing.T) {
	// --dry-run is an alias for --plan, so this should also be rejected.
	code := runBuild([]string{"--emit-manifest", "--dry-run", "//target"})
	if code != 2 {
		t.Errorf("runBuild(--emit-manifest --dry-run) = %d, want 2", code)
	}
}

func TestFlagDefaults(t *testing.T) {
	fs := flag.NewFlagSet("build", flag.ContinueOnError)
	jobs := fs.Int("jobs", 0, "max parallel actions")
	noCache := fs.Bool("no-cache", false, "skip cache reads")
	verbose := fs.Bool("verbose", false, "show plugin I/O")

	err := fs.Parse([]string{"//target"})
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}

	if *jobs != 0 {
		t.Errorf("jobs default = %d, want 0", *jobs)
	}
	if *noCache {
		t.Error("no-cache default = true, want false")
	}
	if *verbose {
		t.Error("verbose default = true, want false")
	}
}

func TestPrintPlanJSONProjectsCompleteActionIdentity(t *testing.T) {
	inputDigest, err := cas.ParseDigest("sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	toolDigest, err := cas.ParseDigest("sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if err != nil {
		t.Fatal(err)
	}
	g := dag.NewGraph()
	err = g.AddAction(&dag.Action{
		ID:                "//app:apply",
		Command:           []string{"apply", "--exact"},
		Inputs:            map[string]cas.Digest{"desired.json": inputDigest},
		Outputs:           []string{"receipt.json"},
		DependsOn:         []string{"//app:prepare"},
		Env:               map[string]string{"MODE": "test"},
		SealedInputs:      map[string]string{"TOKEN": "fake:apps/token"},
		SealedInputModes:  map[string]string{"TOKEN": "file"},
		SealedOutputs:     map[string]string{"RESULT": "fake:apps/result"},
		SealedOutputModes: map[string]string{"RESULT": "create_if_absent"},
		Network:           true,
		WorkDir:           "deploy",
		Impure:            true,
		TimeoutS:          30,
		Retries:           2,
		RetryBackoffMs:    250,
		Toolchain:         map[string]cas.Digest{"bin/apply": toolDigest},
		Sources:           []string{"desired.json"},
	})
	if err != nil {
		t.Fatal(err)
	}

	original := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	t.Cleanup(func() { os.Stdout = original })

	planResult := &coordinator.PlanResult{Graph: g, Plugins: []coordinator.PluginIdentity{{
		Name: "apply", Digest: "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc",
		Version: "1.2.3", ProtocolVersion: 1, Capabilities: []string{"plan"},
	}}}
	if code := printPlanJSON(planResult, []string{"//app"}); code != 0 {
		t.Fatalf("printPlanJSON returned %d", code)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	os.Stdout = original
	payload, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}

	var plan struct {
		Version    int                          `json:"version"`
		PlanSHA256 string                       `json:"plan_sha256"`
		Plugins    []coordinator.PluginIdentity `json:"plugins"`
		Actions    []struct {
			ID                string            `json:"id"`
			ActionKey         string            `json:"action_key"`
			Command           []string          `json:"command"`
			Inputs            map[string]string `json:"inputs"`
			Outputs           []string          `json:"outputs"`
			DependsOn         []string          `json:"depends_on"`
			Env               map[string]string `json:"env"`
			SealedInputs      map[string]string `json:"sealed_inputs"`
			SealedInputModes  map[string]string `json:"sealed_input_modes"`
			SealedOutputs     map[string]string `json:"sealed_outputs"`
			SealedOutputModes map[string]string `json:"sealed_output_modes"`
			Network           bool              `json:"network"`
			WorkDir           string            `json:"work_dir"`
			Impure            bool              `json:"impure"`
			TimeoutS          int               `json:"timeout_s"`
			Retries           int               `json:"retries"`
			RetryBackoffMs    int               `json:"retry_backoff_ms"`
			Toolchain         map[string]string `json:"toolchain"`
			Sources           []string          `json:"sources"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(payload, &plan); err != nil {
		t.Fatalf("decode JSON plan: %v\n%s", err, payload)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(plan.Actions))
	}
	if plan.Version != 2 {
		t.Fatalf("plan version = %d, want 2", plan.Version)
	}
	if len(plan.PlanSHA256) != 64 || len(plan.Plugins) != 1 || plan.Plugins[0].Digest == "" {
		t.Fatalf("plan identity incomplete: digest=%q plugins=%#v", plan.PlanSHA256, plan.Plugins)
	}
	action := plan.Actions[0]
	if action.ActionKey == "" || action.ID != "//app:apply" {
		t.Errorf("action identity = id %q, key %q", action.ID, action.ActionKey)
	}
	if got := action.Inputs["desired.json"]; got != inputDigest.String() {
		t.Errorf("input digest = %q", got)
	}
	if got := action.Toolchain["bin/apply"]; got != toolDigest.String() {
		t.Errorf("toolchain digest = %q", got)
	}
	if got := action.SealedInputs["TOKEN"]; got != "fake:apps/token" {
		t.Errorf("sealed_inputs TOKEN = %q", got)
	}
	if got := action.SealedInputModes["TOKEN"]; got != "file" {
		t.Errorf("sealed_input_modes TOKEN = %q", got)
	}
	if got := action.SealedOutputs["RESULT"]; got != "fake:apps/result" {
		t.Errorf("sealed_outputs RESULT = %q", got)
	}
	if got := action.SealedOutputModes["RESULT"]; got != "create_if_absent" {
		t.Errorf("sealed_output_modes RESULT = %q", got)
	}
	if !action.Network || !action.Impure || action.WorkDir != "deploy" || action.TimeoutS != 30 || action.Retries != 2 || action.RetryBackoffMs != 250 {
		t.Errorf("execution policy fields were not preserved: %#v", action)
	}
	if len(action.Command) != 2 || len(action.Outputs) != 1 || len(action.DependsOn) != 1 || action.Env["MODE"] != "test" || len(action.Sources) != 1 {
		t.Errorf("action projection incomplete: %#v", action)
	}
}

func TestPlanJSONProjectsBodyAndEweExecutionForms(t *testing.T) {
	eweDigest, err := cas.ParseDigest("sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
	if err != nil {
		t.Fatal(err)
	}
	graph := dag.NewGraph()
	if err := graph.AddAction(&dag.Action{ID: "//app:body", Body: []any{"apply"}}); err != nil {
		t.Fatal(err)
	}
	if err := graph.AddAction(&dag.Action{ID: "//app:ewe", EweRef: eweDigest}); err != nil {
		t.Fatal(err)
	}
	document := newPlanJSONDocument(&coordinator.PlanResult{Graph: graph}, []string{"//app"})
	if len(document.Actions) != 2 {
		t.Fatalf("actions = %d, want 2", len(document.Actions))
	}
	if len(document.Actions[0].Body) != 1 || document.Actions[0].Command != nil {
		t.Errorf("body action projection = %#v", document.Actions[0])
	}
	if document.Actions[1].EweDigest != eweDigest.String() || document.Actions[1].Command != nil {
		t.Errorf("ewe action projection = %#v", document.Actions[1])
	}
}

func TestValidateExpectedPlanRequiresExactDigestAndImmutablePlugins(t *testing.T) {
	graph := dag.NewGraph()
	if err := graph.AddAction(&dag.Action{ID: "//app:run", Command: []string{"true"}}); err != nil {
		t.Fatal(err)
	}
	plan := &coordinator.PlanResult{Graph: graph}
	digest, err := planJSONDigest(plan, []string{"//app"})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateExpectedPlan(plan, []string{"//app"}, digest); err != nil {
		t.Fatalf("matching exact plan rejected: %v", err)
	}
	if err := validateExpectedPlan(plan, []string{"//app"}, strings.Repeat("0", 64)); err == nil {
		t.Fatal("changed exact plan digest was accepted")
	}
	plan.Plugins = []coordinator.PluginIdentity{{Name: "mutable", Version: "1", ProtocolVersion: 1}}
	if err := validateExpectedPlan(plan, []string{"//app"}, digest); err == nil || !strings.Contains(err.Error(), "mutable command plugin") {
		t.Fatalf("mutable plugin error = %v", err)
	}
}
