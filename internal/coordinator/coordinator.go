package coordinator

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"

	"github.com/chau/mu/internal/builtin"
	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/dag"
	"github.com/chau/mu/internal/plugin"
)

// ToolchainBuilder builds toolchains from scratch before the planning phase.
// The scratch package provides the concrete implementation.
type ToolchainBuilder interface {
	Build(ctx context.Context, cfg *config.ProjectConfig) error
}

// Coordinator orchestrates the full mu build flow.
type Coordinator struct {
	ProjectRoot       string
	Config            *config.ProjectConfig
	Store             cas.Store
	ToolchainRegistry *ToolchainRegistry
	Builder           ToolchainBuilder      // optional; set to enable toolchain scratch build
	Workers           int                   // 0 = runtime.NumCPU()
}

// BuildResult summarises the outcome of a build.
type BuildResult struct {
	Completed  int
	Cached     int
	Failed     int
	Cancelled  int
	Graph      *dag.Graph          // the planned action DAG (always populated)
	ExecResult *dag.ExecuteResult  // per-action detail from execution
}

// PlanResult holds the planned action graph. Plugins are shut down before
// Plan() returns — they are only needed during planning, not execution.
type PlanResult struct {
	Graph *dag.Graph
}

// Plan runs the planning pipeline: build toolchains, resolve plugins, start
// plugins, resolve the target graph, and ask each plugin to plan its targets.
// Plugins are shut down before returning. The resulting Graph is ready for
// execution or inspection (--plan mode).
func (c *Coordinator) Plan(ctx context.Context, targetNames []string) (*PlanResult, error) {
	// 1. Build toolchains from scratch (must happen before plugins, since plugins
	//    may need a scratch-built runtime like bb).
	registry := c.ToolchainRegistry
	if registry == nil {
		registry = NewToolchainRegistry(c.Store)
	}
	if len(c.Config.Toolchains) > 0 && c.Builder != nil {
		if err := c.Builder.Build(ctx, c.Config); err != nil {
			return nil, fmt.Errorf("coordinator: scratch build: %w", err)
		}
	}

	// 2. Resolve plugins → CAS (hash local scripts or fetch remote ones).
	home, _ := os.UserHomeDir()
	resolver := &PluginResolver{
		Store:       c.Store,
		ProjectRoot: c.ProjectRoot,
		CacheDir:    filepath.Join(home, ".mu", "plugins"),
	}
	resolvedPlugins, err := resolver.Resolve(ctx, c.Config.Plugins)
	if err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}

	// 3. Start plugins.
	mgr := plugin.NewManager(c.ProjectRoot)

	// If any plugin uses "script", resolve the bb binary from the toolchain registry.
	if needsScriptRuntime(c.Config.Plugins) {
		bbPath, err := c.resolveScriptRuntime(ctx, registry)
		if err != nil {
			return nil, fmt.Errorf("coordinator: %w", err)
		}
		mgr.SetScriptRuntime(bbPath)
	}

	for _, rp := range resolvedPlugins {
		if err := mgr.Register(rp.Def); err != nil {
			return nil, fmt.Errorf("coordinator: register plugin %q: %w", rp.Def.Name, err)
		}
	}
	if err := mgr.Start(ctx); err != nil {
		return nil, fmt.Errorf("coordinator: starting plugins: %w", err)
	}
	// Plugins are only needed for planning. Shut them down before returning.
	defer mgr.Close()

	// 4. Resolve target graph (topological order, leaves first).
	targets, err := c.resolveTargets(targetNames)
	if err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}

	// 5. Validate target configs against plugin schemas.
	for _, t := range targets {
		if t.Toolchain == "shell" {
			continue
		}
		info := mgr.DiscoverInfo(t.Toolchain)
		if info != nil && info.ConfigSchema != nil {
			if err := ValidateTargetConfig(t, info.ConfigSchema); err != nil {
				return nil, fmt.Errorf("coordinator: %w", err)
			}
		}
	}

	// 6. Plan each target via its toolchain plugin and resolve actions.
	graph := dag.NewGraph()

	for _, t := range targets {
		var planActions []plugin.ActionSpec

		if t.Toolchain == "shell" {
			// Shell targets use the built-in handler — no external plugin needed.
			actions, _, err := builtin.ShellPlan(t)
			if err != nil {
				return nil, fmt.Errorf("coordinator: %w", err)
			}
			planActions = actions
		} else {
			ti := plugin.TargetInfo{
				Name:      t.Name,
				Toolchain: t.Toolchain,
				Sources:   t.Sources,
				Config:    t.Config,
			}

			// For v1 dep artifacts are empty; full wiring comes later.
			var deps []plugin.DepInfo
			for _, depName := range t.Deps {
				deps = append(deps, plugin.DepInfo{
					Target:    depName,
					Artifacts: nil,
				})
			}

			plan, err := mgr.Plan(ctx, t.Toolchain, ti, deps, registry.ArtifactsMap(t.Toolchain))
			if err != nil {
				return nil, fmt.Errorf("coordinator: planning target %q: %w", t.Name, err)
			}
			if plan.Error != "" {
				return nil, fmt.Errorf("coordinator: plugin error for target %q: %s", t.Name, plan.Error)
			}
			planActions = plan.Actions
		}

		// Prefix action IDs with target name to avoid cross-target collisions.
		prefixActions(t.Name, planActions)

		resolved, err := Resolve(planActions, c.ProjectRoot)
		if err != nil {
			return nil, fmt.Errorf("coordinator: resolving target %q: %w", t.Name, err)
		}

		for _, a := range resolved {
			if err := graph.AddAction(a); err != nil {
				return nil, fmt.Errorf("coordinator: building DAG: %w", err)
			}
		}
	}

	return &PlanResult{Graph: graph}, nil
}

// Execute runs a previously planned DAG.
func (c *Coordinator) Execute(ctx context.Context, plan *PlanResult) (*BuildResult, error) {
	workers := c.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	executor := &dag.Executor{Store: c.Store, Workers: workers}
	execResult, err := executor.Execute(ctx, plan.Graph)
	if err != nil {
		return nil, fmt.Errorf("coordinator: execution: %w", err)
	}

	br := buildResultFrom(execResult)
	br.Graph = plan.Graph
	br.ExecResult = execResult
	return br, nil
}

// Build orchestrates the full build pipeline: Plan() + Execute().
func (c *Coordinator) Build(ctx context.Context, targetNames []string) (*BuildResult, error) {
	plan, err := c.Plan(ctx, targetNames)
	if err != nil {
		return nil, err
	}
	return c.Execute(ctx, plan)
}

// ObserveResult holds the observation result for a single target.
type ObserveResult struct {
	Target string `json:"target"`
	State  string `json:"state"` // "converged", "drifted", "unknown"
	Diff   string `json:"diff,omitempty"`
}

// Observe checks the current state of the given targets by sending observe
// requests to their plugins. Shell targets return {State: "unknown"} unless
// they have an observe_command configured.
func (c *Coordinator) Observe(ctx context.Context, targetNames []string) ([]ObserveResult, error) {
	// 1. Build toolchains from scratch.
	registry := c.ToolchainRegistry
	if registry == nil {
		registry = NewToolchainRegistry(c.Store)
	}
	if len(c.Config.Toolchains) > 0 && c.Builder != nil {
		if err := c.Builder.Build(ctx, c.Config); err != nil {
			return nil, fmt.Errorf("coordinator: scratch build: %w", err)
		}
	}

	// 2. Resolve targets.
	targets, err := c.resolveTargets(targetNames)
	if err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}

	// Collect which toolchains we need (skip shell targets).
	neededToolchains := make(map[string]bool)
	for _, t := range targets {
		if t.Toolchain != "shell" {
			neededToolchains[t.Toolchain] = true
		}
	}

	// 3. Only start plugins for needed toolchains.
	mgr := plugin.NewManager(c.ProjectRoot)

	if len(neededToolchains) > 0 {
		if needsScriptRuntime(c.Config.Plugins) {
			bbPath, err := c.resolveScriptRuntime(ctx, registry)
			if err != nil {
				return nil, fmt.Errorf("coordinator: %w", err)
			}
			mgr.SetScriptRuntime(bbPath)
		}

		home, _ := os.UserHomeDir()
		resolver := &PluginResolver{
			Store:       c.Store,
			ProjectRoot: c.ProjectRoot,
			CacheDir:    filepath.Join(home, ".mu", "plugins"),
		}
		resolvedPlugins, err := resolver.Resolve(ctx, c.Config.Plugins)
		if err != nil {
			return nil, fmt.Errorf("coordinator: %w", err)
		}

		for _, rp := range resolvedPlugins {
			if neededToolchains[rp.Def.Name] {
				if err := mgr.Register(rp.Def); err != nil {
					return nil, fmt.Errorf("coordinator: register plugin %q: %w", rp.Def.Name, err)
				}
			}
		}
		if err := mgr.Start(ctx); err != nil {
			return nil, fmt.Errorf("coordinator: starting plugins: %w", err)
		}
		defer mgr.Close()
	}

	// 4. Send observe request for each target.
	var results []ObserveResult
	for _, t := range targets {
		if t.Toolchain == "shell" {
			// Shell targets: check for observe_command in config.
			if t.Config != nil {
				if obsCmd, ok := t.Config["observe_command"]; ok {
					if cmdSlice, ok := obsCmd.([]any); ok && len(cmdSlice) > 0 {
						// Run the observe command and check exit code.
						result := observeViaCommand(ctx, t, cmdSlice, c.ProjectRoot)
						results = append(results, result)
						continue
					}
				}
			}
			results = append(results, ObserveResult{Target: t.Name, State: "unknown"})
			continue
		}

		ti := plugin.TargetInfo{
			Name:      t.Name,
			Toolchain: t.Toolchain,
			Sources:   t.Sources,
			Config:    t.Config,
		}

		resp, err := mgr.Observe(ctx, t.Toolchain, ti, registry.ArtifactsMap(t.Toolchain))
		if err != nil {
			return nil, fmt.Errorf("coordinator: observing target %q: %w", t.Name, err)
		}

		results = append(results, ObserveResult{
			Target: t.Name,
			State:  resp.State,
			Diff:   resp.Diff,
		})
	}

	return results, nil
}

// observeViaCommand runs an observe_command for a shell target and returns the result.
func observeViaCommand(ctx context.Context, t config.Target, cmdSlice []any, projectRoot string) ObserveResult {
	args := make([]string, 0, len(cmdSlice))
	for _, item := range cmdSlice {
		if s, ok := item.(string); ok {
			args = append(args, s)
		}
	}
	if len(args) == 0 {
		return ObserveResult{Target: t.Name, State: "unknown"}
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	if err != nil {
		return ObserveResult{Target: t.Name, State: "drifted", Diff: string(output)}
	}
	return ObserveResult{Target: t.Name, State: "converged"}
}

// prefixActions rewrites action IDs and DependsOn references with a target
// prefix so that actions from different targets never collide.
func prefixActions(target string, actions []plugin.ActionSpec) {
	for i := range actions {
		actions[i].ID = target + ":" + actions[i].ID
		for j := range actions[i].DependsOn {
			actions[i].DependsOn[j] = target + ":" + actions[i].DependsOn[j]
		}
	}
}

// buildResultFrom converts a dag.ExecuteResult into a BuildResult.
func buildResultFrom(er *dag.ExecuteResult) *BuildResult {
	br := &BuildResult{
		Failed:    len(er.Failed),
		Cancelled: len(er.Cancelled),
	}
	for _, s := range er.Completed {
		if s.Cached {
			br.Cached++
		} else {
			br.Completed++
		}
	}
	return br
}

// resolveTargets returns targets in dependency order (leaves first).
// It resolves transitive deps and detects cycles.
func (c *Coordinator) resolveTargets(names []string) ([]config.Target, error) {
	index := make(map[string]config.Target, len(c.Config.Targets))
	for _, t := range c.Config.Targets {
		index[t.Name] = t
	}

	// Validate that all requested targets exist.
	for _, n := range names {
		if _, ok := index[n]; !ok {
			return nil, fmt.Errorf("target %q not found", n)
		}
	}

	// Collect the transitive closure of required targets.
	required := make(map[string]bool)
	var collect func(name string) error
	collect = func(name string) error {
		if required[name] {
			return nil
		}
		t, ok := index[name]
		if !ok {
			return fmt.Errorf("target %q not found (dependency)", name)
		}
		required[name] = true
		for _, dep := range t.Deps {
			if err := collect(dep); err != nil {
				return err
			}
		}
		return nil
	}
	for _, n := range names {
		if err := collect(n); err != nil {
			return nil, err
		}
	}

	// Topological sort (Kahn's algorithm) — produces leaves first.
	// Build adjacency for required targets only.
	inDegree := make(map[string]int, len(required))
	dependents := make(map[string][]string) // dep -> targets that depend on it
	for name := range required {
		if _, exists := inDegree[name]; !exists {
			inDegree[name] = 0
		}
		for _, dep := range index[name].Deps {
			if required[dep] {
				inDegree[name]++
				dependents[dep] = append(dependents[dep], name)
			}
		}
	}

	var queue []string
	for name, deg := range inDegree {
		if deg == 0 {
			queue = append(queue, name)
		}
	}
	sort.Strings(queue)

	var sorted []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		sorted = append(sorted, cur)

		// Collect newly ready nodes and sort them for deterministic ordering.
		var ready []string
		for _, dep := range dependents[cur] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				ready = append(ready, dep)
			}
		}
		sort.Strings(ready)
		queue = append(queue, ready...)
	}

	if len(sorted) != len(required) {
		return nil, fmt.Errorf("dependency cycle detected among targets")
	}

	result := make([]config.Target, len(sorted))
	for i, name := range sorted {
		result[i] = index[name]
	}
	return result, nil
}

// needsScriptRuntime returns true if any plugin uses the Script field.
func needsScriptRuntime(plugins []config.PluginDef) bool {
	for _, p := range plugins {
		if p.Script != "" {
			return true
		}
	}
	return false
}

// resolveScriptRuntime extracts the bb binary from the toolchain registry
// and returns its filesystem path. The bb toolchain must be built from scratch first.
func (c *Coordinator) resolveScriptRuntime(ctx context.Context, registry *ToolchainRegistry) (string, error) {
	m := registry.Get("bb")
	if m == nil {
		return "", fmt.Errorf("plugin uses \"script\" but no \"bb\" toolchain is defined; add a bb toolchain to your config")
	}

	// Find the bb binary artifact — look for "bb" or "bin/bb".
	artifact := "bb"
	if _, ok := m.Artifacts[artifact]; !ok {
		artifact = "bin/bb"
		if _, ok := m.Artifacts[artifact]; !ok {
			return "", fmt.Errorf("bb toolchain has no \"bb\" or \"bin/bb\" artifact")
		}
	}

	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	baseDir := filepath.Join(home, ".mu", "toolchains")

	return registry.ExtractBinary(ctx, "bb", artifact, baseDir)
}
