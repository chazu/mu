package coordinator

import (
	"time"

	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/dag"
)

// ManifestTarget describes a target that was part of the build.
// Carries BRICK classification metadata for round-tripping to pudl.
type ManifestTarget struct {
	Name       string `json:"name"`
	Toolchain  string `json:"toolchain"`
	Kind       string `json:"kind,omitempty"`       // BRICK kind (set by pudl)
	Implements string `json:"implements,omitempty"` // BRICK interface (set by pudl)
}

// ManifestAction describes a single action's outcome in a build manifest.
type ManifestAction struct {
	ID       string            `json:"id"`
	Cached   bool              `json:"cached"`
	ExitCode int               `json:"exit_code"`
	Outputs  map[string]string `json:"outputs,omitempty"` // name -> digest string
}

// Manifest is the structured output of a build run.
type Manifest struct {
	Version   int              `json:"version"`
	Type      string           `json:"type"`       // "mu.build.manifest/v1"
	Timestamp string           `json:"timestamp"`  // ISO 8601
	Duration  float64          `json:"duration_s"` // seconds
	Targets   []ManifestTarget `json:"targets"`
	Actions   []ManifestAction `json:"actions"`
	Summary   BuildResultJSON  `json:"summary"`
}

// BuildResultJSON is the JSON-friendly summary of build counts.
type BuildResultJSON struct {
	Completed int `json:"completed"`
	Cached    int `json:"cached"`
	Failed    int `json:"failed"`
	Cancelled int `json:"cancelled"`
}

// NewManifest constructs a Manifest from build results and the targets that were built.
func NewManifest(result *BuildResult, execResult *dag.ExecuteResult, targets []config.Target, elapsed time.Duration) *Manifest {
	actions := make([]ManifestAction, 0, len(execResult.Completed)+len(execResult.Failed))

	for _, s := range execResult.Completed {
		ma := ManifestAction{
			ID:       s.ID,
			Cached:   s.Cached,
			ExitCode: s.ExitCode,
		}
		if len(s.Outputs) > 0 {
			ma.Outputs = make(map[string]string, len(s.Outputs))
			for name, dgst := range s.Outputs {
				ma.Outputs[name] = dgst.String()
			}
		}
		actions = append(actions, ma)
	}

	for _, s := range execResult.Failed {
		ma := ManifestAction{
			ID:       s.ID,
			Cached:   false,
			ExitCode: s.ExitCode,
		}
		if len(s.Outputs) > 0 {
			ma.Outputs = make(map[string]string, len(s.Outputs))
			for name, dgst := range s.Outputs {
				ma.Outputs[name] = dgst.String()
			}
		}
		actions = append(actions, ma)
	}

	// Build target list with BRICK metadata.
	manifestTargets := make([]ManifestTarget, 0, len(targets))
	for _, t := range targets {
		manifestTargets = append(manifestTargets, ManifestTarget{
			Name:       t.Name,
			Toolchain:  t.Toolchain,
			Kind:       t.Kind,
			Implements: t.Implements,
		})
	}

	return &Manifest{
		Version:   1,
		Type:      "mu.build.manifest/v1",
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Duration:  elapsed.Seconds(),
		Targets:   manifestTargets,
		Actions:   actions,
		Summary: BuildResultJSON{
			Completed: result.Completed,
			Cached:    result.Cached,
			Failed:    result.Failed,
			Cancelled: result.Cancelled,
		},
	}
}
