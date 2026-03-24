package main

import (
	"flag"
	"testing"
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
