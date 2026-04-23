package coordinator

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/chau/mu/internal/builtin"
	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/coordinator/discovercache"
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
	Builder           ToolchainBuilder // optional; set to enable toolchain scratch build
	Workers           int              // 0 = runtime.NumCPU()

	// NoDiscoverCache disables the plugin discover response cache for this
	// build (no reads, no writes). Useful when hacking on a plugin locally.
	NoDiscoverCache bool

	// Verbose increases coordinator-level log verbosity when true. Cache
	// layer events (hit/miss/repair) are surfaced at info level instead
	// of debug; plugin stderr is mirrored live to VerboseSink.
	Verbose bool

	// VerboseSink receives tagged plugin stderr when Verbose is true.
	// Defaults to os.Stderr when nil and Verbose is true.
	VerboseSink io.Writer

	// SubprocessStdout overrides where action subprocess stdout is written.
	// Nil means os.Stdout. Set to os.Stderr when stdout is reserved for
	// structured output (e.g. `mu build --emit-manifest`).
	SubprocessStdout io.Writer
}

// BuildResult summarises the outcome of a build.
type BuildResult struct {
	Completed  int
	Cached     int
	Failed     int
	Cancelled  int
	Graph      *dag.Graph         // the planned action DAG (always populated)
	ExecResult *dag.ExecuteResult // per-action detail from execution
	Targets    []config.Target    // the targets that were built
}

// PlanResult holds the planned action graph. Plugins are shut down before
// Plan() returns — they are only needed during planning, not execution.
type PlanResult struct {
	Graph           *dag.Graph
	Targets         []config.Target              // the resolved targets (for manifest metadata)
	ResolvedSecrets map[string]map[string]string // actionID → envName → secret value (never persisted)
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

	if c.Verbose {
		sink := c.VerboseSink
		if sink == nil {
			sink = os.Stderr
		}
		mgr.SetVerbose(sink)
	}

	// If any plugin uses "script", resolve the bb binary from the toolchain registry.
	if needsScriptRuntime(c.Config.Plugins, c.ProjectRoot) {
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

	// Consult the discover cache before starting. Entries are keyed by CAS
	// digest; command plugins (zero digest) are never cached.
	var dcache *discovercache.Cache
	if !c.NoDiscoverCache {
		if p := discovercache.DefaultPath(); p != "" {
			dcache = discovercache.Open(p)
			seeded := make(map[string]*plugin.DiscoverResponse)
			for _, rp := range resolvedPlugins {
				if rp.Digest.Hash == "" {
					continue
				}
				if resp, ok := dcache.Get(rp.Digest); ok {
					seeded[rp.Def.Name] = resp
				}
			}
			if len(seeded) > 0 {
				mgr.SetCachedDiscover(seeded)
			}
		}
	}

	if err := mgr.Start(ctx); err != nil {
		return nil, fmt.Errorf("coordinator: starting plugins: %w", err)
	}
	// Plugins are only needed for planning. Shut them down before returning.
	defer mgr.Close()

	// Persist any discover responses obtained live this build.
	if dcache != nil {
		digestByName := make(map[string]cas.Digest, len(resolvedPlugins))
		for _, rp := range resolvedPlugins {
			digestByName[rp.Def.Name] = rp.Digest
		}
		for _, name := range mgr.NewlyDiscovered() {
			d, ok := digestByName[name]
			if !ok || d.Hash == "" {
				continue
			}
			info := mgr.DiscoverInfo(name)
			if info == nil {
				continue
			}
			_ = dcache.Put(d, info)
		}
	}

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

	// Cross-target artifact plumbing: as each target is planned (in
	// topological order, leaves first), record its declared_outputs so
	// later dependents receive them via DepInfo.Artifacts, and track
	// which action produces each output path so Resolve can wire
	// implicit DependsOn edges across target boundaries.
	declaredByTarget := make(map[string]map[string]string, len(targets))
	producerForPath := make(map[string]string)

	for _, t := range targets {
		var planActions []plugin.ActionSpec
		var declaredOutputs map[string]string

		if t.Toolchain == "shell" {
			// Shell targets use the built-in handler — no external plugin needed.
			actions, declared, err := builtin.ShellPlan(t)
			if err != nil {
				return nil, fmt.Errorf("coordinator: %w", err)
			}
			planActions = actions
			declaredOutputs = declared
		} else {
			ti := plugin.TargetInfo{
				Name:         t.Name,
				Toolchain:    t.Toolchain,
				Sources:      t.Sources,
				Config:       t.Config,
				SealedInputs: t.SealedInputs,
			}

			// Thread each dep's declared_outputs (artifact-type → path,
			// project-relative) into DepInfo.Artifacts so the plugin
			// can reference them when shaping its own actions.
			var deps []plugin.DepInfo
			for _, depName := range t.Deps {
				deps = append(deps, plugin.DepInfo{
					Target:    depName,
					Artifacts: cloneStringMap(declaredByTarget[depName]),
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
			declaredOutputs = plan.Outputs
		}

		// Prefix action IDs with target name to avoid cross-target collisions.
		prefixActions(t.Name, planActions)

		// Snapshot this target's output producers BEFORE Resolve runs:
		// the target's own actions need to self-reference via {action:id}
		// for intra-target wiring, not through the cross-target path map.
		crossTargetProducers := cloneStringMap(producerForPath)

		resolved, err := Resolve(planActions, c.ProjectRoot, crossTargetProducers)
		if err != nil {
			return nil, fmt.Errorf("coordinator: resolving target %q: %w", t.Name, err)
		}

		for _, a := range resolved {
			if err := graph.AddAction(a); err != nil {
				return nil, fmt.Errorf("coordinator: building DAG: %w", err)
			}
		}

		// Register this target's outputs for any later-planned dependents.
		declaredByTarget[t.Name] = cloneStringMap(declaredOutputs)
		for _, a := range planActions {
			for _, outPath := range a.Outputs {
				producerForPath[outPath] = a.ID
			}
		}
	}

	// 7. Resolve sealed inputs (secrets) before shutting down plugins.
	resolvedSecrets, err := resolveSecrets(ctx, graph, mgr)
	if err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}

	return &PlanResult{Graph: graph, Targets: targets, ResolvedSecrets: resolvedSecrets}, nil
}

// Execute runs a previously planned DAG.
func (c *Coordinator) Execute(ctx context.Context, plan *PlanResult) (*BuildResult, error) {
	workers := c.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	executor := &dag.Executor{Store: c.Store, Workers: workers, ResolvedSecrets: plan.ResolvedSecrets, SubprocessStdout: c.SubprocessStdout}
	execResult, err := executor.Execute(ctx, plan.Graph)
	if err != nil {
		return nil, fmt.Errorf("coordinator: execution: %w", err)
	}

	br := buildResultFrom(execResult)
	br.Graph = plan.Graph
	br.ExecResult = execResult
	br.Targets = plan.Targets
	return br, nil
}

// Build orchestrates the full build pipeline: Plan() + Execute() + bundle plugins.
func (c *Coordinator) Build(ctx context.Context, targetNames []string) (*BuildResult, error) {
	plan, err := c.Plan(ctx, targetNames)
	if err != nil {
		return nil, err
	}
	result, err := c.Execute(ctx, plan)
	if err != nil {
		return result, err
	}

	// Bundle any plugin directories whose targets were built.
	if err := c.bundlePlugins(ctx, targetNames); err != nil {
		return result, fmt.Errorf("coordinator: bundling plugins: %w", err)
	}

	return result, nil
}

// bundlePlugins checks if any of the built targets belong to a plugin
// directory (as declared by PluginDirs from the config loader). For each
// match, it bundles the directory into CAS via the plugin resolver.
func (c *Coordinator) bundlePlugins(ctx context.Context, targetNames []string) error {
	if len(c.Config.PluginDirs) == 0 {
		return nil
	}

	// Expand wildcards in target names to get actual target names. No
	// error on zero matches here — bundling is a side effect of a
	// already-validated build invocation.
	tnames := make([]string, 0, len(c.Config.Targets))
	for _, t := range c.Config.Targets {
		tnames = append(tnames, t.Name)
	}
	expanded, _ := expandTargetWildcards(targetNames, tnames, false)

	// Check which plugin directories had targets built.
	home, _ := os.UserHomeDir()
	resolver := &PluginResolver{
		Store:       c.Store,
		ProjectRoot: c.ProjectRoot,
		CacheDir:    filepath.Join(home, ".mu", "plugins"),
	}

	for _, pluginDir := range c.Config.PluginDirs {
		prefix := "//" + filepath.ToSlash(pluginDir)
		for _, tname := range expanded {
			if strings.HasPrefix(tname, prefix) {
				// This plugin directory had a target built — bundle it.
				manifest, err := config.LoadPluginManifest(filepath.Join(c.ProjectRoot, pluginDir))
				if err != nil {
					return fmt.Errorf("plugin dir %s: %w", pluginDir, err)
				}
				dirPath := filepath.Join(c.ProjectRoot, pluginDir)
				dgst, err := resolver.bundleDir(ctx, dirPath, manifest.Plugin.Files)
				if err != nil {
					return fmt.Errorf("bundle %s: %w", pluginDir, err)
				}
				// Extract so it's ready for use.
				name := filepath.Base(pluginDir)
				if _, err := resolver.extractDirFromCAS(ctx, name, dgst); err != nil {
					return fmt.Errorf("extract %s: %w", pluginDir, err)
				}
				break // only bundle once per plugin dir
			}
		}
	}
	return nil
}

// ObserveResult holds the observation result for a single target.
// The Current field contains the observed state as reported by the plugin.
// Convergence decisions are made downstream (by pudl), not here.
type ObserveResult struct {
	Target  string         `json:"target"`
	Current map[string]any `json:"current,omitempty"` // observed state from plugin
	Error   string         `json:"error,omitempty"`   // non-empty if observation failed
}

// Observe checks the current state of the given targets by sending observe
// requests to their plugins. Each plugin reports the observed state as
// structured data. Convergence decisions are made downstream (by pudl),
// not by mu.
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

	// Collect which toolchains and secret schemes we need.
	neededPlugins := make(map[string]bool)
	for _, t := range targets {
		if t.Toolchain != "shell" {
			neededPlugins[t.Toolchain] = true
		}
		for _, ref := range t.SealedInputs {
			scheme, _, ok := parseSecretRef(ref)
			if ok {
				neededPlugins[scheme] = true
			}
		}
	}

	// 3. Start plugins for needed toolchains and secret providers.
	mgr := plugin.NewManager(c.ProjectRoot)

	if len(neededPlugins) > 0 {
		if needsScriptRuntime(c.Config.Plugins, c.ProjectRoot) {
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
			if neededPlugins[rp.Def.Name] {
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

	// 4. Resolve sealed inputs for all targets that declare them.
	targetSecrets := make(map[string]map[string]string) // target name → env name → resolved value
	for _, t := range targets {
		if len(t.SealedInputs) == 0 {
			continue
		}
		secrets := make(map[string]string, len(t.SealedInputs))
		for envName, ref := range t.SealedInputs {
			scheme, path, ok := parseSecretRef(ref)
			if !ok {
				return nil, fmt.Errorf("target %q: sealed input %q: invalid reference %q (expected scheme:path)", t.Name, envName, ref)
			}
			value, err := mgr.ResolveSecret(ctx, scheme, path)
			if err != nil {
				return nil, fmt.Errorf("target %q: sealed input %q: %w", t.Name, envName, err)
			}
			secrets[envName] = value
		}
		targetSecrets[t.Name] = secrets
	}

	// 5. Send observe request for each target.
	var results []ObserveResult
	for _, t := range targets {
		if t.Toolchain == "shell" {
			// Shell targets: check for observe_command in config.
			if t.Config != nil {
				if obsCmd, ok := t.Config["observe_command"]; ok {
					if cmdSlice, ok := obsCmd.([]any); ok && len(cmdSlice) > 0 {
						result := observeViaCommand(ctx, t, cmdSlice, c.ProjectRoot)
						results = append(results, result)
						continue
					}
				}
			}
			// Kit targets (shell with deps, no observe_command): aggregate
			// dependency state for downstream consumption.
			if len(t.Deps) > 0 {
				results = append(results, aggregateKitState(t.Name, t.Deps, results))
				continue
			}
			results = append(results, ObserveResult{Target: t.Name})
			continue
		}

		ti := plugin.TargetInfo{
			Name:      t.Name,
			Toolchain: t.Toolchain,
			Sources:   t.Sources,
			Config:    t.Config,
		}

		resp, err := mgr.Observe(ctx, t.Toolchain, ti, registry.ArtifactsMap(t.Toolchain), targetSecrets[t.Name])
		if err != nil {
			return nil, fmt.Errorf("coordinator: observing target %q: %w", t.Name, err)
		}

		results = append(results, ObserveResult{
			Target:  t.Name,
			Current: resp.Current,
		})
	}

	return results, nil
}

// observeViaCommand runs an observe_command for a shell target and returns
// the result. The command's stdout is captured as the observed state.
func observeViaCommand(ctx context.Context, t config.Target, cmdSlice []any, projectRoot string) ObserveResult {
	args := make([]string, 0, len(cmdSlice))
	for _, item := range cmdSlice {
		if s, ok := item.(string); ok {
			args = append(args, s)
		}
	}
	if len(args) == 0 {
		return ObserveResult{Target: t.Name}
	}

	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir = projectRoot
	output, err := cmd.CombinedOutput()
	current := map[string]any{
		"output":    strings.TrimSpace(string(output)),
		"exit_code": 0,
	}
	if err != nil {
		exitCode := -1
		if cmd.ProcessState != nil {
			exitCode = cmd.ProcessState.ExitCode()
		}
		current["exit_code"] = exitCode
	}
	return ObserveResult{Target: t.Name, Current: current}
}

// aggregateKitState collects the observed state of a kit's dependencies
// into a single result. The downstream consumer (pudl) decides convergence.
func aggregateKitState(name string, deps []string, results []ObserveResult) ObserveResult {
	depResults := make(map[string]any, len(deps))
	resultIndex := make(map[string]ObserveResult, len(results))
	for _, r := range results {
		resultIndex[r.Target] = r
	}
	for _, dep := range deps {
		if r, ok := resultIndex[dep]; ok {
			depResults[dep] = r.Current
		}
	}
	return ObserveResult{
		Target:  name,
		Current: map[string]any{"deps": depResults},
	}
}

// cloneStringMap returns a copy of m or nil when m is empty. Used to
// snapshot per-target state (declared outputs, producer map) so that
// downstream mutations by plugins or later planning iterations don't
// reach back into earlier targets.
func cloneStringMap(m map[string]string) map[string]string {
	if len(m) == 0 {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
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

// resolveSecrets walks all actions in the graph, finds those with SealedInputs,
// parses the scheme from each reference, and calls the appropriate plugin's
// resolve_secret method. Returns a map of actionID → envName → resolved value.
//
// Secret references use the format "scheme:path" (e.g. "pass:deploy/token").
// The scheme maps to a plugin name. The path is sent to the plugin.
func resolveSecrets(ctx context.Context, graph *dag.Graph, mgr *plugin.Manager) (map[string]map[string]string, error) {
	resolved := make(map[string]map[string]string)

	for _, a := range graph.Actions() {
		if len(a.SealedInputs) == 0 {
			continue
		}

		secrets := make(map[string]string, len(a.SealedInputs))
		for envName, ref := range a.SealedInputs {
			scheme, path, ok := parseSecretRef(ref)
			if !ok {
				return nil, fmt.Errorf("action %q: sealed input %q: invalid reference %q (expected scheme:path)", a.ID, envName, ref)
			}

			value, err := mgr.ResolveSecret(ctx, scheme, path)
			if err != nil {
				return nil, fmt.Errorf("action %q: sealed input %q: %w", a.ID, envName, err)
			}
			secrets[envName] = value
		}
		resolved[a.ID] = secrets
	}

	if len(resolved) == 0 {
		return nil, nil
	}
	return resolved, nil
}

// parseSecretRef splits a secret reference "scheme:path" into its components.
// Returns false if the reference is malformed (no colon or empty parts).
func parseSecretRef(ref string) (scheme, path string, ok bool) {
	i := strings.IndexByte(ref, ':')
	if i <= 0 || i >= len(ref)-1 {
		return "", "", false
	}
	return ref[:i], ref[i+1:], true
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
//
// Target names support the following wildcard patterns:
//   - "//pkg/..." matches all targets under //pkg/ (recursive)
//   - "*" matches any non-slash run within a single path segment
//     (e.g. "//plugins/*/build" matches //plugins/go/build,
//     //plugins/docker/build, etc.; "//plugins/*/*" matches all
//     two-segment targets under //plugins).
func (c *Coordinator) resolveTargets(names []string) ([]config.Target, error) {
	index := make(map[string]config.Target, len(c.Config.Targets))
	for _, t := range c.Config.Targets {
		index[t.Name] = t
	}

	tnames := make([]string, 0, len(index))
	for n := range index {
		tnames = append(tnames, n)
	}

	expanded, err := expandTargetWildcards(names, tnames, true)
	if err != nil {
		return nil, err
	}
	names = expanded

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

// needsScriptRuntime returns true if any plugin will need the bb runtime
// toolchain. This is determined by checking plugin manifests (for directory
// plugins) or inferring from file extensions (.bb → needs bb toolchain).
func needsScriptRuntime(plugins []config.PluginDef, projectRoot string) bool {
	for _, p := range plugins {
		if len(p.Command) > 0 {
			continue
		}
		if p.Script != "" {
			scriptPath := p.Script
			if !filepath.IsAbs(scriptPath) {
				scriptPath = filepath.Join(projectRoot, scriptPath)
			}
			// Directory plugin: check manifest toolchain.
			if info, err := os.Stat(scriptPath); err == nil && info.IsDir() {
				if manifest, err := config.LoadPluginManifest(scriptPath); err == nil {
					tc := manifest.Plugin.Toolchain
					if tc == "" {
						tc = inferPluginToolchain(manifest.Plugin.Entrypoint)
					}
					if tc == "bb" {
						return true
					}
				}
				continue
			}
			// Single-file plugin: infer from extension.
			if inferPluginToolchain(p.Script) == "bb" {
				return true
			}
		}
		if p.URL != "" && inferPluginToolchain(p.URL) == "bb" {
			return true
		}
		// Digest-only with no extension hint: assume bb for backward compat.
		if p.Digest != "" {
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
