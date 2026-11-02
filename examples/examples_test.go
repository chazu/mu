// Package examples_test checks that every example project still loads,
// validates and plans. Planning never executes actions and runs offline.
package examples_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas/oci"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/registryhttp"
)

// skipExample lists example directories that are not checked at all.
var skipExample = map[string]string{
	"cowsay":            "being deleted by mu-lx8.4 (toy plugin)",
	"scratch":           "being deleted by mu-lx8.4 (duplicates mu scratch)",
	"pith-e2e":          "being deleted with the pith VM by mu-d5k.2",
	"pith-go-toolchain": "being deleted with the pith VM by mu-d5k.2",
}

// skipPlan lists examples whose config is loaded and validated but not
// planned, because planning needs a plugin runtime the test cannot provide
// offline.
var skipPlan = map[string]string{
	"go-hello": "the go plugin is a Babashka script; planning needs the bb toolchain, which `mu scratch` downloads",
}

func TestExamplesPlan(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir, err := filepath.Abs(e.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(filepath.Join(dir, "mu.cue")); err != nil {
			continue
		}
		found++
		name := e.Name()
		t.Run(name, func(t *testing.T) {
			if reason, ok := skipExample[name]; ok {
				t.Skip(reason)
			}
			cfg := loadExample(t, dir)
			if reason, ok := skipPlan[name]; ok {
				t.Skip("config loads; planning skipped: " + reason)
			}
			planAll(t, dir, cfg)
		})
	}
	if found == 0 {
		t.Fatal("no examples with a mu.cue found")
	}
}

// TestHelloGoPluginPlans builds examples/plugins/hello-go and plans the
// mu.cue snippet from its README against it.
func TestHelloGoPluginPlans(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	bin := filepath.Join(t.TempDir(), "hello-go")
	build := exec.Command(goBin, "build", "-o", bin, "./plugins/hello-go")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("go build hello-go: %v\n%s", err, out)
	}

	project := t.TempDir()
	cue := `package mu

plugins: [{name: "hello", command: ["` + bin + `"]}]

targets: [{
	target:    "//hello"
	toolchain: "hello"
	sources: []
	config: {
		out:     "hello.txt"
		message: "hello from mu"
	}
}]
`
	if err := os.WriteFile(filepath.Join(project, "mu.cue"), []byte(cue), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg := loadExample(t, project)
	if n := planAll(t, project, cfg); n != 1 {
		t.Errorf("planned %d actions, want 1", n)
	}
}

func loadExample(t *testing.T, dir string) *config.ProjectConfig {
	t.Helper()
	cfg, err := config.LoadOffline(dir, true)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if err := config.Validate(cfg); err != nil {
		t.Fatalf("validate: %v", err)
	}
	return cfg
}

// planAll plans every target in cfg and returns the number of actions.
func planAll(t *testing.T, dir string, cfg *config.ProjectConfig) int {
	t.Helper()
	t.Setenv("HOME", t.TempDir()) // keep plugin and cache state out of ~/.mu
	store, err := oci.NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &coordinator.Coordinator{
		ProjectRoot:       dir,
		Config:            cfg,
		Store:             store,
		ToolchainRegistry: coordinator.NewToolchainRegistry(store),
		NoDiscoverCache:   true,
	}
	plan, err := c.Plan(registryhttp.WithOffline(context.Background()), []string{"//..."})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	n := plan.Graph.Len()
	if n == 0 {
		t.Fatalf("plan has no actions (targets: %s)", targetNames(cfg))
	}
	return n
}

func targetNames(cfg *config.ProjectConfig) string {
	names := make([]string, len(cfg.Targets))
	for i, tg := range cfg.Targets {
		names[i] = tg.Name
	}
	return strings.Join(names, ", ")
}
