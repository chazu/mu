package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chazu/mu/docs/guide"
	"github.com/chazu/mu/internal/config"
)

func runGuide(args []string) int {
	if len(args) == 0 {
		printGuideIndex()
		return 0
	}

	switch args[0] {
	case "overview":
		printGuideOverview()
	case "mu.cue":
		printGuideMuJSON()
	case "plugins":
		printGuidePlugins()
	case "build":
		printGuideBuild()
	case "observe":
		printGuideObserve()
	case "pudl":
		printGuidePudl()
	case "cache":
		printGuideCache()
	case "secrets":
		printGuideSecrets()
	case "secret-gen":
		printGuideSecretGen()
	case "toolchains":
		printGuideToolchains()
	case "shell":
		printGuideShell()
	case "protocol":
		printGuideProtocol()
	case "secret-providers":
		printGuideSecretProviders()
	case "pith-plugins":
		printGuidePithPlugins()
	case "sandbox":
		printGuideSandbox()
	case "advice":
		printGuideAdvice()
	case "sdk":
		printGuideSDK()
	case "plugin":
		fs := flag.NewFlagSet("guide plugin", flag.ContinueOnError)
		fs.SetOutput(os.Stderr)
		digest := fs.String("digest", "", "select a cached guide by full SHA-256 digest")
		if err := fs.Parse(args[1:]); err != nil {
			return exitUsage
		}
		if fs.NArg() != 1 {
			fmt.Fprintln(os.Stderr, "usage: mu guide plugin [--digest sha256:...] <name>")
			return exitUsage
		}
		return printGuideForPlugin(fs.Arg(0), *digest)
	default:
		fmt.Fprintf(os.Stderr, "mu guide: unknown topic %q\n", args[0])
		fmt.Fprintln(os.Stderr, "Run 'mu guide' for a list of topics.")
		return 2
	}
	return 0
}

// printTopic renders the embedded markdown for a guide topic. If the
// topic is unknown (which would be a programming error since the
// dispatcher gates topic names), it prints a clear message to stderr.
func printTopic(topic string) {
	body, ok := guide.Get(topic)
	if !ok {
		fmt.Fprintf(os.Stderr, "mu guide: missing embedded topic %q\n", topic)
		return
	}
	fmt.Print(body)
}

func printGuideIndex()           { printTopic("index") }
func printGuideOverview()        { printTopic("overview") }
func printGuideMuJSON()          { printTopic("mu.cue") }
func printGuideBuild()           { printTopic("build") }
func printGuidePlugins()         { printTopic("plugins") }
func printGuideObserve()         { printTopic("observe") }
func printGuidePudl()            { printTopic("pudl") }
func printGuideCache()           { printTopic("cache") }
func printGuideSecrets()         { printTopic("secrets") }
func printGuideSecretGen()       { printTopic("secret-gen") }
func printGuideToolchains()      { printTopic("toolchains") }
func printGuideShell()           { printTopic("shell") }
func printGuideSecretProviders() { printTopic("secret-providers") }
func printGuideProtocol()        { printTopic("protocol") }
func printGuidePithPlugins()     { printTopic("pith-plugins") }
func printGuideSandbox()         { printTopic("sandbox") }
func printGuideAdvice()          { printTopic("advice") }
func printGuideSDK()             { printTopic("sdk") }

// printGuideForPlugin finds and prints the guide text for a named plugin.
// It searches in order:
//  1. Extracted CAS bundles in ~/.mu/plugins/<name>/bundle-*/
//  2. Local plugin directory in the current project (plugins/<name>/)
func printGuideForPlugin(name string, selection ...string) int {
	requested := ""
	if len(selection) > 0 {
		requested = selection[0]
	}
	root, rootErr := findGuideProjectRoot()
	var cfg *config.ProjectConfig
	if rootErr == nil {
		cfg, _ = config.Load(root)
	}
	source, def, _, err := resolveInfoTarget(root, cfg, name, requested)
	if err == nil {
		dir := def.WorkDir
		if dir == "" {
			dir = filepath.Dir(def.Script)
		}
		if path := findGuideInDir(dir); path != "" {
			return printGuideFile(name, path)
		}
		// Once an identity is selected, never take another version's guide.
		fmt.Fprintf(os.Stderr, "mu guide plugin %s: selected %s plugin has no guide\n", name, source)
		return 1
	}
	if !errors.Is(err, errCachedPluginMissing) {
		fmt.Fprintf(os.Stderr, "mu guide plugin: %v\n", err)
		return 1
	}
	if rootErr == nil && requested == "" {
		if path := findGuideInDir(filepath.Join(root, "plugins", name)); path != "" {
			return printGuideFile(name, path)
		}
	}

	fmt.Fprintf(os.Stderr, "mu guide plugin %s: no guide found\n", name)
	fmt.Fprintf(os.Stderr, "\nTo add a guide, create a GUIDE.md file in the plugin directory\n")
	fmt.Fprintf(os.Stderr, "and set \"guide\": \"GUIDE.md\" in the plugin manifest:\n\n")
	fmt.Fprintf(os.Stderr, "  plugins/%s/GUIDE.md\n", name)
	fmt.Fprintf(os.Stderr, "  plugins/%s/mu.cue → {\"plugin\": {\"guide\": \"GUIDE.md\", ...}}\n", name)
	return 1
}

// findGuideInDir looks for a guide file in a plugin directory.
// It checks the manifest first (for the declared guide path), then
// falls back to conventional filenames.
func findGuideInDir(dir string) string {
	if cfg, err := config.LoadPluginManifest(dir); err == nil && cfg.Plugin != nil && cfg.Plugin.Guide != "" {
		guidePath := filepath.Join(dir, cfg.Plugin.Guide)
		if _, err := os.Stat(guidePath); err == nil {
			return guidePath
		}
	}

	// Fall back to conventional filenames.
	for _, name := range []string{"GUIDE.md", "GUIDE", "guide.md", "guide.txt"} {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}

	return ""
}

// findGuideProjectRoot finds the mu project root for guide lookups.
func findGuideProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return config.FindProjectRoot(cwd)
}

// printGuideFile reads and prints a guide file.
func printGuideFile(pluginName, path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mu guide plugin %s: %v\n", pluginName, err)
		return 1
	}

	content := strings.TrimRight(string(data), "\n")
	fmt.Println(content)
	return 0
}
