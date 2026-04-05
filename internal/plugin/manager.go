package plugin

import (
	"context"
	"fmt"
	"sync"

	"golang.org/x/sync/errgroup"
)

// PluginDef defines a plugin as declared in mu.json.
type PluginDef struct {
	Name         string   `json:"name"`          // logical name, matches target toolchain field
	Command      []string `json:"command"`        // command to spawn, relative to project root
	Script       string   `json:"script"`         // CAS-extracted file path (any executable)
	NeedsRuntime bool     `json:"needs_runtime"`  // true = prepend ScriptRuntime (bb) to execute
	WorkDir      string   `json:"work_dir"`       // if set, plugin cwd is this directory (for bundled plugins)
}

// Manager manages the lifecycle of plugin processes and routes requests.
type Manager struct {
	projectRoot   string
	scriptRuntime string                  // path to bb binary for script-based plugins (optional)
	plugins       map[string]*pluginEntry // name → entry
	mu            sync.RWMutex
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

// Register adds a plugin definition. Call before Start.
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
		command, err := m.resolveCommand(entry.def)
		if err != nil {
			return fmt.Errorf("plugin %q: %w", name, err)
		}
		toStart = append(toStart, resolved{name: name, entry: entry, command: command})
	}

	// Start all plugins in parallel.
	g, gctx := errgroup.WithContext(ctx)
	for _, r := range toStart {
		r := r // capture loop var
		g.Go(func() error {
			proc, err := StartProcess(r.name, r.command, m.projectRoot, r.entry.def.WorkDir)
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
// For CAS-extracted plugins that need a runtime (e.g. .bb scripts), the
// script runtime is prepended. Other CAS-extracted plugins (compiled
// binaries, shell scripts) are executed directly.
func (m *Manager) resolveCommand(def PluginDef) ([]string, error) {
	if def.Script != "" {
		if def.NeedsRuntime {
			if m.scriptRuntime == "" {
				return nil, fmt.Errorf("script %q requires a bb toolchain but no script runtime is available", def.Script)
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

// Observe sends an observe request to the plugin registered for the given toolchain.
// If the plugin does not declare "observe" in its capabilities, returns an empty response.
// Secrets contains resolved sealed input values for the target (may be nil).
func (m *Manager) Observe(ctx context.Context, toolchain string, target TargetInfo, toolchainArtifacts map[string]string, secrets map[string]string) (*ObserveResponse, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	entry, ok := m.plugins[toolchain]
	if !ok {
		return nil, fmt.Errorf("no plugin registered for toolchain %q", toolchain)
	}
	if entry.process == nil {
		return nil, fmt.Errorf("plugin %q: not started", toolchain)
	}

	// Check capabilities before sending observe.
	if entry.discover != nil && !entry.discover.HasCapability("observe") {
		return &ObserveResponse{}, nil
	}

	return entry.process.Observe(ctx, target, toolchainArtifacts, secrets)
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

// PluginNames returns the names of all registered plugins.
func (m *Manager) PluginNames() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	names := make([]string, 0, len(m.plugins))
	for name := range m.plugins {
		names = append(names, name)
	}
	return names
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
