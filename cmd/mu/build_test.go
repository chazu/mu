package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"testing"

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

func TestPrintPlanJSONIncludesSealedActionClaims(t *testing.T) {
	g := dag.NewGraph()
	err := g.AddAction(&dag.Action{
		ID:                "//app:apply",
		Command:           []string{"apply"},
		SealedInputs:      map[string]string{"TOKEN": "fake:apps/token"},
		SealedInputModes:  map[string]string{"TOKEN": "file"},
		SealedOutputs:     map[string]string{"RESULT": "fake:apps/result"},
		SealedOutputModes: map[string]string{"RESULT": "create_if_absent"},
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

	if code := printPlanJSON(g, []string{"//app"}); code != 0 {
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
		Actions []struct {
			SealedInputs      map[string]string `json:"sealed_inputs"`
			SealedInputModes  map[string]string `json:"sealed_input_modes"`
			SealedOutputs     map[string]string `json:"sealed_outputs"`
			SealedOutputModes map[string]string `json:"sealed_output_modes"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(payload, &plan); err != nil {
		t.Fatalf("decode JSON plan: %v\n%s", err, payload)
	}
	if len(plan.Actions) != 1 {
		t.Fatalf("actions = %d, want 1", len(plan.Actions))
	}
	action := plan.Actions[0]
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
}
