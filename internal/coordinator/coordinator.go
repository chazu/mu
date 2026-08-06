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
	"time"

	"github.com/chazu/mu/internal/builtin"
	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/coordinator/discovercache"
	"github.com/chazu/mu/internal/dag"
	"github.com/chazu/mu/internal/pithvm"
	"github.com/chazu/mu/internal/plugin"
	"github.com/chazu/pith"
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
		if t.Toolchain == "shell" || t.Toolchain == "secret-gen" {
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
		} else if t.Toolchain == "secret-gen" {
			// secret-gen targets are built-in: derive a value, route it
			// through the configured secret provider via sealed_outputs.
			actions, declared, err := builtin.SecretGenPlan(t)
			if err != nil {
				return nil, fmt.Errorf("coordinator: %w", err)
			}
			planActions = actions
			declaredOutputs = declared
		} else if len(t.Plan) > 0 {
			// Inline pith plan program — run the VM to emit action specs.
			vm := pith.New(ctx)
			buf := &pithvm.ActionBuffer{}
			pithvm.RegisterPlanDrivers(vm, t.Config, buf)
			if err := vm.Run(t.Plan); err != nil {
				return nil, fmt.Errorf("coordinator: target %q plan: %w", t.Name, err)
			}
			// Convert emitted action maps to ActionSpecs.
			for _, m := range buf.Actions {
				spec := mapToActionSpec(m)
				planActions = append(planActions, spec)
			}
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
				SealedRouting:     t.SealedRouting,
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

		// Inject a synthetic transform action if the target has a Transform program.
		// The transform must wait for all dependency targets to complete,
		// then plan actions within this target depend on _transform.
		if len(t.Transform) > 0 {
			var depActionIDs []string
			for _, depName := range t.Deps {
				prefix := depName + ":"
				for _, a := range graph.Actions() {
					if strings.HasPrefix(a.ID, prefix) {
						depActionIDs = append(depActionIDs, a.ID)
					}
				}
			}
			transformAction := plugin.ActionSpec{
				ID:        "_transform",
				Body:      t.Transform,
				Outputs:   []string{},
				DependsOn: depActionIDs,
			}
			for i := range planActions {
				planActions[i].DependsOn = append(planActions[i].DependsOn, "_transform")
			}
			planActions = append([]plugin.ActionSpec{transformAction}, planActions...)
		}

		if t.SealedRouting == "strict" {
			if err := validateStrictSealedRouting(t, planActions); err != nil {
				return nil, fmt.Errorf("coordinator: target %q: %w", t.Name, err)
			}
		} else {
			// Apply target-level sealed_inputs to plan/transform-emitted actions.
			// The plugin path threads these through TargetInfo, but pith plan
			// programs emit bare action specs, so the coordinator attaches the
			// target's sealed_inputs (and modes) to any emitted action that did
			// not declare its own. This is what lets a pith body read a secret via
			// secret/get / env-mode injection.
			if len(t.SealedInputs) > 0 {
				for i := range planActions {
					if len(planActions[i].SealedInputs) == 0 {
						planActions[i].SealedInputs = cloneStringMap(t.SealedInputs)
						if len(t.SealedInputModes) > 0 {
							planActions[i].SealedInputModes = cloneStringMap(t.SealedInputModes)
						}
					}
				}
			}

			// Apply target-level sealed_outputs as a convenience: if the target
			// declares them and no plan action set its own, attach them to the
			// single action in the plan. Multi-action plans must have the
			// plugin route sealed_outputs to the correct action explicitly.
			if len(t.SealedOutputs) > 0 {
				anyExplicit := false
				for _, a := range planActions {
					if len(a.SealedOutputs) > 0 {
						anyExplicit = true
						break
					}
				}
				if !anyExplicit {
					if len(planActions) != 1 {
						return nil, fmt.Errorf("coordinator: target %q: sealed_outputs declared at target level but plan has %d actions; route them explicitly via the plugin", t.Name, len(planActions))
					}
					planActions[0].SealedOutputs = cloneStringMap(t.SealedOutputs)
					planActions[0].SealedOutputModes = cloneStringMap(t.SealedOutputModes)
				}
			}
		}

		// Prefix action IDs with target name to avoid cross-target collisions.
		prefixActions(t.Name, planActions)

		// Snapshot this target's output producers BEFORE Resolve runs:
		// the target's own actions need to self-reference via {action:id}
		// for intra-target wiring, not through the cross-target path map.
		crossTargetProducers := cloneStringMap(producerForPath)

		resolved, err := Resolve(ctx, planActions, c.ProjectRoot, c.Store, crossTargetProducers)
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
		if target.Toolchain != "" && target.Toolchain != "shell" && target.Toolchain != "secret-gen" && len(target.Plan) == 0 {
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

// Build orchestrates the full build pipeline: Plan() + Execute() + advice + bundle plugins.
func (c *Coordinator) Build(ctx context.Context, targetNames []string) (*BuildResult, error) {
	return c.BuildValidated(ctx, targetNames, nil)
}

// BuildValidated plans once, invokes validate before any execution boundary,
// then executes and finalizes that exact PlanResult. A validation error stops
// before sealed providers, actions, advice, or plugin bundling are invoked.
func (c *Coordinator) BuildValidated(ctx context.Context, targetNames []string, validate func(*PlanResult) error) (*BuildResult, error) {
	start := time.Now()

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

	// Run after-build advice (non-fatal).
	c.runAfterBuildAdvice(ctx, result, targetNames, time.Since(start))

	// Bundle any plugin directories whose targets were built.
	if err := c.bundlePlugins(ctx, targetNames); err != nil {
		return result, fmt.Errorf("coordinator: bundling plugins: %w", err)
	}

	return result, nil
}

// runAfterBuildAdvice starts advice plugins, sends them the build manifest,
// and shuts them down. Errors are logged but never fail the build.
func (c *Coordinator) runAfterBuildAdvice(ctx context.Context, result *BuildResult, targetNames []string, elapsed time.Duration) {
	if len(c.Config.Advice) == 0 {
		return
	}

	// Filter to advice defs that want "after-build".
	var afterBuild []config.AdviceDef
	for _, a := range c.Config.Advice {
		for _, phase := range a.Phases {
			if phase == "after-build" {
				afterBuild = append(afterBuild, a)
				break
			}
		}
	}
	if len(afterBuild) == 0 {
		return
	}

	// Resolve + start only the plugins referenced by advice defs.
	home, _ := os.UserHomeDir()
	resolver := &PluginResolver{
		Store:       c.Store,
		ProjectRoot: c.ProjectRoot,
		CacheDir:    filepath.Join(home, ".mu", "plugins"),
	}

	// Find the PluginDef for each advice plugin.
	pluginsByName := make(map[string]config.PluginDef)
	for _, p := range c.Config.Plugins {
		pluginsByName[p.Name] = p
	}

	var toResolve []config.PluginDef
	for _, a := range afterBuild {
		if pd, ok := pluginsByName[a.Plugin]; ok {
			toResolve = append(toResolve, pd)
		} else {
			fmt.Fprintf(os.Stderr, "  advice: plugin %q not found in plugins[]\n", a.Plugin)
		}
	}
	if len(toResolve) == 0 {
		return
	}

	resolved, err := resolver.Resolve(ctx, toResolve)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  advice: resolve plugins: %v\n", err)
		return
	}

	mgr := plugin.NewManager(c.ProjectRoot)
	if needsScriptRuntime(toResolve, c.ProjectRoot) {
		registry := c.ToolchainRegistry
		if registry == nil {
			registry = NewToolchainRegistry(c.Store)
		}
		bbPath, err := c.resolveScriptRuntime(ctx, registry)
		if err != nil {
			fmt.Fprintf(os.Stderr, "  advice: resolve script runtime: %v\n", err)
			return
		}
		mgr.SetScriptRuntime(bbPath)
	}

	for _, rp := range resolved {
		if err := mgr.Register(rp.Def); err != nil {
			fmt.Fprintf(os.Stderr, "  advice: register %q: %v\n", rp.Def.Name, err)
			return
		}
	}

	if err := mgr.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "  advice: start plugins: %v\n", err)
		return
	}
	defer mgr.Close()

	// Build manifest and context.
	manifest := NewManifest(result, result.ExecResult, result.Targets, elapsed)
	advCtx := &plugin.AdviseContext{
		ProjectRoot: c.ProjectRoot,
		Targets:     targetNames,
		DurationS:   elapsed.Seconds(),
	}
	advCtx.GitSHA, advCtx.GitBranch, advCtx.GitDirty = gitInfo(c.ProjectRoot)

	// Collect per-plugin configs and resolve sealed inputs.
	configs := make(map[string]map[string]any)
	secrets := make(map[string]map[string]string)
	for _, a := range afterBuild {
		if a.Config != nil {
			configs[a.Plugin] = a.Config
		}
		// Sealed inputs for advice are resolved here. For now, pass as-is
		// (refs, not values) — full secret resolution requires the secret
		// provider plugins which may not be running. The advice plugin is
		// responsible for resolving its own secrets if needed.
		if len(a.SealedInputs) > 0 {
			secrets[a.Plugin] = a.SealedInputs
		}
	}

	mgr.Advise(ctx, "after-build", manifest, advCtx, configs, secrets)
}

// gitInfo returns the current HEAD SHA, branch, and dirty state.
func gitInfo(projectRoot string) (sha, branch string, dirty bool) {
	if out, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--short", "HEAD").Output(); err == nil {
		sha = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", projectRoot, "rev-parse", "--abbrev-ref", "HEAD").Output(); err == nil {
		branch = strings.TrimSpace(string(out))
	}
	if err := exec.Command("git", "-C", projectRoot, "diff", "--quiet", "HEAD").Run(); err != nil {
		dirty = true
	}
	return
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

// mapToActionSpec converts a map[string]any (emitted by a pith plan program)
// into a plugin.ActionSpec. Only recognized keys are mapped; unknown keys are
// silently ignored so that plan programs can evolve independently of the spec.
func mapToActionSpec(m map[string]any) plugin.ActionSpec {
	var spec plugin.ActionSpec
	if id, ok := m["id"].(string); ok {
		spec.ID = id
	}
	if cmd, ok := m["command"].([]any); ok {
		for _, c := range cmd {
			if s, ok := c.(string); ok {
				spec.Command = append(spec.Command, s)
			}
		}
	}
	if body, ok := m["body"].([]any); ok {
		spec.Body = body
	}
	if es, ok := m["eweSource"].(string); ok {
		spec.EweSource = es
	}
	if outputs, ok := m["outputs"].([]any); ok {
		for _, o := range outputs {
			if s, ok := o.(string); ok {
				spec.Outputs = append(spec.Outputs, s)
			}
		}
	}
	if deps, ok := m["depends_on"].([]any); ok {
		for _, d := range deps {
			if s, ok := d.(string); ok {
				spec.DependsOn = append(spec.DependsOn, s)
			}
		}
	}
	if env, ok := m["env"].(map[string]any); ok {
		spec.Env = make(map[string]string, len(env))
		for k, v := range env {
			if s, ok := v.(string); ok {
				spec.Env[k] = s
			}
		}
	}
	if inputs, ok := m["inputs"].(map[string]any); ok {
		spec.Inputs = make(map[string]string, len(inputs))
		for k, v := range inputs {
			if s, ok := v.(string); ok {
				spec.Inputs[k] = s
			}
		}
	}
	if wd, ok := m["work_dir"].(string); ok {
		spec.WorkDir = wd
	}
	if si, ok := m["sealed_inputs"].(map[string]any); ok {
		spec.SealedInputs = make(map[string]string, len(si))
		for k, v := range si {
			if s, ok := v.(string); ok {
				spec.SealedInputs[k] = s
			}
		}
	}
	if sm, ok := m["sealed_input_modes"].(map[string]any); ok {
		spec.SealedInputModes = make(map[string]string, len(sm))
		for k, v := range sm {
			if s, ok := v.(string); ok {
				spec.SealedInputModes[k] = s
			}
		}
	}
	if so, ok := m["sealed_outputs"].(map[string]any); ok {
		spec.SealedOutputs = make(map[string]string, len(so))
		for k, v := range so {
			if s, ok := v.(string); ok {
				spec.SealedOutputs[k] = s
			}
		}
	}
	if sm, ok := m["sealed_output_modes"].(map[string]any); ok {
		spec.SealedOutputModes = make(map[string]string, len(sm))
		for k, v := range sm {
			if s, ok := v.(string); ok {
				spec.SealedOutputModes[k] = s
			}
		}
	}
	if imp, ok := m["impure"].(bool); ok {
		spec.Impure = imp
	}
	if net, ok := m["network"].(bool); ok {
		spec.Network = net
	}
	return spec
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
