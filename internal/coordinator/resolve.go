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
func Resolve(specs []plugin.ActionSpec, projectRoot string) ([]*dag.Action, error) {
	actions := make([]*dag.Action, 0, len(specs))

	for _, spec := range specs {
		inputs := make(map[string]cas.Digest, len(spec.Inputs))

		for name, value := range spec.Inputs {
			if isActionRef(value) {
				// Placeholder for inter-action dependency; executor resolves later.
				inputs[name] = cas.Digest{}
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

		actions = append(actions, &dag.Action{
			ID:           spec.ID,
			Command:      spec.Command,
			Inputs:       inputs,
			Outputs:      spec.Outputs,
			DependsOn:    spec.DependsOn,
			Env:          env,
			SealedInputs: sealedInputs,
			Network:      spec.Network,
			WorkDir:      workDir,
			Impure:       spec.Impure,
		})
	}

	return actions, nil
}
