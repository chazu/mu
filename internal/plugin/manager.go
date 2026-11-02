package plugin

import (
	"context"
	"fmt"
	"io"
	"os"
	"sync"

	"golang.org/x/sync/errgroup"
)

// PluginDef defines a plugin as declared in mu.cue.
type PluginDef struct {
	Name      string   `json:"name"`      // logical name, matches target toolchain field
	Command   []string `json:"command"`   // command to spawn, relative to project root
	Script    string   `json:"script"`    // CAS-extracted file path (any executable)
	Toolchain string   `json:"toolchain"` // runtime toolchain (e.g. "bb"); empty = direct execution
	WorkDir   string   `json:"work_dir"`  // if set, plugin cwd is this directory (for bundled plugins)
}

// Manager manages the lifecycle of plugin processes and routes requests.
type Manager struct {
	projectRoot   string
	scriptRuntime string                  // path to bb binary for script-based plugins (optional)
	plugins       map[string]*pluginEntry // name → entry
	mu            sync.RWMutex

	// verboseSink, if non-nil, receives tagged stderr lines from every
	// spawned plugin as they arrive. Writes across plugins are
	// serialized through sinkMu so lines never interleave mid-line.
	verboseSink io.Writer
	sinkMu      sync.Mutex
}

type pluginEntry struct {
	def      PluginDef
	process  *Process
	discover *DiscoverResponse
}

// NewManager creates a Manager but does not start any plugins.
// Call Start to spawn processes and run discovery.
func NewManager(projectRoot string) *Manager {
	return &Manager{
		projectRoot: projectRoot,
		plugins:     make(map[string]*pluginEntry),
	}
}

// SetScriptRuntime sets the path to the bb binary for script-based plugins.
// Must be called before Start.
func (m *Manager) SetScriptRuntime(path string) {
	m.scriptRuntime = path
}

// SetVerbose configures a writer that receives tagged stderr lines from
// every plugin as they arrive ("[name] <line>\n"). Writes across
// concurrent plugins are serialized so lines never interleave. Passing
// nil disables live streaming (buffered ring capture continues regardless).
// Must be called before Start.
func (m *Manager) SetVerbose(w io.Writer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.verboseSink = w
}

// Register adds a plugin definition. A later Start starts only new definitions.
func (m *Manager) Register(def PluginDef) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.plugins[def.Name]; exists {
		return fmt.Errorf("plugin %q: already registered", def.Name)
	}
	m.plugins[def.Name] = &pluginEntry{def: def}
	return nil
}

// Start spawns all registered plugins concurrently and sends discover requests.
// Returns an error if any plugin fails to start or has incompatible protocol.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Resolve commands first (no I/O, fast).
	type resolved struct {
		name    string
		entry   *pluginEntry
		command []string
	}
	var toStart []resolved
	for name, entry := range m.plugins {
		if entry.process != nil {
			continue
		}
		command, err := m.resolveCommand(entry.def)
		if err != nil {
			return fmt.Errorf("plugin %q: %w", name, err)
		}
		toStart = append(toStart, resolved{name: name, entry: entry, command: command})
	}

	// Start all plugins in parallel; each gets exactly one live discover,
	// which Process.Discover validates.
	g, gctx := errgroup.WithContext(ctx)
	for _, r := range toStart {
		r := r // capture loop var
		g.Go(func() error {
			opts := ProcessOptions{}
			if m.verboseSink != nil {
				opts.LiveSink = m.verboseSink
				opts.LiveSinkMu = &m.sinkMu
			}
			proc, err := StartProcessWithOptions(r.name, r.command, m.projectRoot, r.entry.def.WorkDir, opts)
			if err != nil {
				return err
			}
			r.entry.process = proc

			resp, err := proc.Discover(gctx)
			if err != nil {
				return err
			}
			r.entry.discover = resp
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		m.closeAllLocked()
		return err
	}
	return nil
}

// resolveCommand returns the command to spawn for a plugin definition.
// If the plugin declares a runtime toolchain (e.g. "bb"), the toolchain
// binary is prepended to the command. Otherwise the script or command
// is executed directly.
func (m *Manager) resolveCommand(def PluginDef) ([]string, error) {
	if def.Script != "" {
		if def.Toolchain != "" {
			if m.scriptRuntime == "" {
				return nil, fmt.Errorf("plugin %q requires toolchain %q but no runtime is available", def.Name, def.Toolchain)
			}
			return []string{m.scriptRuntime, def.Script}, nil
		}
		return []string{def.Script}, nil
	}
	return def.Command, nil
}

// Plan sends a plan request to the plugin registered for the given toolchain.
func (m *Manager) Plan(ctx context.Context, toolchain string, target TargetInfo, deps []DepInfo, toolchainArtifacts map[string]string) (*PlanResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.plugins[toolchain]
	if !ok {
		return nil, fmt.Errorf("no plugin registered for toolchain %q", toolchain)
	}
	if entry.process == nil {
		return nil, fmt.Errorf("plugin %q: not started", toolchain)
	}

	return entry.process.Plan(ctx, target, deps, toolchainArtifacts)
}

// ResolveSecret sends a resolve_secret request to the named plugin.
// Returns an error if the plugin is not registered or does not declare the
// "resolve_secret" capability. The returned value must never be logged,
// cached, or stored in CAS.
func (m *Manager) ResolveSecret(ctx context.Context, pluginName string, ref string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.plugins[pluginName]
	if !ok {
		// Built-in "env" scheme: resolve a sealed input from the process
		// environment with no plugin. env-var secrets are too basic to warrant a
		// subprocess; an explicitly declared plugin named "env" still overrides
		// (handled by the registered-plugin path above).
		if pluginName == "env" {
			return resolveEnvSecret(ref)
		}
		return "", fmt.Errorf("no plugin registered for secret scheme %q", pluginName)
	}
	if entry.process == nil {
		return "", fmt.Errorf("plugin %q: not started", pluginName)
	}

	if entry.discover != nil && !entry.discover.HasCapability("resolve_secret") {
		return "", fmt.Errorf("plugin %q does not support resolve_secret", pluginName)
	}

	return entry.process.ResolveSecret(ctx, ref)
}

// resolveEnvSecret implements the built-in "env" secret scheme: a ref of
// "env:NAME" resolves to $NAME from the process environment. Read-only; the
// value must never be logged, cached, or stored in CAS by the caller.
func resolveEnvSecret(name string) (string, error) {
	val, ok := os.LookupEnv(name)
	if !ok {
		return "", fmt.Errorf("env: environment variable %q is not set", name)
	}
	return val, nil
}

// StoreSecret sends a store_secret request to the named plugin.
// Returns an error if the plugin is not registered or does not declare the
// "store_secret" capability. The value bytes must never be logged or
// cached by the caller.
func (m *Manager) StoreSecret(ctx context.Context, pluginName, ref, value, mode string) error {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.plugins[pluginName]
	if !ok {
		return fmt.Errorf("no plugin registered for secret scheme %q", pluginName)
	}
	if entry.process == nil {
		return fmt.Errorf("plugin %q: not started", pluginName)
	}

	if entry.discover != nil && !entry.discover.HasCapability("store_secret") {
		return fmt.Errorf("plugin %q does not support store_secret", pluginName)
	}

	return entry.process.StoreSecret(ctx, ref, value, mode)
}

// DiscoverInfo returns a copy of the discover response for a plugin, or nil if not found.
func (m *Manager) DiscoverInfo(name string) *DiscoverResponse {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.plugins[name]
	if !ok || entry.discover == nil {
		return nil
	}

	// Return a defensive copy so callers cannot mutate internal state.
	cpy := *entry.discover
	if entry.discover.Consumes != nil {
		cpy.Consumes = make([]string, len(entry.discover.Consumes))
		copy(cpy.Consumes, entry.discover.Consumes)
	}
	if entry.discover.Produces != nil {
		cpy.Produces = make([]string, len(entry.discover.Produces))
		copy(cpy.Produces, entry.discover.Produces)
	}
	if entry.discover.ConfigSchema != nil {
		cpy.ConfigSchema = make(map[string]any, len(entry.discover.ConfigSchema))
		for k, v := range entry.discover.ConfigSchema {
			cpy.ConfigSchema[k] = v
		}
	}
	return &cpy
}

// Close gracefully shuts down all plugin processes.
func (m *Manager) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closeAllLocked()
}

// closeAllLocked shuts down all started plugins. Must hold m.mu.
func (m *Manager) closeAllLocked() error {
	var firstErr error
	for _, entry := range m.plugins {
		if entry.process != nil {
			if err := entry.process.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
			entry.process = nil
		}
	}
	return firstErr
}
