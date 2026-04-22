// Package coordinator converts plugin output into executable DAG actions.
package coordinator

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/dag"
	"github.com/chau/mu/internal/plugin"
)

// isActionRef reports whether value is an inter-action reference like "{action:someID}".
func isActionRef(value string) bool {
	return strings.HasPrefix(value, "{action:") && strings.HasSuffix(value, "}")
}

// Resolve converts a slice of plugin ActionSpecs into resolved dag.Actions.
// File-path inputs are resolved relative to projectRoot and hashed via cas.ComputeDigest.
// Action-reference inputs ("{action:id}") are stored with a zero cas.Digest placeholder.
//
// crossTargetProducers maps project-relative output paths to the action IDs
// that produce them in this build. When a plugin declares an input whose
// path matches such an entry, the input is treated as deferred (zero
// digest) and an implicit DependsOn edge to the producing action is added
// so the DAG executes in the correct order. This lets a dependent target
// consume a dep's declared output by path without needing to wait for
// execution during planning.
func Resolve(specs []plugin.ActionSpec, projectRoot string, crossTargetProducers map[string]string) ([]*dag.Action, error) {
	actions := make([]*dag.Action, 0, len(specs))

	for _, spec := range specs {
		inputs := make(map[string]cas.Digest, len(spec.Inputs))
		extraDeps := make(map[string]struct{})

		for name, value := range spec.Inputs {
			if isActionRef(value) {
				// Placeholder for inter-action dependency; executor resolves later.
				inputs[name] = cas.Digest{}
				continue
			}

			// Cross-target reference: value is a project-relative path that
			// another already-planned action produces. File won't exist on
			// disk yet, so defer hashing and wire in a DependsOn edge.
			if producer, ok := crossTargetProducers[value]; ok {
				inputs[name] = cas.Digest{}
				extraDeps[producer] = struct{}{}
				continue
			}

			// Treat as file path relative to project root.
			path := filepath.Clean(filepath.Join(projectRoot, value))
			cleanRoot := filepath.Clean(projectRoot) + string(filepath.Separator)
			if path != filepath.Clean(projectRoot) && !strings.HasPrefix(path, cleanRoot) {
				return nil, fmt.Errorf("resolve action %q input %q: path %q escapes project root", spec.ID, name, value)
			}
			dgst, err := func() (cas.Digest, error) {
				f, err := os.Open(path)
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
		}

		// Copy Env map (nil-safe).
		var env map[string]string
		if spec.Env != nil {
			env = make(map[string]string, len(spec.Env))
			for k, v := range spec.Env {
				env[k] = v
			}
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

		// Copy SealedInputs map (nil-safe).
		var sealedInputs map[string]string
		if spec.SealedInputs != nil {
			sealedInputs = make(map[string]string, len(spec.SealedInputs))
			for k, v := range spec.SealedInputs {
				sealedInputs[k] = v
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
			for d := range extraDeps {
				if _, ok := seen[d]; ok {
					continue
				}
				seen[d] = struct{}{}
				merged = append(merged, d)
			}
			dependsOn = merged
		}

		actions = append(actions, &dag.Action{
			ID:             spec.ID,
			Command:        spec.Command,
			Inputs:         inputs,
			Outputs:        spec.Outputs,
			DependsOn:      dependsOn,
			Env:            env,
			SealedInputs:   sealedInputs,
			Network:        spec.Network,
			WorkDir:        workDir,
			Impure:         spec.Impure,
			TimeoutS:       spec.TimeoutS,
			Retries:        spec.Retries,
			RetryBackoffMs: spec.RetryBackoffMs,
		})
	}

	return actions, nil
}
