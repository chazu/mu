// Package plugin defines the wire protocol types for mu's plugin system.
// Plugins communicate with the build coordinator over NDJSON (newline-delimited JSON).
package plugin

// ProtocolVersion is the current version of the plugin protocol.
const ProtocolVersion = 1

// Request is the unified envelope sent to plugins via NDJSON.
// Plugins dispatch on the Method field.
type Request struct {
	Method             string            `json:"method"`                        // "discover" or "plan"
	Target             *TargetInfo       `json:"target,omitempty"`              // set for "plan"
	Deps               []DepInfo         `json:"deps,omitempty"`               // set for "plan"
	ToolchainArtifacts map[string]string `json:"toolchain_artifacts,omitempty"` // set for "plan"
}

// DiscoverResponse is returned by plugins for method "discover".
type DiscoverResponse struct {
	Name            string         `json:"name"`
	Version         string         `json:"version"`
	ProtocolVersion int            `json:"protocol_version"`
	Consumes        []string       `json:"consumes"`              // artifact types this plugin can consume
	Produces        []string       `json:"produces"`              // artifact types this plugin can produce
	ConfigSchema    map[string]any `json:"config_schema,omitempty"`
}

// PlanResponse is returned by plugins for method "plan".
type PlanResponse struct {
	Actions []ActionSpec      `json:"actions"`
	Outputs map[string]string `json:"declared_outputs"` // artifact type -> output file path
	Error   string            `json:"error,omitempty"`
}

// TargetInfo carries the build file declaration for a target.
type TargetInfo struct {
	Name      string         `json:"name"`      // e.g. "//lib/crypto"
	Toolchain string         `json:"toolchain"` // e.g. "go"
	Sources   []string       `json:"sources"`
	Config    map[string]any `json:"config,omitempty"`
}

// DepInfo carries what a dependency produced.
type DepInfo struct {
	Target    string            `json:"target"`    // e.g. "//lib/utils"
	Artifacts map[string]string `json:"artifacts"` // artifact type -> digest string
}

// ActionSpec is the plugin's output -- an action template with file paths (not resolved digests).
// The coordinator resolves paths to digests and converts ActionSpec to dag.Action.
type ActionSpec struct {
	ID        string            `json:"id"`
	Command   []string          `json:"command"`
	Inputs    map[string]string `json:"inputs"`               // name -> file path or "{action:id}" reference
	Outputs   []string          `json:"outputs"`              // declared output file paths
	DependsOn []string          `json:"depends_on,omitempty"` // intra-subgraph action IDs
	Env       map[string]string `json:"env,omitempty"`
	Network   bool              `json:"network,omitempty"`
	WorkDir   string            `json:"work_dir,omitempty"`   // relative to project root (default: project root)
	Impure    bool              `json:"impure,omitempty"`     // skip CAS cache
}

// NewDiscoverRequest returns a Request for the "discover" method.
func NewDiscoverRequest() Request {
	return Request{Method: "discover"}
}

// NewPlanRequest returns a Request for the "plan" method.
func NewPlanRequest(target TargetInfo, deps []DepInfo, toolchainArtifacts map[string]string) Request {
	return Request{
		Method:             "plan",
		Target:             &target,
		Deps:               deps,
		ToolchainArtifacts: toolchainArtifacts,
	}
}
