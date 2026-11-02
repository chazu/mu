package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator"
	"github.com/chazu/mu/internal/registryhttp"
	"github.com/chazu/mu/internal/scratch"
)

const (
	exitOK    = 0
	exitFail  = 1
	exitUsage = 2
)

// cliContext carries the shared preamble state for a mu subcommand —
// flags, project root, config, cache store, logging toggles. Every
// subcommand constructs one via newCLIContext, naming the shared flags it
// actually reads, registers its own subcommand-specific flags, parses, then
// calls Resolve with the resources it needs.
type cliContext struct {
	Name string // "build", "plugin add", ...

	// Shared flags (pointers; read after parsing). A pointer is nil when
	// the command did not register that flag.
	flagConfig  *string
	flagJSON    *bool
	flagVerbose *bool
	flagOffline *bool

	// Resolved values (populated by Resolve).
	ProjectRoot string
	Config      *config.ProjectConfig
	Store       cas.Store

	// Convenience accessors populated from flags by Resolve.
	JSON    bool
	Verbose bool
	Offline bool

	stderr io.Writer // injectable for tests
}

// sharedFlag names one of the flags newCLIContext can register. A command
// registers only the shared flags it reads, so no command accepts a flag
// it silently ignores.
type sharedFlag int

const (
	useConfig  sharedFlag = iota // --config PATH
	useJSON                      // --json
	useVerbose                   // --verbose
	useOffline                   // --offline
)

// resolveOpts selects which heavy resources Resolve should materialize.
// Light subcommands (e.g. mu cache, mu verify) skip project-root / config
// entirely.
type resolveOpts struct {
	NeedProjectRoot bool
	NeedConfig      bool // implies NeedProjectRoot
	NeedStore       bool // opens ~/.mu/cache (honors cfg.Cache if NeedConfig)
	ValidateConfig  bool // run config.Validate after load
}

// newCLIContext registers the requested shared flags on fs. It does NOT
// parse; the caller should register any subcommand-specific flags and
// then parse before invoking Resolve.
func newCLIContext(name string, fs *flag.FlagSet, flags ...sharedFlag) *cliContext {
	c := &cliContext{Name: name, stderr: os.Stderr}
	for _, f := range flags {
		switch f {
		case useConfig:
			c.flagConfig = fs.String("config", "", "path to mu.cue (default: discover by walking up)")
		case useJSON:
			c.flagJSON = fs.Bool("json", false, "output as JSON")
		case useVerbose:
			c.flagVerbose = fs.Bool("verbose", false, "verbose output")
		case useOffline:
			c.flagOffline = fs.Bool("offline", false, "use local dependencies; disable Mu-managed registry access and downloads")
		}
	}
	return c
}

// configPath returns the --config value, or "" when the command has no
// --config flag or it was not given.
func (c *cliContext) configPath() string {
	if c.flagConfig == nil {
		return ""
	}
	return *c.flagConfig
}

func boolFlag(p *bool) bool { return p != nil && *p }

// Resolve populates ProjectRoot, Config, and Store according to opts. It
// must be called after fs.Parse so flag values are available. Returns
// (exitCode, ok); ok=false means resolution failed and the caller should
// return the exit code.
func (c *cliContext) Resolve(opts resolveOpts) (int, bool) {
	c.JSON = boolFlag(c.flagJSON)
	c.Verbose = boolFlag(c.flagVerbose)
	c.Offline = boolFlag(c.flagOffline)

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
		cfg, err := config.LoadOffline(c.ProjectRoot, c.Offline)
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

	if opts.NeedStore {
		var cacheCfg *config.CacheConfig
		if c.Config != nil {
			cacheCfg = c.Config.Cache
		}
		store, err := buildCacheStore(cacheCfg, c.Verbose, c.Offline)
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
	if path := c.configPath(); path != "" {
		abs, err := filepath.Abs(path)
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

func (c *cliContext) networkContext(ctx context.Context) context.Context {
	if boolFlag(c.flagOffline) {
		return registryhttp.WithOffline(ctx)
	}
	return ctx
}
func (c *cliContext) requireRegistry() bool {
	if boolFlag(c.flagOffline) {
		c.fail(exitUsage, "this operation requires a registry and cannot run with --offline")
		return false
	}
	return true
}

// localCacheDir returns the local disk cache directory for the cache
// inspection subcommands: the cache.path of the mu.cue named by --config
// when one is given and sets it, else the default ~/.mu/cache.
func (c *cliContext) localCacheDir() (string, int, bool) {
	if c.configPath() == "" {
		return cachePath(), exitOK, true
	}
	if code, ok := c.Resolve(resolveOpts{NeedConfig: true}); !ok {
		return "", code, false
	}
	if c.Config.Cache == nil || c.Config.Cache.Path == "" {
		return cachePath(), exitOK, true
	}
	dir, err := cas.ExpandPath(c.Config.Cache.Path)
	if err != nil {
		return "", c.fail(exitUsage, "cache.path: %v", err), false
	}
	return dir, exitOK, true
}

// coordinatorOpts are the per-command settings of newCoordinator.
type coordinatorOpts struct {
	Workers          int       // 0 = runtime.NumCPU()
	NoCache          bool      // skip action cache reads
	SubprocessStdout io.Writer // nil = os.Stdout
}

// newCoordinator is the one place a command builds a Coordinator. cli must
// have been resolved with NeedConfig and NeedStore. Toolchains declared in
// the config are scratch-built on demand.
func newCoordinator(cli *cliContext, opts coordinatorOpts) *coordinator.Coordinator {
	registry := coordinator.NewToolchainRegistry(cli.Store)
	c := &coordinator.Coordinator{
		NoCache:           opts.NoCache,
		ProjectRoot:       cli.ProjectRoot,
		Config:            cli.Config,
		Store:             cli.Store,
		ToolchainRegistry: registry,
		Workers:           opts.Workers,
		Verbose:           cli.Verbose,
		SubprocessStdout:  opts.SubprocessStdout,
	}
	if len(cli.Config.Toolchains) > 0 && cli.Store != nil {
		c.Builder = newScratchBuilder(cli, registry, false)
	}
	return c
}

// newScratchBuilder builds the toolchain scratch builder over cli's store.
func newScratchBuilder(cli *cliContext, registry *coordinator.ToolchainRegistry, noCache bool) *scratch.Builder {
	home, _ := os.UserHomeDir()
	return &scratch.Builder{
		NoCache:  noCache,
		Store:    cli.Store,
		Registry: registry,
		CacheDir: filepath.Join(home, ".mu", "cache"),
	}
}
