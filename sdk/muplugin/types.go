// Package muplugin is mu's public plugin SDK.
//
// Authoring a plugin in Go takes one struct literal and one Main() call:
//
//	func main() {
//	    (&muplugin.Plugin{
//	        Name:     "hello",
//	        Version:  "0.1.0",
//	        Produces: []string{"text"},
//	        Plan:     func(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
//	            return muplugin.PlanResponse{
//	                Actions: []muplugin.ActionSpec{{
//	                    ID:      "greet",
//	                    Command: []string{"echo", "hello"},
//	                    Outputs: []string{"hello.txt"},
//	                }},
//	                Outputs: map[string]string{"text": "hello.txt"},
//	            }, nil
//	        },
//	    }).Main()
//	}
//
// The SDK derives the discover capabilities array from which optional
// fields (Observe, ResolveSecret, StoreSecret, Advise) are non-nil — no
// manual capabilities list.
//
// This file defines the wire types. They are the canonical Go
// representation of every NDJSON message exchanged with the coordinator.
// internal/plugin re-exports these via type aliases so the rest of the
// mu codebase keeps its existing import paths.
package muplugin

import "time"

// ProtocolVersion is the current version of the plugin protocol.
const ProtocolVersion = 1

// Request is the unified envelope sent to plugins via NDJSON.
// Plugins dispatch on the Method field.
type Request struct {
	Method             string            `json:"method"`                        // "discover", "plan", "observe", "resolve_secret", "store_secret", or "advise"
	Target             *TargetInfo       `json:"target,omitempty"`              // set for "plan" and "observe"
	Deps               []DepInfo         `json:"deps,omitempty"`                // set for "plan"
	ToolchainArtifacts map[string]string `json:"toolchain_artifacts,omitempty"` // set for "plan" and "observe"
	Secrets            map[string]string `json:"secrets,omitempty"`             // set for "observe" and "advise" — resolved sealed input values
	SecretRef          string            `json:"secret_ref,omitempty"`          // set for "resolve_secret" and "store_secret"
	SecretValue        string            `json:"secret_value,omitempty"`        // set for "store_secret" — bytes to persist
	SecretMode         string            `json:"secret_mode,omitempty"`         // set for "store_secret" — "create" | "overwrite" | "create_if_absent"
	Phase              string            `json:"phase,omitempty"`               // set for "advise" — lifecycle phase
	Manifest           any               `json:"manifest,omitempty"`            // set for "advise" when phase is "after-build"
	AdviseContext      *AdviseContext    `json:"advise_context,omitempty"`      // set for "advise"
	AdviseConfig       map[string]any    `json:"advise_config,omitempty"`       // set for "advise" — per-advice config from mu.cue
}

// DiscoverResponse is returned by plugins for method "discover".
type DiscoverResponse struct {
	Name            string         `json:"name"`
	Description     string         `json:"description,omitempty"`
	Version         string         `json:"version"`
	ProtocolVersion int            `json:"protocol_version"`
	Consumes        []string       `json:"consumes"` // artifact types this plugin can consume
	Produces        []string       `json:"produces"` // artifact types this plugin can produce
	ConfigSchema    map[string]any `json:"config_schema,omitempty"`
	Capabilities    []string       `json:"capabilities,omitempty"`   // supported methods, e.g. ["discover","plan","observe","advise"]
	AdvisePhases    []string       `json:"advise_phases,omitempty"`  // lifecycle phases this plugin wants to advise on
	OutputSchema    *SchemaRef     `json:"output_schema,omitempty"`  // legacy single-schema form
	OutputSchemas   []SchemaRef    `json:"output_schemas,omitempty"` // resource-specific output schemas
}

// SchemaRef is an optional, declarative reference to a CUE schema that
// describes the shape of a plugin's output. It is consumed by downstream
// tools (notably pudl) to classify imported data without re-inferring.
//
// The reference is a pointer to a schema; the schema itself is resolved
// from a vendored module shipped in the plugin bundle, the local schema
// cache, or (eventually) a remote registry. Plugins do not embed CUE
// content here.
//
// Namespace convention (see brainstorm 2026-05-04-plugin-output-schemas):
//   - "pudl/..."         → first-party pudl schemas
//   - "mu/<plugin-name>" → schemas originated by a mu plugin's authors
//   - anything else      → third-party / user-defined
type SchemaRef struct {
	ResourceType string `json:"resource_type,omitempty"` // emitted record type, e.g. "aws.ec2.instance"
	Module       string `json:"module"`                  // CUE module path, e.g. "mu/aws"
	Version      string `json:"version"`                 // module version, e.g. "v1"
	Definition   string `json:"definition,omitempty"`    // optional CUE definition selector, e.g. "#EC2Instance"
	Description  string `json:"description,omitempty"`   // human-readable summary of the schema's purpose
	Source       string `json:"source,omitempty"`        // advisory: "vendored" | "remote"
}

// HasCapability reports whether the plugin declared support for the given method.
// If Capabilities is empty (old plugins), defaults to supporting "discover" and "plan".
func (d *DiscoverResponse) HasCapability(method string) bool {
	if len(d.Capabilities) == 0 {
		return method == "discover" || method == "plan"
	}
	for _, c := range d.Capabilities {
		if c == method {
			return true
		}
	}
	return false
}

// ObserveResponse is returned by plugins for method "observe".
// Plugins report the current state of the resource as structured data.
// Convergence decisions (converged vs. drifted) are made by pudl, not
// by the plugin — the plugin is a sensor, not a judge.
type ObserveResponse struct {
	Current map[string]any `json:"current,omitempty"` // observed state of the resource
	Error   string         `json:"error,omitempty"`
}

// NewObserveRequest returns a Request for the "observe" method.
// Secrets contains resolved sealed input values (never logged, cached, or stored in CAS).
func NewObserveRequest(target TargetInfo, toolchainArtifacts map[string]string, secrets map[string]string) Request {
	return Request{
		Method:             "observe",
		Target:             &target,
		ToolchainArtifacts: toolchainArtifacts,
		Secrets:            secrets,
	}
}

// PlanResponse is returned by plugins for method "plan".
type PlanResponse struct {
	Actions []ActionSpec      `json:"actions"`
	Outputs map[string]string `json:"declared_outputs"` // artifact type -> output file path
	Error   string            `json:"error,omitempty"`
}

// TargetInfo carries the build file declaration for a target.
type TargetInfo struct {
	Name             string            `json:"name"`      // e.g. "//lib/crypto"
	Toolchain        string            `json:"toolchain"` // e.g. "go"
	Sources          []string          `json:"sources"`
	Config           map[string]any    `json:"config,omitempty"`
	SealedInputs     map[string]string `json:"sealed_inputs,omitempty"`      // name -> secret reference (e.g. "pass:deploy/token")
	SealedInputModes map[string]string `json:"sealed_input_modes,omitempty"` // name -> delivery mode: "env" (default) or "file"
	SealedOutputs    map[string]string `json:"sealed_outputs,omitempty"`     // file name -> secret reference; action writes to $MU_SEALED_OUT_DIR/<name>
}

// DepInfo carries what a dependency produced.
type DepInfo struct {
	Target    string            `json:"target"`    // e.g. "//lib/utils"
	Artifacts map[string]string `json:"artifacts"` // artifact type -> digest string
}

// ActionSpec is the plugin's output -- an action template with file paths (not resolved digests).
// The coordinator resolves paths to digests and converts ActionSpec to dag.Action.
type ActionSpec struct {
	ID                string            `json:"id"`
	Command           []string          `json:"command"`
	Body              []any             `json:"body,omitempty"`       // pith VM program; mutually exclusive with Command
	EweSource         string            `json:"ewe_source,omitempty"` // project-relative path to an ewe populator .cue program; mutually exclusive with Command/Body
	Inputs            map[string]string `json:"inputs"`               // name -> file path or "{action:id}" reference
	Outputs           []string          `json:"outputs"`              // declared output file paths
	DependsOn         []string          `json:"depends_on,omitempty"` // intra-subgraph action IDs
	Env               map[string]string `json:"env,omitempty"`
	SealedInputs      map[string]string `json:"sealed_inputs,omitempty"`       // name -> secret reference (e.g. "pass:deploy/token")
	SealedInputModes  map[string]string `json:"sealed_input_modes,omitempty"`  // name -> delivery mode: "env" (default) or "file"
	SealedOutputs     map[string]string `json:"sealed_outputs,omitempty"`      // file name -> secret reference; written via $MU_SEALED_OUT_DIR
	SealedOutputModes map[string]string `json:"sealed_output_modes,omitempty"` // file name -> store mode ("create" | "overwrite" | "create_if_absent"); default "overwrite"
	Network           bool              `json:"network,omitempty"`
	WorkDir           string            `json:"work_dir,omitempty"` // relative to project root (default: project root)
	Impure            bool              `json:"impure,omitempty"`   // skip CAS cache

	// TimeoutS is the per-attempt wall-clock timeout in seconds. 0 = no
	// timeout (legacy). Applies to all actions regardless of Network.
	TimeoutS int `json:"timeout_s,omitempty"`

	// Retries is the number of additional attempts on failure. 0 = no
	// retry (legacy). Retries ONLY apply when Network is true — pure
	// actions are deterministic and retrying is meaningless. Non-network
	// actions silently ignore Retries (with a one-time warning per build).
	Retries int `json:"retries,omitempty"`

	// RetryBackoffMs is the sleep between attempts in milliseconds.
	// 0 = retry immediately.
	RetryBackoffMs int `json:"retry_backoff_ms,omitempty"`
}

// ResolveSecretResponse is returned by plugins for method "resolve_secret".
// The Value field contains the resolved secret. It must never be logged,
// cached, or included in build manifests.
type ResolveSecretResponse struct {
	Value string `json:"value"`
	Error string `json:"error,omitempty"`
}

// StoreSecretMode values for NewStoreSecretRequest.
const (
	StoreSecretModeCreate         = "create"
	StoreSecretModeOverwrite      = "overwrite"
	StoreSecretModeCreateIfAbsent = "create_if_absent"
)

// StoreSecretResponse is returned by plugins for method "store_secret".
// The response carries no value back; the runner must not surface or log
// the stored bytes.
type StoreSecretResponse struct {
	Error string `json:"error,omitempty"`
}

// AdviseContext carries metadata about the build environment for advice plugins.
type AdviseContext struct {
	ProjectRoot string   `json:"project_root"`
	Targets     []string `json:"targets"`
	DurationS   float64  `json:"duration_s,omitempty"`
	GitSHA      string   `json:"git_sha,omitempty"`
	GitBranch   string   `json:"git_branch,omitempty"`
	GitDirty    bool     `json:"git_dirty,omitempty"`
}

// AdviseResponse is returned by plugins for method "advise".
// Advice errors are non-fatal — the build is not failed if an advice plugin
// returns an error.
type AdviseResponse struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// DefaultAdviseTimeout is the default timeout for advise requests.
const DefaultAdviseTimeout = 30 * time.Second

// NewAdviseRequest returns a Request for the "advise" method.
func NewAdviseRequest(phase string, manifest any, ctx *AdviseContext, cfg map[string]any, secrets map[string]string) Request {
	return Request{
		Method:        "advise",
		Phase:         phase,
		Manifest:      manifest,
		AdviseContext: ctx,
		AdviseConfig:  cfg,
		Secrets:       secrets,
	}
}

// HasAdvisePhase reports whether the plugin declared interest in the given phase.
func (d *DiscoverResponse) HasAdvisePhase(phase string) bool {
	for _, p := range d.AdvisePhases {
		if p == phase {
			return true
		}
	}
	return false
}

// NewStoreSecretRequest returns a Request for the "store_secret" method.
// The ref is the secret path with the scheme prefix stripped (e.g.
// "deploy/token", not "pass:deploy/token"). Mode must be one of the
// StoreSecretMode* constants; an empty mode is treated as "overwrite"
// by convention but plugins should reject empty modes explicitly.
func NewStoreSecretRequest(ref, value, mode string) Request {
	return Request{
		Method:      "store_secret",
		SecretRef:   ref,
		SecretValue: value,
		SecretMode:  mode,
	}
}

// NewDiscoverRequest returns a Request for the "discover" method.
func NewDiscoverRequest() Request {
	return Request{Method: "discover"}
}

// NewResolveSecretRequest returns a Request for the "resolve_secret" method.
// The ref is the secret path with the scheme prefix stripped (e.g. "deploy/token",
// not "pass:deploy/token").
func NewResolveSecretRequest(ref string) Request {
	return Request{Method: "resolve_secret", SecretRef: ref}
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

// PlanRequest is an ergonomic helper view of a Request when Method == "plan".
// The SDK passes this to user Plan funcs so they don't dispatch on Method themselves.
type PlanRequest struct {
	Target             TargetInfo
	Deps               []DepInfo
	ToolchainArtifacts map[string]string
}

// ObserveRequest is the ergonomic view of an observe Request for SDK callers.
type ObserveRequest struct {
	Target             TargetInfo
	ToolchainArtifacts map[string]string
	Secrets            map[string]string
}

// AdviseRequest is the ergonomic view of an advise Request for SDK callers.
type AdviseRequest struct {
	Phase    string
	Manifest any
	Context  *AdviseContext
	Config   map[string]any
	Secrets  map[string]string
}

// StoreSecretRequest is the ergonomic view of a store_secret Request.
type StoreSecretRequest struct {
	Ref   string
	Value string
	Mode  string
}
