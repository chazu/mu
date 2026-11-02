// Package coordinator converts plugin output into executable DAG actions.
package coordinator

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chazu/mu/internal/cas"
	"github.com/chazu/mu/internal/dag"
	"github.com/chazu/mu/internal/plugin"
)

// isActionRef reports whether value is an inter-action reference like "{action:someID}".
func isActionRef(value string) bool {
	return strings.HasPrefix(value, "{action:") && strings.HasSuffix(value, "}")
}

// actionRefID returns the action ID named by an "{action:id}" reference.
func actionRefID(value string) string {
	return strings.TrimSuffix(strings.TrimPrefix(value, "{action:"), "}")
}

// Resolve converts a slice of plugin ActionSpecs into resolved dag.Actions.
// File-path inputs are resolved relative to projectRoot and hashed via cas.ComputeDigest.
// Action-reference inputs ("{action:id}") are stored with a zero cas.Digest
// placeholder, recorded in InputSources, and add a DependsOn edge to the
// referenced action. The executor fills the placeholder with a digest over the
// producer's declared outputs before computing the consumer's action key.
//
// crossTargetProducers maps project-relative output paths to the action IDs
// that produce them in this build. When a plugin declares an input whose
// path matches such an entry, the input is treated as deferred (zero
// digest, recorded in InputSources so the executor substitutes the produced
// file's digest) and an implicit DependsOn edge to the producing action is added
// so the DAG executes in the correct order. This lets a dependent target
// consume a dep's declared output by path without needing to wait for
// execution during planning.
//
// Every file input (project file or cross-target output) also records its
// project-relative path in InputPaths, where a sandboxed action finds it.
// toolchains resolves the scratch toolchains an action names (spec.Toolchains)
// into its dag.Action.Toolchain artifact set; it may be nil when no action
// names one.
func Resolve(ctx context.Context, specs []plugin.ActionSpec, projectRoot string, store cas.Store, crossTargetProducers map[string]string, toolchains ToolchainLookup) ([]*dag.Action, error) {
	actions := make([]*dag.Action, 0, len(specs))

	for _, spec := range specs {
		inputs := make(map[string]cas.Digest, len(spec.Inputs))
		var inputSources map[string]dag.InputSource
		var inputPaths map[string]string
		extraDeps := make(map[string]struct{})
		deferInput := func(name string, src dag.InputSource) {
			inputs[name] = cas.Digest{}
			if inputSources == nil {
				inputSources = make(map[string]dag.InputSource)
			}
			inputSources[name] = src
			extraDeps[src.ProducerID] = struct{}{}
		}
		recordPath := func(name, value string) {
			if inputPaths == nil {
				inputPaths = make(map[string]string)
			}
			inputPaths[name] = filepath.ToSlash(projectPathKey(projectRoot, "", value))
		}

		for name, value := range spec.Inputs {
			if isActionRef(value) {
				// Inter-action dependency: the executor substitutes a digest
				// over the producer's outputs once it has run.
				deferInput(name, dag.InputSource{ProducerID: actionRefID(value)})
				continue
			}

			// Cross-target reference: value is a project-relative path that
			// an action of another target declares as an output (Plan
			// collects producers for every target before resolving any).
			// The file may not exist on disk yet, so defer hashing and wire
			// in a DependsOn edge.
			if producer, ok := crossTargetProducers[projectPathKey(projectRoot, "", value)]; ok {
				deferInput(name, dag.InputSource{ProducerID: producer, OutputPath: value})
				recordPath(name, value)
				continue
			}

			dgst, err := func() (cas.Digest, error) {
				f, err := openProjectFile(projectRoot, value)
				if err != nil {
					return cas.Digest{}, err
				}
				defer f.Close()
				return cas.ComputeDigest(f)
			}()
			if err != nil {
				return nil, fmt.Errorf("resolve action %q input %q: %w", spec.ID, name, err)
			}
			inputs[name] = dgst
			recordPath(name, value)
		}

		toolchain, err := resolveToolchains(ctx, spec, spec.Impure || len(spec.SealedOutputs) > 0, toolchains)
		if err != nil {
			return nil, err
		}

		// Pure actions use a fixed clean default; only impure actions may inherit.
		var env map[string]string
		if spec.Env != nil {
			env = make(map[string]string, len(spec.Env))
			for k, v := range spec.Env {
				env[k] = v
			}
		}

		env = dag.ActionEnvironment(env, spec.Impure || len(spec.SealedOutputs) > 0)

		passthrough, err := resolveEnvPassthrough(spec)
		if err != nil {
			return nil, err
		}

		workDir := projectRoot
		if spec.WorkDir != "" {
			workDir = filepath.Join(projectRoot, spec.WorkDir)
			cleanRoot := filepath.Clean(projectRoot)
			cleanWork := filepath.Clean(workDir)
			if !strings.HasPrefix(cleanWork, cleanRoot+string(filepath.Separator)) && cleanWork != cleanRoot {
				return nil, fmt.Errorf("work_dir %q escapes project root", spec.WorkDir)
			}
		}

		workRoot, err := dag.OpenWorkDir(projectRoot, workDir)
		if err != nil {
			return nil, fmt.Errorf("action %q work_dir: %w", spec.ID, err)
		}
		workRoot.Close()

		// Copy SealedInputs map (nil-safe).
		var sealedInputs map[string]string
		if spec.SealedInputs != nil {
			sealedInputs = make(map[string]string, len(spec.SealedInputs))
			for k, v := range spec.SealedInputs {
				sealedInputs[k] = v
			}
		}
		var sealedInputModes map[string]string
		if spec.SealedInputModes != nil {
			sealedInputModes = make(map[string]string, len(spec.SealedInputModes))
			for k, v := range spec.SealedInputModes {
				sealedInputModes[k] = v
			}
		}
		for k, mode := range sealedInputModes {
			if _, ok := sealedInputs[k]; !ok {
				return nil, fmt.Errorf("action %q: sealed_input_modes[%q] references unknown sealed input", spec.ID, k)
			}
			switch mode {
			case "", "env", "file":
				// ok
			default:
				return nil, fmt.Errorf("action %q: sealed_input_modes[%q] = %q must be one of env | file", spec.ID, k, mode)
			}
		}

		// Copy SealedOutputs map (nil-safe). Force impure when present:
		// caching would skip the store_secret side-effect.
		var sealedOutputs map[string]string
		impure := spec.Impure
		if spec.SealedOutputs != nil {
			sealedOutputs = make(map[string]string, len(spec.SealedOutputs))
			for k, v := range spec.SealedOutputs {
				sealedOutputs[k] = v
			}
			impure = true
		}
		var sealedOutputModes map[string]string
		if spec.SealedOutputModes != nil {
			sealedOutputModes = make(map[string]string, len(spec.SealedOutputModes))
			for k, v := range spec.SealedOutputModes {
				sealedOutputModes[k] = v
			}
		}

		if spec.TimeoutS < 0 || spec.Retries < 0 || spec.RetryBackoffMs < 0 {
			return nil, fmt.Errorf("action %q: timeout_s, retries, and retry_backoff_ms must be non-negative", spec.ID)
		}

		// Merge implicit cross-target DependsOn edges with any already
		// declared by the plugin. Order is irrelevant for DAG correctness,
		// but we de-dup.
		dependsOn := spec.DependsOn
		if len(extraDeps) > 0 {
			seen := make(map[string]struct{}, len(dependsOn)+len(extraDeps))
			merged := make([]string, 0, len(dependsOn)+len(extraDeps))
			for _, d := range dependsOn {
				if _, ok := seen[d]; ok {
					continue
				}
				seen[d] = struct{}{}
				merged = append(merged, d)
			}
			implicit := make([]string, 0, len(extraDeps))
			for d := range extraDeps {
				implicit = append(implicit, d)
			}
			sort.Strings(implicit)
			for _, d := range implicit {
				if _, ok := seen[d]; ok {
					continue
				}
				seen[d] = struct{}{}
				merged = append(merged, d)
			}
			dependsOn = merged
		}

		actions = append(actions, &dag.Action{
			ID:                spec.ID,
			Command:           spec.Command,
			Inputs:            inputs,
			InputSources:      inputSources,
			InputPaths:        inputPaths,
			Toolchain:         toolchain,
			Outputs:           spec.Outputs,
			DependsOn:         dependsOn,
			Env:               env,
			EnvPassthrough:    passthrough,
			SealedInputs:      sealedInputs,
			SealedInputModes:  sealedInputModes,
			SealedOutputs:     sealedOutputs,
			SealedOutputModes: sealedOutputModes,
			Network:           spec.Network,
			WorkDir:           workDir,
			ProjectRoot:       projectRoot,
			Impure:            impure,
			TimeoutS:          spec.TimeoutS,
			Retries:           spec.Retries,
			RetryBackoffMs:    spec.RetryBackoffMs,
		})
	}

	return actions, nil
}

// ToolchainLookup returns the artifact map (relative path -> full digest
// string) of the named scratch toolchain, building it first when needed. It
// returns (nil, nil) when no such toolchain is configured.
type ToolchainLookup func(ctx context.Context, name string) (map[string]string, error)

// resolveToolchains merges the artifacts of every toolchain spec names into
// one sandbox-root artifact set. A raw-binary toolchain stores its binary at
// the root as "<name>"; it is placed at bin/<name> so it is on the sandbox
// PATH. The result is nil when the action names no toolchain.
func resolveToolchains(ctx context.Context, spec plugin.ActionSpec, impure bool, lookup ToolchainLookup) (map[string]cas.Digest, error) {
	if len(spec.Toolchains) == 0 {
		return nil, nil
	}
	if impure {
		return nil, fmt.Errorf("action %q names toolchains %v but is impure; toolchains exist only inside the sandbox, which runs pure actions", spec.ID, spec.Toolchains)
	}
	merged := make(map[string]cas.Digest)
	owner := make(map[string]string)
	for _, name := range spec.Toolchains {
		var artifacts map[string]string
		var err error
		if lookup != nil {
			artifacts, err = lookup(ctx, name)
		}
		if err != nil {
			return nil, fmt.Errorf("action %q toolchain %q: %w", spec.ID, name, err)
		}
		if artifacts == nil {
			return nil, fmt.Errorf("action %q names unknown toolchain %q (no `from: \"scratch\"` toolchain with that name is configured)", spec.ID, name)
		}
		_, hasBin := artifacts["bin/"+name]
		for rel, digest := range artifacts {
			d, err := cas.ParseFullDigest(digest)
			if err != nil {
				return nil, fmt.Errorf("action %q toolchain %q artifact %q: %w", spec.ID, name, rel, err)
			}
			if rel == name && !hasBin {
				rel = "bin/" + name
			}
			if prev, ok := merged[rel]; ok && prev != d {
				return nil, fmt.Errorf("action %q: toolchains %q and %q both provide %q with different content", spec.ID, owner[rel], name, rel)
			}
			merged[rel] = d
			owner[rel] = name
		}
	}
	return merged, nil
}

// resolveEnvPassthrough validates and copies an action's env_passthrough names.
// Duplicates are rejected so the key-hashed name set has one spelling.
func resolveEnvPassthrough(spec plugin.ActionSpec) ([]string, error) {
	if len(spec.EnvPassthrough) == 0 {
		return nil, nil
	}
	seen := make(map[string]struct{}, len(spec.EnvPassthrough))
	for _, name := range spec.EnvPassthrough {
		if name == "" || strings.ContainsAny(name, "=\x00") {
			return nil, fmt.Errorf("action %q env_passthrough: invalid variable name %q", spec.ID, name)
		}
		if _, dup := seen[name]; dup {
			return nil, fmt.Errorf("action %q env_passthrough: duplicate variable name %q", spec.ID, name)
		}
		seen[name] = struct{}{}
	}
	return append([]string(nil), spec.EnvPassthrough...), nil
}

// openProjectFile allows project-relative paths and absolute paths lexically
// inside the project, then confines symlink resolution through os.Root. A
// check followed by os.Open would let a swapped symlink escape the root.
func openProjectFile(projectRoot, name string) (*os.File, error) {
	root, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}
	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("path %q escapes project root", name)
	}
	return os.OpenInRoot(root, rel)
}
