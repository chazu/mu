package coordinator

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/chazu/mu/internal/builtin"
	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/dag"
	"github.com/chazu/mu/internal/plugin"
)

// ToolchainBuilder builds toolchains from scratch before the planning phase.
// The scratch package provides the concrete implementation.
type ToolchainBuilder interface {
	Build(ctx context.Context, cfg *config.ProjectConfig) error
}

// Coordinator orchestrates the full mu build flow.
type Coordinator struct {
	NoCache           bool
	ProjectRoot       string
	Config            *config.ProjectConfig
	Store             cas.Store
	ToolchainRegistry *ToolchainRegistry
	Builder           ToolchainBuilder // optional; set to enable toolchain scratch build
	Workers           int              // 0 = runtime.NumCPU()

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
	Graph   *dag.Graph
	Targets []config.Target // the resolved targets (for manifest metadata)

	// Plugins records the immutable identities of every external plugin used
	// to produce or execute this plan. Exact-plan consumers commit these rows
	// alongside the action graph.
	Plugins []PluginIdentity

	// SealedInputProviders is the set of resolved plugin artifacts needed to resolve
	// sealed_inputs at execute time. Planning validates capability but never
	// resolves a value, so --plan remains a reference-only operation.
	SealedInputProviders []ResolvedPlugin

	// SealedOutputProviders is the set of resolved plugin artifacts
	// needed at execute time to satisfy sealed_outputs writes. Empty if
	// no action in the graph declares sealed_outputs. Populated even
	// after the planning manager is shut down so Execute can spin up
	// a provider-only manager.
	SealedOutputProviders []ResolvedPlugin

	// SecretWritePolicy is the project's writable-ref allow-list,
	// enforced at write time as a defense-in-depth check on top of
	// the plan-time enforcement. Nil means no allow-list configured.
	SecretWritePolicy *secretWritePolicy
}

// PluginIdentity is the stable, public identity of a plugin that participated
// in a plan. Digest is empty only for mutable command plugins; guarded exact
// execution rejects such plans rather than pretending the command is pinned.
type PluginIdentity struct {
	Name            string   `json:"name"`
	Digest          string   `json:"digest"`
	Version         string   `json:"version"`
	ProtocolVersion int      `json:"protocol_version"`
	Capabilities    []string `json:"capabilities"`
}

// Plan runs the planning pipeline: build toolchains, resolve plugins, start
// plugins, resolve the target graph, and ask each plugin to plan its targets.
// Plugins are shut down before returning. The resulting Graph is ready for
// execution or inspection (--plan mode).
func (c *Coordinator) Plan(ctx context.Context, targetNames []string) (*PlanResult, error) {
	targets, err := c.resolveTargets(targetNames)
	if err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}
	needed := make(map[string]bool)
	for _, target := range targets {
		needed[target.Toolchain] = true
	}
	var planningPlugins []config.PluginDef
	for _, definition := range c.Config.Plugins {
		if needed[definition.Name] {
			planningPlugins = append(planningPlugins, definition)
		}
	}
	registry := c.ToolchainRegistry
	if registry == nil {
		registry = NewToolchainRegistry(c.Store)
	}

	// 2. Resolve plugins → CAS (hash local scripts or fetch remote ones).
	home, _ := os.UserHomeDir()
	resolver := &PluginResolver{
		Store:       c.Store,
		ProjectRoot: c.ProjectRoot,
		CacheDir:    filepath.Join(home, ".mu", "plugins"),
	}
	resolvedPlugins, err := resolver.Resolve(ctx, planningPlugins)
	if err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}

	if c.Builder != nil {
		for _, rp := range resolvedPlugins {
			if rp.Def.Toolchain != "" {
				needed[rp.Def.Toolchain] = true
			}
		}
		subset := *c.Config
		subset.Toolchains = nil
		for _, tc := range c.Config.Toolchains {
			if needed[tc.Name] {
				subset.Toolchains = append(subset.Toolchains, tc)
			}
		}
		if len(subset.Toolchains) > 0 {
			if err := c.Builder.Build(ctx, &subset); err != nil {
				return nil, fmt.Errorf("coordinator: scratch build required toolchains: %w", err)
			}
		}
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
	if resolvedNeedsRuntime(resolvedPlugins) {
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
	// The target set was resolved before any dependency bootstrap.

	// 5. Validate target configs against plugin schemas.
	for _, t := range targets {
		if t.Toolchain == "shell" || t.Toolchain == "secret-gen" {
			continue
		}
		info := mgr.DiscoverInfo(t.Toolchain)
		warnMissingConfigSchema(t, info)
		if info != nil && info.ConfigSchema != nil {
			if err := ValidateTargetConfig(t, info.ConfigSchema); err != nil {
				return nil, fmt.Errorf("coordinator: %w", err)
			}
		}
	}

	// 6. Plan each target via its toolchain plugin and resolve actions.
	graph := dag.NewGraph()

	// Cross-target artifact plumbing happens in two passes so that path
	// wiring does not depend on planning order:
	//
	//  1. Plan every target (topological order, so each plugin receives its
	//     deps' declared_outputs via DepInfo.Artifacts) and record which
	//     action produces each output path.
	//  2. Resolve every target's actions against the complete producer map.
	//     An input naming another target's output becomes a deferred input
	//     with a DependsOn edge to the producer, which orders the two even
	//     without a deps relationship.
	declaredByTarget := make(map[string]map[string]string, len(targets))
	producers := make(pathProducers)
	plannedActions := make([][]plugin.ActionSpec, len(targets))

	for i, t := range targets {
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
		} else if t.Toolchain == "secret-gen" {
			// secret-gen targets are built-in: derive a value, route it
			// through the configured secret provider via sealed_outputs.
			// Its output mode defaults to create_if_absent; apply that to
			// the target too so routing validation sees the same mode.
			t = builtin.SecretGenDefaults(t)
			targets[i] = t
			actions, declared, err := builtin.SecretGenPlan(t)
			if err != nil {
				return nil, fmt.Errorf("coordinator: %w", err)
			}
			planActions = actions
			declaredOutputs = declared
		} else {
			ti := plugin.TargetInfo{
				Name:              t.Name,
				Toolchain:         t.Toolchain,
				Sources:           t.Sources,
				Config:            t.Config,
				SealedInputs:      t.SealedInputs,
				SealedInputModes:  t.SealedInputModes,
				SealedOutputs:     t.SealedOutputs,
				SealedOutputModes: t.SealedOutputModes,
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

		// Target-level sealed maps are the complete allow-list; actions
		// claim from it explicitly (a lone action with no claims of its own
		// is filled from the target maps).
		autoClaimSingleAction(t, planActions)
		if err := validateSealedRouting(t, planActions); err != nil {
			return nil, fmt.Errorf("coordinator: target %q: %w", t.Name, err)
		}

		// Prefix action IDs with target name to avoid cross-target collisions.
		prefixActions(t.Name, planActions)

		plannedActions[i] = planActions
		declaredByTarget[t.Name] = cloneStringMap(declaredOutputs)
		if err := producers.addTarget(c.ProjectRoot, t.Name, planActions); err != nil {
			return nil, fmt.Errorf("coordinator: %w", err)
		}
	}

	// leavesByTarget records each resolved target's leaf actions (no
	// successor inside its own subgraph) so dependents can be ordered after
	// them. A target with no actions forwards its own deps' leaves.
	leavesByTarget := make(map[string][]string, len(targets))
	// pathDeps records, per consumer target, the targets whose outputs it
	// reads by path; together with deps it must stay acyclic.
	pathDeps := make(map[string][]string, len(targets))

	for i, t := range targets {
		// A target's own outputs are excluded: its actions wire to each
		// other via {action:id}, not through the cross-target path map.
		resolved, err := Resolve(ctx, plannedActions[i], c.ProjectRoot, c.Store, producers.actionIDsExcept(t.Name), c.toolchainLookup(registry))
		if err != nil {
			return nil, fmt.Errorf("coordinator: resolving target %q: %w", t.Name, err)
		}
		pathDeps[t.Name] = producers.useProducerPaths(c.ProjectRoot, resolved)

		// Target deps order execution: every root action of t waits on
		// every leaf action of each dep. Deps are resolved first
		// (resolveTargets sorts leaves first and pulls in unselected deps).
		depLeaves := targetDepLeaves(t.Deps, leavesByTarget)
		roots, leaves := subgraphEnds(resolved)
		addDependsOn(resolved, roots, depLeaves)
		if len(resolved) == 0 {
			leaves = depLeaves
		}
		leavesByTarget[t.Name] = leaves

		for _, a := range resolved {
			if err := graph.AddAction(a); err != nil {
				return nil, fmt.Errorf("coordinator: building DAG: %w", err)
			}
		}
	}

	if err := checkTargetCycles(targets, pathDeps); err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}

	// Action-level sealed claims can introduce providers unknown until planning.
	// Resolve only those providers now and retain these exact artifacts for Execute.
	known := make(map[string]bool)
	for _, rp := range resolvedPlugins {
		known[rp.Def.Name] = true
	}
	requiredProviders := make(map[string]bool)
	for _, action := range graph.Actions() {
		for _, refs := range []map[string]string{action.SealedInputs, action.SealedOutputs} {
			for _, ref := range refs {
				scheme, _, ok := parseSecretRef(ref)
				if ok && scheme != "env" && !known[scheme] {
					requiredProviders[scheme] = true
				}
			}
		}
	}
	var providerDefinitions []config.PluginDef
	for _, definition := range c.Config.Plugins {
		if requiredProviders[definition.Name] {
			providerDefinitions = append(providerDefinitions, definition)
		}
	}
	if len(providerDefinitions) > 0 {
		providers, err := resolver.Resolve(ctx, providerDefinitions)
		if err != nil {
			return nil, err
		}
		if resolvedNeedsRuntime(providers) {
			bb, err := c.resolveScriptRuntime(ctx, registry)
			if err != nil {
				return nil, err
			}
			mgr.SetScriptRuntime(bb)
		}
		for _, rp := range providers {
			if err := mgr.Register(rp.Def); err != nil {
				return nil, err
			}
		}
		if err := mgr.Start(ctx); err != nil {
			return nil, err
		}
		resolvedPlugins = append(resolvedPlugins, providers...)
	}

	// 7. Identify provider plugins needed at execute time for any
	//    sealed_outputs writes. We collect schemes from the graph and
	//    capture the plugin defs so Execute can start a provider-only
	//    manager after this Plan() returns.
	inputProviderDefs := collectSealedProviders(graph, resolvedPlugins, true)
	outputProviderDefs := collectSealedProviders(graph, resolvedPlugins, false)

	// 8. Enforce the secret write-policy at plan time. We check every
	//    sealed_output ref in the graph against the configured allow-list
	//    so that a forbidden ref aborts before any execute-time work
	//    happens (and never spins up the provider manager).
	policy, err := newSecretWritePolicy(c.Config.Secrets)
	if err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}
	if policy != nil {
		for _, a := range graph.Actions() {
			for _, ref := range a.SealedOutputs {
				if !policy.Allow(ref) {
					return nil, fmt.Errorf("coordinator: action %q: sealed_output ref %q is not allowed by secrets.writable_refs (%s)", a.ID, ref, policy.Description())
				}
			}
		}
	}

	// 9. Validate sealed provider capabilities without resolving any value.
	// Policy runs first so a forbidden destination fails on the authority that
	// denied it even when no provider is installed.
	if err := validateSealedProviderCapabilities(graph, mgr); err != nil {
		return nil, fmt.Errorf("coordinator: %w", err)
	}

	identities := collectPluginIdentities(targets, graph, resolvedPlugins, mgr)
	return &PlanResult{
		Graph: graph, Targets: targets,
		Plugins:              identities,
		SealedInputProviders: inputProviderDefs, SealedOutputProviders: outputProviderDefs,
		SecretWritePolicy: policy,
	}, nil
}

// collectSealedProviders returns plugin definitions needed at execute time for
// either input resolution or output storage. The built-in env input scheme has
// no definition and therefore contributes no row.
func collectSealedProviders(graph *dag.Graph, plugins []ResolvedPlugin, inputs bool) []ResolvedPlugin {
	schemes := make(map[string]struct{})
	for _, a := range graph.Actions() {
		refs := a.SealedOutputs
		if inputs {
			refs = a.SealedInputs
		}
		for _, ref := range refs {
			scheme, _, ok := parseSecretRef(ref)
			if !ok {
				continue
			}
			schemes[scheme] = struct{}{}
		}
	}
	if len(schemes) == 0 {
		return nil
	}
	out := make([]ResolvedPlugin, 0, len(schemes))
	for _, p := range plugins {
		if _, want := schemes[p.Def.Name]; want {
			out = append(out, p)
		}
	}
	return out
}

func collectPluginIdentities(targets []config.Target, graph *dag.Graph, resolved []ResolvedPlugin, mgr *plugin.Manager) []PluginIdentity {
	used := make(map[string]struct{})
	for _, target := range targets {
		if target.Toolchain != "" && target.Toolchain != "shell" && target.Toolchain != "secret-gen" {
			used[target.Toolchain] = struct{}{}
		}
	}
	for _, action := range graph.Actions() {
		for _, refs := range []map[string]string{action.SealedInputs, action.SealedOutputs} {
			for _, ref := range refs {
				if scheme, _, ok := parseSecretRef(ref); ok && scheme != "env" {
					used[scheme] = struct{}{}
				}
			}
		}
	}

	byName := make(map[string]ResolvedPlugin, len(resolved))
	for _, item := range resolved {
		byName[item.Def.Name] = item
	}
	names := make([]string, 0, len(used))
	for name := range used {
		names = append(names, name)
	}
	sort.Strings(names)
	identities := make([]PluginIdentity, 0, len(names))
	for _, name := range names {
		item, ok := byName[name]
		if !ok {
			continue
		}
		identity := PluginIdentity{Name: name}
		if !item.Digest.IsZero() {
			identity.Digest = item.Digest.String()
		}
		if info := mgr.DiscoverInfo(name); info != nil {
			identity.Version = info.Version
			identity.ProtocolVersion = info.ProtocolVersion
			identity.Capabilities = append([]string(nil), info.Capabilities...)
			sort.Strings(identity.Capabilities)
			if identity.Capabilities == nil {
				identity.Capabilities = []string{}
			}
		}
		identities = append(identities, identity)
	}
	return identities
}

func validateSealedProviderCapabilities(graph *dag.Graph, mgr *plugin.Manager) error {
	for _, action := range graph.Actions() {
		for name, ref := range action.SealedInputs {
			scheme, _, ok := parseSecretRef(ref)
			if !ok {
				return fmt.Errorf("action %q: sealed input %q: invalid reference %q (expected scheme:path)", action.ID, name, ref)
			}
			info := mgr.DiscoverInfo(scheme)
			if info == nil && scheme == "env" {
				continue
			}
			if info == nil {
				return fmt.Errorf("action %q: sealed input %q: no plugin registered for secret scheme %q", action.ID, name, scheme)
			}
			if !info.HasCapability("resolve_secret") {
				return fmt.Errorf("action %q: sealed input %q: plugin %q does not support resolve_secret", action.ID, name, scheme)
			}
		}
		for name, ref := range action.SealedOutputs {
			scheme, _, ok := parseSecretRef(ref)
			if !ok {
				return fmt.Errorf("action %q: sealed output %q: invalid reference %q (expected scheme:path)", action.ID, name, ref)
			}
			info := mgr.DiscoverInfo(scheme)
			if info == nil {
				return fmt.Errorf("action %q: sealed output %q: no plugin registered for secret scheme %q", action.ID, name, scheme)
			}
			if !info.HasCapability("store_secret") {
				return fmt.Errorf("action %q: sealed output %q: plugin %q does not support store_secret", action.ID, name, scheme)
			}
		}
	}
	return nil
}

func graphHasSealedInputs(graph *dag.Graph) bool {
	for _, action := range graph.Actions() {
		if len(action.SealedInputs) > 0 {
			return true
		}
	}
	return false
}

func graphHasSealedOutputs(graph *dag.Graph) bool {
	for _, action := range graph.Actions() {
		if len(action.SealedOutputs) > 0 {
			return true
		}
	}
	return false
}

func mergePluginDefs(groups ...[]ResolvedPlugin) []ResolvedPlugin {
	seen := map[string]struct{}{}
	var merged []ResolvedPlugin
	for _, group := range groups {
		for _, resolved := range group {
			if _, exists := seen[resolved.Def.Name]; exists {
				continue
			}
			seen[resolved.Def.Name] = struct{}{}
			merged = append(merged, resolved)
		}
	}
	return merged
}

// startSealedProviderManager spins up the provider-only plugin manager used for
// execute-time input resolution and output storage. The caller must Close it.
func (c *Coordinator) startSealedProviderManager(ctx context.Context, resolved []ResolvedPlugin) (*plugin.Manager, error) {
	mgr := plugin.NewManager(c.ProjectRoot)
	if c.Verbose {
		sink := c.VerboseSink
		if sink == nil {
			sink = os.Stderr
		}
		mgr.SetVerbose(sink)
	}
	needsRuntime := false
	for _, item := range resolved {
		if item.Def.Toolchain != "" {
			needsRuntime = true
			break
		}
	}
	if needsRuntime {
		bbPath, err := c.resolveScriptRuntime(ctx, c.ToolchainRegistry)
		if err != nil {
			return nil, err
		}
		mgr.SetScriptRuntime(bbPath)
	}
	for _, rp := range resolved {
		if err := mgr.Register(rp.Def); err != nil {
			return nil, fmt.Errorf("register provider plugin %q: %w", rp.Def.Name, err)
		}
	}
	if err := mgr.Start(ctx); err != nil {
		return nil, fmt.Errorf("starting provider plugins: %w", err)
	}
	return mgr, nil
}

// Execute runs a previously planned DAG.
func (c *Coordinator) Execute(ctx context.Context, plan *PlanResult) (*BuildResult, error) {
	workers := c.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}

	// Start providers only at the execution boundary. This is where sealed input
	// values are first resolved; plan/approval paths carry references only.
	var writer dag.SealedOutputWriter
	var resolver dag.SealedInputResolver
	if graphHasSealedInputs(plan.Graph) || graphHasSealedOutputs(plan.Graph) {
		providerDefs := mergePluginDefs(plan.SealedInputProviders, plan.SealedOutputProviders)
		pmgr, err := c.startSealedProviderManager(ctx, providerDefs)
		if err != nil {
			return nil, fmt.Errorf("coordinator: starting sealed providers: %w", err)
		}
		defer pmgr.Close()
		if graphHasSealedInputs(plan.Graph) {
			resolver = func(ctx context.Context, action *dag.Action) (map[string]string, error) {
				return resolveActionSecrets(ctx, action, pmgr)
			}
		}
		if graphHasSealedOutputs(plan.Graph) {
			policy := plan.SecretWritePolicy
			writer = func(ctx context.Context, ref, value, mode string) error {
				if !policy.Allow(ref) {
					return fmt.Errorf("sealed-output ref %q is not allowed by secrets.writable_refs (%s)", ref, policy.Description())
				}
				scheme, path, ok := parseSecretRef(ref)
				if !ok {
					return fmt.Errorf("invalid sealed-output ref %q (expected scheme:path)", ref)
				}
				if mode == "" {
					mode = plugin.StoreSecretModeOverwrite
				}
				return pmgr.StoreSecret(ctx, scheme, path, value, mode)
			}
		}
	}

	executor := &dag.Executor{
		NoCache:             c.NoCache,
		Store:               c.Store,
		Workers:             workers,
		SealedInputResolver: resolver,
		SubprocessStdout:    c.SubprocessStdout,
		SealedOutputWriter:  writer,
	}
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
	return c.BuildValidated(ctx, targetNames, nil)
}

// BuildValidated plans once, invokes validate before any execution boundary,
// then executes and finalizes that exact PlanResult. A validation error stops
// before sealed providers, actions, or plugin bundling are invoked.
func (c *Coordinator) BuildValidated(ctx context.Context, targetNames []string, validate func(*PlanResult) error) (*BuildResult, error) {
	plan, err := c.Plan(ctx, targetNames)
	if err != nil {
		return nil, err
	}
	if validate != nil {
		if err := validate(plan); err != nil {
			return nil, err
		}
	}
	result, err := c.Execute(ctx, plan)
	if err != nil {
		return result, err
	}

	return result, nil
}

// cloneStringMap returns a copy of m or nil when m is empty. Used to
// snapshot per-target state (declared outputs, producer map) so that
// downstream mutations by plugins or later planning iterations don't
// reach back into earlier targets.
// projectPathKey normalises a path to the project-relative form used to
// match one action's outputs against another action's inputs. Outputs are
// relative to the producing action's work_dir; inputs are relative to the
// project root (pass workDir ""). Absolute paths inside projectRoot are made
// relative, so "./x", "x", and "<root>/x" all yield the same key.
func projectPathKey(projectRoot, workDir, p string) string {
	if filepath.IsAbs(p) {
		if rel, err := filepath.Rel(projectRoot, p); err == nil && filepath.IsLocal(rel) {
			return rel
		}
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(workDir, p))
}

// pathProducer is the action that declares an output path, with the path
// spelled as the producer declared it (relative to its own work_dir).
type pathProducer struct {
	target, actionID, path string
}

// pathProducers maps projectPathKey-normalised output paths to their
// producers across every planned target.
type pathProducers map[string]pathProducer

// addTarget records the output paths declared by target's (already
// prefixed) actions. Each path has exactly one producer:
//
//   - Two targets declaring the same path is an error: consumers could not
//     tell which to wire to and the two actions would race on the file.
//   - Within one target, actions may share a path only when they are
//     ordered by the plugin's dependency edges (depends_on or {action:id}
//     inputs, transitively), e.g. a generate step followed by an in-place
//     rewrite. The last writer in that chain is the producer. Unordered
//     writers of the same path are an error.
func (p pathProducers) addTarget(projectRoot, target string, actions []plugin.ActionSpec) error {
	var keys []string
	writers := make(map[string][]pathProducer)
	for _, a := range actions {
		for _, outPath := range a.Outputs {
			key := projectPathKey(projectRoot, a.WorkDir, outPath)
			ws := writers[key]
			if len(ws) > 0 && ws[len(ws)-1].actionID == a.ID {
				continue // same action listing one path twice
			}
			if len(ws) == 0 {
				keys = append(keys, key)
			}
			writers[key] = append(ws, pathProducer{target: target, actionID: a.ID, path: outPath})
		}
	}

	ancestors := actionAncestors(actions)
	for _, key := range keys {
		ws := writers[key]
		last := ws[0]
		for _, w := range ws[1:] {
			if ancestors[w.actionID][last.actionID] {
				last = w
			}
		}
		// The last writer must follow every other writer of the path.
		for _, w := range ws {
			if w.actionID != last.actionID && !ancestors[last.actionID][w.actionID] {
				return fmt.Errorf("target %q: actions %q and %q both declare output %q without a dependency between them",
					target, last.actionID, w.actionID, key)
			}
		}
		if prev, ok := p[key]; ok {
			return fmt.Errorf("output %q is declared by both target %q (action %q) and target %q (action %q)",
				key, prev.target, prev.actionID, target, last.actionID)
		}
		p[key] = last
	}
	return nil
}

// actionAncestors returns, for each action ID, the set of actions in the same
// subgraph it transitively depends on via depends_on or {action:id} inputs.
func actionAncestors(actions []plugin.ActionSpec) map[string]map[string]bool {
	direct := make(map[string][]string, len(actions))
	for _, a := range actions {
		deps := append([]string(nil), a.DependsOn...)
		for _, v := range a.Inputs {
			if isActionRef(v) {
				deps = append(deps, actionRefID(v))
			}
		}
		direct[a.ID] = deps
	}
	out := make(map[string]map[string]bool, len(actions))
	var visit func(id string) map[string]bool
	visit = func(id string) map[string]bool {
		if set, ok := out[id]; ok {
			return set
		}
		set := make(map[string]bool)
		out[id] = set // guards against cycles; the DAG rejects those later
		for _, d := range direct[id] {
			if _, local := direct[d]; !local {
				continue
			}
			set[d] = true
			for anc := range visit(d) {
				set[anc] = true
			}
		}
		return set
	}
	for _, a := range actions {
		visit(a.ID)
	}
	return out
}

// actionIDsExcept returns the key -> producing action ID map Resolve uses for
// cross-target wiring, leaving out the outputs of target self.
func (p pathProducers) actionIDsExcept(self string) map[string]string {
	out := make(map[string]string, len(p))
	for key, prod := range p {
		if prod.target != self {
			out[key] = prod.actionID
		}
	}
	return out
}

// useProducerPaths rewrites the OutputPath of every cross-target deferred
// input to the producer's own spelling of the path. Resolve records the
// consumer's project-relative spelling, but the executor looks the output up
// among the producer's declared outputs and, failing that, opens it relative
// to the producer's work_dir; both need the producer's form. It returns the
// sorted, de-duplicated producer targets the actions read from.
func (p pathProducers) useProducerPaths(projectRoot string, actions []*dag.Action) []string {
	seen := make(map[string]bool)
	var targets []string
	for _, a := range actions {
		for name, src := range a.InputSources {
			if src.OutputPath == "" {
				continue
			}
			prod, ok := p[projectPathKey(projectRoot, "", src.OutputPath)]
			if !ok || prod.actionID != src.ProducerID {
				continue
			}
			src.OutputPath = prod.path
			a.InputSources[name] = src
			if !seen[prod.target] {
				seen[prod.target] = true
				targets = append(targets, prod.target)
			}
		}
	}
	sort.Strings(targets)
	return targets
}

// checkTargetCycles reports a cycle in the target graph formed by deps plus
// cross-target path inputs (consumer -> producer). Deps alone are already
// acyclic (resolveTargets), so any cycle found involves a path input.
func checkTargetCycles(targets []config.Target, pathDeps map[string][]string) error {
	edges := make(map[string][]string, len(targets))
	names := make([]string, 0, len(targets))
	for _, t := range targets {
		names = append(names, t.Name)
		seen := make(map[string]bool)
		for _, d := range append(append([]string(nil), t.Deps...), pathDeps[t.Name]...) {
			if !seen[d] {
				seen[d] = true
				edges[t.Name] = append(edges[t.Name], d)
			}
		}
		sort.Strings(edges[t.Name])
	}
	sort.Strings(names)

	const (
		unvisited = iota
		visiting
		done
	)
	state := make(map[string]int, len(targets))
	var stack []string
	var visit func(string) error
	visit = func(name string) error {
		state[name] = visiting
		stack = append(stack, name)
		for _, next := range edges[name] {
			switch state[next] {
			case visiting:
				cycle := append(append([]string(nil), stack[slices.Index(stack, next):]...), next)
				return fmt.Errorf("target cycle through cross-target inputs: %s", strings.Join(cycle, " -> "))
			case unvisited:
				if err := visit(next); err != nil {
					return err
				}
			}
		}
		stack = stack[:len(stack)-1]
		state[name] = done
		return nil
	}
	for _, name := range names {
		if state[name] == unvisited {
			if err := visit(name); err != nil {
				return err
			}
		}
	}
	return nil
}

// subgraphEnds returns the sorted IDs of a target's root actions (no
// predecessor inside the subgraph) and leaf actions (no successor inside it).
// Edges to actions outside the subgraph are ignored.
func subgraphEnds(actions []*dag.Action) (roots, leaves []string) {
	ids := make(map[string]bool, len(actions))
	for _, a := range actions {
		ids[a.ID] = true
	}
	hasPred := make(map[string]bool)
	hasSucc := make(map[string]bool)
	for _, a := range actions {
		for _, d := range a.DependsOn {
			if ids[d] && d != a.ID {
				hasPred[a.ID] = true
				hasSucc[d] = true
			}
		}
	}
	for _, a := range actions {
		if !hasPred[a.ID] {
			roots = append(roots, a.ID)
		}
		if !hasSucc[a.ID] {
			leaves = append(leaves, a.ID)
		}
	}
	sort.Strings(roots)
	sort.Strings(leaves)
	return roots, leaves
}

// targetDepLeaves returns the sorted, de-duplicated leaf action IDs of the
// given dependency targets.
func targetDepLeaves(deps []string, leavesByTarget map[string][]string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, dep := range deps {
		for _, id := range leavesByTarget[dep] {
			if !seen[id] {
				seen[id] = true
				out = append(out, id)
			}
		}
	}
	sort.Strings(out)
	return out
}

// addDependsOn appends each of edges (already sorted) to the DependsOn of
// the actions named by ids, skipping edges an action already declares.
func addDependsOn(actions []*dag.Action, ids, edges []string) {
	if len(edges) == 0 {
		return
	}
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	for _, a := range actions {
		if !want[a.ID] {
			continue
		}
		have := make(map[string]bool, len(a.DependsOn))
		merged := append([]string(nil), a.DependsOn...)
		for _, d := range merged {
			have[d] = true
		}
		for _, e := range edges {
			if !have[e] {
				have[e] = true
				merged = append(merged, e)
			}
		}
		a.DependsOn = merged
	}
}

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
	prefix := target + ":"
	for i := range actions {
		actions[i].ID = prefix + actions[i].ID
		for j := range actions[i].DependsOn {
			// Don't re-prefix cross-target deps (already fully qualified).
			if !strings.Contains(actions[i].DependsOn[j], ":") {
				actions[i].DependsOn[j] = prefix + actions[i].DependsOn[j]
			}
		}
		// "{action:id}" inputs name subgraph-local IDs, like depends_on.
		for name, value := range actions[i].Inputs {
			if id := actionRefID(value); isActionRef(value) && !strings.Contains(id, ":") {
				actions[i].Inputs[name] = "{action:" + prefix + id + "}"
			}
		}
	}
}

// resolveActionSecrets resolves only the sealed inputs of the action that the
// scheduler is about to run. Keeping this at the action boundary avoids
// provider reads for cached or dependency-cancelled actions.
func resolveActionSecrets(ctx context.Context, action *dag.Action, mgr *plugin.Manager) (map[string]string, error) {
	if len(action.SealedInputs) == 0 {
		return nil, nil
	}
	secrets := make(map[string]string, len(action.SealedInputs))
	for envName, ref := range action.SealedInputs {
		scheme, path, ok := parseSecretRef(ref)
		if !ok {
			return nil, fmt.Errorf("action %q: sealed input %q: invalid reference %q (expected scheme:path)", action.ID, envName, ref)
		}
		value, err := mgr.ResolveSecret(ctx, scheme, path)
		if err != nil {
			return nil, fmt.Errorf("action %q: sealed input %q: %w", action.ID, envName, err)
		}
		secrets[envName] = value
	}
	return secrets, nil
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
	if m == nil && c.Builder != nil {
		subset := *c.Config
		subset.Toolchains = nil
		for _, tc := range c.Config.Toolchains {
			if tc.Name == "bb" {
				subset.Toolchains = append(subset.Toolchains, tc)
			}
		}
		if len(subset.Toolchains) > 0 {
			if err := c.Builder.Build(ctx, &subset); err != nil {
				return "", fmt.Errorf("required bb runtime: %w", err)
			}
		}
		m = registry.Get("bb")
	}
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

func resolvedNeedsRuntime(plugins []ResolvedPlugin) bool {
	for _, p := range plugins {
		if p.Def.Toolchain == "bb" {
			return true
		}
	}
	return false
}
