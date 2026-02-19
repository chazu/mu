package coordinator

import (
	"context"
	"fmt"
	"runtime"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/dag"
	"github.com/chau/mu/internal/plugin"
)

// Coordinator orchestrates the full mu build flow.
type Coordinator struct {
	ProjectRoot string
	Config      *config.ProjectConfig
	Store       cas.Store
	Workers     int // 0 = runtime.NumCPU()
}

// BuildResult summarises the outcome of a build.
type BuildResult struct {
	Completed int
	Cached    int
	Failed    int
	Cancelled int
}

// Build orchestrates the full build pipeline for the given target names.
func (c *Coordinator) Build(ctx context.Context, targetNames []string) (*BuildResult, error) {
	// 1. Start plugins.
	mgr := plugin.NewManager(c.ProjectRoot)
	for _, p := range c.Config.Plugins {
		if err := mgr.Register(plugin.PluginDef{
			Name:    p.Name,
			Command: p.Command,
		}); err != nil {
			return nil, fmt.Errorf("coordinator: register plugin %q: %w", p.Name, err)
		}
	}
	if err := mgr.Start(ctx); err != nil {
		return nil, fmt.Errorf("coordinator: starting plugins: %w", err)
	}
	defer mgr.Close()

	// 2. Resolve target graph (topological order, leaves first).
	targets, err := c.resolveTargets(targetNames)
	if err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}

	// 3-4. Plan each target via its toolchain plugin and resolve actions.
	graph := dag.NewGraph()

	for _, t := range targets {
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

		plan, err := mgr.Plan(ctx, t.Toolchain, ti, deps, nil)
		if err != nil {
			return nil, fmt.Errorf("coordinator: planning target %q: %w", t.Name, err)
		}
		if plan.Error != "" {
			return nil, fmt.Errorf("coordinator: plugin error for target %q: %s", t.Name, plan.Error)
		}

		// Prefix action IDs with target name to avoid cross-target collisions.
		prefixActions(t.Name, plan.Actions)

		resolved, err := Resolve(plan.Actions, c.ProjectRoot)
		if err != nil {
			return nil, fmt.Errorf("coordinator: resolving target %q: %w", t.Name, err)
		}

		for _, a := range resolved {
			if err := graph.AddAction(a); err != nil {
				return nil, fmt.Errorf("coordinator: building DAG: %w", err)
			}
		}
	}

	// 5. Execute the global DAG.
	workers := c.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	executor := &dag.Executor{Store: c.Store, Workers: workers}
	execResult, err := executor.Execute(ctx, graph)
	if err != nil {
		return nil, fmt.Errorf("coordinator: execution: %w", err)
	}

	return buildResultFrom(execResult), nil
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

	var sorted []string
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		sorted = append(sorted, cur)

		for _, dep := range dependents[cur] {
			inDegree[dep]--
			if inDegree[dep] == 0 {
				queue = append(queue, dep)
			}
		}
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
