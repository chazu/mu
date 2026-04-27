package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// cliContext carries the shared preamble state for a mu subcommand —
// flags, project root, config, cache store, logging toggles. Every
// subcommand constructs one via newCLIContext, registers its own
// subcommand-specific flags, calls fs.Parse, then calls Resolve with the
// resources it needs.
type cliContext struct {
	Name string // "build", "plugin add", ...

	// Shared flags (pointers; read after fs.Parse).
	flagConfig  *string
	flagJSON    *bool
	flagVerbose *bool
	flagNoColor *bool

	// Resolved values (populated by Resolve).
	ProjectRoot string
	Config      *config.ProjectConfig
	Store       cas.Store

	// Convenience accessors populated from flags after Parse.
	JSON    bool
	Verbose bool
	NoColor bool

	stderr io.Writer // injectable for tests
}

// resolveOpts selects which heavy resources Resolve should materialize.
// Light subcommands (e.g. mu cache, mu verify) skip project-root / config
// entirely.
type resolveOpts struct {
	NeedProjectRoot bool
	NeedConfig      bool // implies NeedProjectRoot
	NeedStore       bool // opens ~/.mu/cache (honors cfg.Cache if NeedConfig)
	NoCache         bool // when NeedStore is true: leave Store nil
	ValidateConfig  bool // run config.Validate after load
}

// newCLIContext registers the canonical shared flags on fs. It does NOT
// call fs.Parse; the caller should register any subcommand-specific flags
// and then parse before invoking Resolve.
func newCLIContext(name string, fs *flag.FlagSet) *cliContext {
	c := &cliContext{
		Name:        name,
		flagConfig:  fs.String("config", "", "path to mu.cue (default: discover by walking up)"),
		flagJSON:    fs.Bool("json", false, "output as JSON"),
		flagVerbose: fs.Bool("verbose", false, "verbose output"),
		flagNoColor: fs.Bool("no-color", false, "disable ANSI color output"),
		stderr:      os.Stderr,
	}
	return c
}

// Resolve populates ProjectRoot, Config, and Store according to opts. It
// must be called after fs.Parse so flag values are available. Returns
// (exitCode, ok); ok=false means resolution failed and the caller should
// return the exit code.
func (c *cliContext) Resolve(opts resolveOpts) (int, bool) {
	c.JSON = *c.flagJSON
	c.Verbose = *c.flagVerbose
	c.NoColor = *c.flagNoColor

	if opts.NeedConfig {
		opts.NeedProjectRoot = true
	}

	if opts.NeedProjectRoot {
		root, code, ok := c.resolveProjectRoot()
		if !ok {
			return code, false
		}
		c.ProjectRoot = root
	}

	if opts.NeedConfig {
		cfg, err := config.Load(c.ProjectRoot)
		if err != nil {
			return c.fail(exitUsage, "%v", err), false
		}
		if opts.ValidateConfig {
			if err := config.Validate(cfg); err != nil {
				return c.fail(exitUsage, "%v", err), false
			}
		}
		c.Config = cfg
	}

	if opts.NeedStore && !opts.NoCache {
		var cacheCfg *config.CacheConfig
		if c.Config != nil {
			cacheCfg = c.Config.Cache
		}
		store, err := buildCacheStore(cacheCfg, c.Verbose)
		if err != nil {
			return c.fail(exitUsage, "creating cache store: %v", err), false
		}
		c.Store = store
	}

	return exitOK, true
}

// resolveProjectRoot picks a project root from --config or cwd discovery.
// Returns (root, exitCode, ok).
func (c *cliContext) resolveProjectRoot() (string, int, bool) {
	if *c.flagConfig != "" {
		abs, err := filepath.Abs(*c.flagConfig)
		if err != nil {
			return "", c.fail(exitUsage, "%v", err), false
		}
		return filepath.Dir(abs), exitOK, true
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", c.fail(exitUsage, "%v", err), false
	}
	root, err := config.FindProjectRoot(cwd)
	if err != nil {
		return "", c.fail(exitUsage, "%v", err), false
	}
	return root, exitOK, true
}

// fail prints "mu <name>: <message>\n" to stderr and returns code.
func (c *cliContext) fail(code int, format string, args ...any) int {
	fmt.Fprintf(c.stderr, "mu %s: ", c.Name)
	fmt.Fprintf(c.stderr, format, args...)
	fmt.Fprintln(c.stderr)
	return code
}

// CachePath returns the default on-disk cache directory (~/.mu/cache).
// Light subcommands that don't load config but need the cache directory
// directly (mu cache, mu verify) use this instead of opening a Store.
func (c *cliContext) CachePath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mu", "cache")
}
