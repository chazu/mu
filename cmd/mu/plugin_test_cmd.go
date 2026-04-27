package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chau/mu/internal/plugin"
	"github.com/chau/mu/internal/plugin/scenario"
)

// runPluginTest implements `mu plugin test <plugin-path> [flags]`.
//
// <plugin-path> is a directory containing plugin.bb / mu.cue (the
// typical plugin layout). The command spawns the plugin, runs bundled
// generic scenarios plus any scenarios under <path>/testdata/, and
// reports pass/fail per scenario.
func runPluginTest(args []string) int {
	fs := flag.NewFlagSet("plugin test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cli := newCLIContext("plugin test", fs)
	scenarioFilter := fs.String("scenario", "", "run only scenarios whose name matches (substring)")
	update := fs.Bool("update", false, "rewrite golden files from the actual response")
	bundled := fs.Bool("bundled", true, "include mu's built-in generic scenarios")
	if err := fs.Parse(args); err != nil {
		return exitUsage
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: mu plugin test <plugin-path>")
		return exitUsage
	}
	if code, ok := cli.Resolve(resolveOpts{}); !ok {
		return code
	}
	pluginPath := fs.Arg(0)

	abs, err := filepath.Abs(pluginPath)
	if err != nil {
		return cli.fail(exitUsage, "%v", err)
	}
	entrypoint, err := resolvePluginEntrypoint(abs)
	if err != nil {
		return cli.fail(exitUsage, "%v", err)
	}

	// Spawn the plugin. We mirror the main mu manager's toolchain
	// inference: a .bb file needs bb on PATH (we use resolveBbPath,
	// which looks in ~/.mu/toolchains/bb/bb).
	var command []string
	if filepath.Ext(entrypoint) == ".bb" {
		bbPath := resolveBbPath()
		if bbPath == "" {
			return cli.fail(exitFail, "bb toolchain not found (run `mu build` in a project once to bootstrap)")
		}
		command = []string{bbPath, entrypoint}
	} else {
		command = []string{entrypoint}
	}

	name := pluginNameFromPath(abs)
	proc, err := plugin.StartProcess(name, command, filepath.Dir(entrypoint), filepath.Dir(entrypoint))
	if err != nil {
		return cli.fail(exitFail, "starting plugin: %v", err)
	}
	defer proc.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	discoverResp, err := proc.Discover(ctx)
	if err != nil {
		return cli.fail(exitFail, "discover failed: %v", err)
	}

	scenarios, err := collectScenarios(abs, *bundled, *scenarioFilter)
	if err != nil {
		return cli.fail(exitFail, "%v", err)
	}
	if len(scenarios) == 0 {
		fmt.Fprintln(os.Stderr, "no scenarios to run (use --bundled or add testdata/*.yaml)")
		return exitOK
	}

	runner := &scenario.Runner{
		Proc:     proc,
		Discover: discoverResp,
		Opts:     scenario.RunOptions{UpdateGolden: *update, Verbose: cli.Verbose},
	}

	var results []scenario.Result
	for _, s := range scenarios {
		results = append(results, runner.Run(ctx, s))
	}

	if cli.JSON {
		return emitResultsJSON(name, results)
	}
	return emitResultsHuman(name, discoverResp.Name, results, cli.Verbose)
}

func resolvePluginEntrypoint(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return path, nil
	}
	// Look for common entrypoint names.
	for _, name := range []string{"plugin.bb", "plugin"} {
		p := filepath.Join(path, name)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	// Last resort: any single *-plugin.bb.
	matches, _ := filepath.Glob(filepath.Join(path, "*-plugin.bb"))
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", fmt.Errorf("no plugin entrypoint found in %s (looked for plugin.bb, plugin, *-plugin.bb)", path)
}

func pluginNameFromPath(path string) string {
	// Directory name is the conventional plugin name (plugins/go → "go").
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return filepath.Base(path)
	}
	return filepath.Base(filepath.Dir(path))
}

func collectScenarios(pluginDir string, withBundled bool, filter string) ([]*scenario.Scenario, error) {
	var out []*scenario.Scenario
	if withBundled {
		out = append(out, scenario.Bundled()...)
	}
	testdata := filepath.Join(pluginDir, "testdata")
	local, err := scenario.LoadDir(testdata)
	if err != nil {
		return nil, err
	}
	out = append(out, local...)

	if filter != "" {
		filtered := out[:0]
		for _, s := range out {
			if scenarioMatches(s.Name, filter) {
				filtered = append(filtered, s)
			}
		}
		out = filtered
	}
	return out, nil
}

func scenarioMatches(name, filter string) bool {
	return filter == "" || strings.Contains(name, filter)
}

func emitResultsHuman(name, discoverName string, results []scenario.Result, verbose bool) int {
	fmt.Printf("=== %s (plugin: %s) ===\n", name, discoverName)
	var pass, fail, skip, errCount int
	for _, r := range results {
		symbol := "?"
		switch r.Status {
		case "pass":
			symbol = "\u2713"
			pass++
		case "fail":
			symbol = "\u2717"
			fail++
		case "skip":
			symbol = "-"
			skip++
		case "error":
			symbol = "!"
			errCount++
		}
		fmt.Printf("  %s %-40s (%dms)", symbol, r.Name, r.Elapsed.Milliseconds())
		if r.Status != "pass" && r.Reason != "" {
			fmt.Printf(" — %s", firstLine(r.Reason))
		}
		fmt.Println()
		if verbose && r.Status != "pass" {
			if r.Reason != "" {
				fmt.Printf("      reason: %s\n", r.Reason)
			}
			if r.Actual != nil {
				buf, _ := json.MarshalIndent(r.Actual, "      ", "  ")
				fmt.Printf("      actual: %s\n", buf)
			}
		}
	}
	fmt.Printf("\n%d passed, %d failed, %d skipped, %d errors\n", pass, fail, skip, errCount)
	if fail > 0 || errCount > 0 {
		return exitFail
	}
	return exitOK
}

func emitResultsJSON(name string, results []scenario.Result) int {
	payload := map[string]any{
		"plugin":  name,
		"results": results,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	enc.Encode(payload)
	for _, r := range results {
		if r.Status == "fail" || r.Status == "error" {
			return exitFail
		}
	}
	return exitOK
}

func firstLine(s string) string {
	for i, r := range s {
		if r == '\n' {
			return s[:i]
		}
	}
	return s
}
