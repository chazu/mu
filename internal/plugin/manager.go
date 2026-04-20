package plugin

import (
	"context"
	"fmt"
	"io"
	"sync"

	"golang.org/x/sync/errgroup"
)

// PluginDef defines a plugin as declared in mu.json.
type PluginDef struct {
	Name      string   `json:"name"`       // logical name, matches target toolchain field
	Command   []string `json:"command"`     // command to spawn, relative to project root
	Script    string   `json:"script"`      // CAS-extracted file path (any executable)
	Toolchain string   `json:"toolchain"`   // runtime toolchain (e.g. "bb"); empty = direct execution
	WorkDir   string   `json:"work_dir"`    // if set, plugin cwd is this directory (for bundled plugins)
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
	def           PluginDef
	process       *Process
	discover      *DiscoverResponse
	freshDiscover bool // true if discover was obtained live on the most recent Start
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

// Logs returns a fresh copy of the tagged stderr ring for a plugin, or
// nil if the plugin is unknown or not started. Oldest line first.
func (m *Manager) Logs(name string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, ok := m.plugins[name]
	if !ok || entry.process == nil {
		return nil
	}
	return entry.process.Logs()
}

// SetCachedDiscover pre-seeds plugin discover responses so Start will skip
// the discover RPC for each named plugin. The plugin process is still
// spawned — only the round-trip is avoided. Must be called after Register
// but before Start. Entries for unknown plugin names are silently ignored.
func (m *Manager) SetCachedDiscover(cached map[string]*DiscoverResponse) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, resp := range cached {
		if resp == nil {
			continue
		}
		entry, ok := m.plugins[name]
		if !ok {
			continue
		}
		cpy := *resp
		entry.discover = &cpy
	}
}

// NewlyDiscovered returns the names of plugins whose discover response was
// obtained live during the most recent Start call (i.e. not seeded via
// SetCachedDiscover). Callers use this to persist fresh entries into a
// discover cache.
func (m *Manager) NewlyDiscovered() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]string, 0, len(m.plugins))
	for name, entry := range m.plugins {
		if entry.freshDiscover {
			out = append(out, name)
		}
	}
	return out
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

	// Reset fresh-discover flags from any prior Start.
	for _, entry := range m.plugins {
		entry.freshDiscover = false
	}

	// Start all plugins in parallel. If a plugin's discover response was
	// pre-seeded via SetCachedDiscover, skip the discover RPC — the content-
	// addressed plugin binary is byte-identical to what produced the cached
	// response, so the response cannot have changed.
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

			if r.entry.discover != nil {
				// Cached — skip discover RPC. Still run a protocol-version
				// sanity check against the cached value.
				if r.entry.discover.ProtocolVersion != ProtocolVersion {
					return fmt.Errorf("plugin %q: cached protocol version %d incompatible with v%d",
						r.name, r.entry.discover.ProtocolVersion, ProtocolVersion)
				}
				return nil
			}

			resp, err := proc.Discover(gctx)
			if err != nil {
				return err
			}
			r.entry.discover = resp
			r.entry.freshDiscover = true
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
