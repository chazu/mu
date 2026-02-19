package plugin

import (
	"context"
	"fmt"
	"sync"
)

// PluginDef defines a plugin as declared in mu.json.
type PluginDef struct {
	Name    string   `json:"name"`    // logical name, matches target toolchain field
	Command []string `json:"command"` // command to spawn, relative to project root
}

// Manager manages the lifecycle of plugin processes and routes requests.
type Manager struct {
	projectRoot string
	plugins     map[string]*pluginEntry // name → entry
	mu          sync.RWMutex
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

// Start spawns all registered plugins and sends discover requests.
// Returns an error if any plugin fails to start or has incompatible protocol.
func (m *Manager) Start(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	for name, entry := range m.plugins {
		proc, err := StartProcess(name, entry.def.Command, m.projectRoot)
		if err != nil {
			// Shut down any already-started plugins.
			m.closeAllLocked()
			return err
		}
		entry.process = proc

		resp, err := proc.Discover(ctx)
		if err != nil {
			m.closeAllLocked()
			return err
		}
		entry.discover = resp
	}
	return nil
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
