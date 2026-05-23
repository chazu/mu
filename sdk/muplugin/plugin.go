package muplugin

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Plugin describes a mu plugin. Set the required fields, set whichever
// optional method handlers you want (Observe, ResolveSecret, StoreSecret,
// Advise) — any non-nil handler is automatically advertised in the
// discover response's capabilities array. Then call Main().
type Plugin struct {
	// Identity
	Name        string
	Version     string
	Description string

	// Artifact types this plugin consumes (input dependencies) and produces.
	Consumes []string
	Produces []string

	// Optional: declarative config schema and output schemas. The SDK
	// passes these through to the discover response unmodified.
	ConfigSchema map[string]any
	OutputSchema *SchemaRef

	// Required.
	Plan func(ctx context.Context, req PlanRequest) (PlanResponse, error)

	// Optional method handlers. A non-nil handler implies the capability;
	// leaving it nil hides the capability from discover.
	Observe       func(ctx context.Context, req ObserveRequest) (ObserveResponse, error)
	ResolveSecret func(ctx context.Context, ref string) (string, error)
	StoreSecret   func(ctx context.Context, req StoreSecretRequest) error
	Advise        func(ctx context.Context, req AdviseRequest) error

	// AdvisePhases lists the build lifecycle phases this plugin wants to
	// observe (e.g. "after-build"). Only consulted when Advise is non-nil.
	AdvisePhases []string
}

// Main runs the plugin against stdin/stdout and exits the process.
// On unrecoverable transport errors, exits with status 1 after writing
// a single error line to stderr.
func (p *Plugin) Main() {
	if err := p.Run(context.Background(), os.Stdin, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "%s: %v\n", p.Name, err)
		os.Exit(1)
	}
}

// Run reads NDJSON requests from r, dispatches them to the configured
// handlers, and writes NDJSON responses to w. It returns only on EOF or
// an unrecoverable transport error. Handler errors are surfaced in the
// response's Error field; they do not abort the loop.
func (p *Plugin) Run(ctx context.Context, r io.Reader, w io.Writer) error {
	if p.Name == "" {
		return errors.New("muplugin: Plugin.Name is required")
	}
	if p.Plan == nil {
		return errors.New("muplugin: Plugin.Plan is required")
	}

	enc := json.NewEncoder(w)
	scanner := bufio.NewScanner(r)
	// Plan responses can be large; lift the line cap to 16 MiB.
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)

	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var req Request
		if err := json.Unmarshal(line, &req); err != nil {
			if encErr := enc.Encode(map[string]string{"error": fmt.Sprintf("invalid request: %v", err)}); encErr != nil {
				return encErr
			}
			continue
		}
		resp := p.dispatch(ctx, req)
		if err := enc.Encode(resp); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	return nil
}

// dispatch routes a single Request to the appropriate handler and returns
// the response value (a struct that json-encodes to the protocol shape).
func (p *Plugin) dispatch(ctx context.Context, req Request) any {
	switch req.Method {
	case "discover":
		return p.discover()
	case "plan":
		if req.Target == nil {
			return PlanResponse{Error: "plan: missing target"}
		}
		out, err := p.Plan(ctx, PlanRequest{
			Target:             *req.Target,
			Deps:               req.Deps,
			ToolchainArtifacts: req.ToolchainArtifacts,
		})
		if err != nil {
			return PlanResponse{Error: err.Error()}
		}
		if out.Actions == nil {
			out.Actions = []ActionSpec{}
		}
		if out.Outputs == nil {
			out.Outputs = map[string]string{}
		}
		return out
	case "observe":
		if p.Observe == nil {
			return ObserveResponse{Error: "observe: capability not supported"}
		}
		if req.Target == nil {
			return ObserveResponse{Error: "observe: missing target"}
		}
		out, err := p.Observe(ctx, ObserveRequest{
			Target:             *req.Target,
			ToolchainArtifacts: req.ToolchainArtifacts,
			Secrets:            req.Secrets,
		})
		if err != nil {
			return ObserveResponse{Error: err.Error()}
		}
		return out
	case "resolve_secret":
		if p.ResolveSecret == nil {
			return ResolveSecretResponse{Error: "resolve_secret: capability not supported"}
		}
		val, err := p.ResolveSecret(ctx, req.SecretRef)
		if err != nil {
			return ResolveSecretResponse{Error: err.Error()}
		}
		return ResolveSecretResponse{Value: val}
	case "store_secret":
		if p.StoreSecret == nil {
			return StoreSecretResponse{Error: "store_secret: capability not supported"}
		}
		err := p.StoreSecret(ctx, StoreSecretRequest{
			Ref:   req.SecretRef,
			Value: req.SecretValue,
			Mode:  req.SecretMode,
		})
		if err != nil {
			return StoreSecretResponse{Error: err.Error()}
		}
		return StoreSecretResponse{}
	case "advise":
		if p.Advise == nil {
			return AdviseResponse{Error: "advise: capability not supported"}
		}
		err := p.Advise(ctx, AdviseRequest{
			Phase:    req.Phase,
			Manifest: req.Manifest,
			Context:  req.AdviseContext,
			Config:   req.AdviseConfig,
			Secrets:  req.Secrets,
		})
		if err != nil {
			return AdviseResponse{Error: err.Error()}
		}
		return AdviseResponse{OK: true}
	default:
		return map[string]string{"error": fmt.Sprintf("unknown method %q", req.Method)}
	}
}

// discover builds the DiscoverResponse from the Plugin's configured fields.
// The capabilities array is derived from which optional handlers are non-nil.
func (p *Plugin) discover() DiscoverResponse {
	caps := []string{"discover", "plan"}
	if p.Observe != nil {
		caps = append(caps, "observe")
	}
	if p.ResolveSecret != nil {
		caps = append(caps, "resolve_secret")
	}
	if p.StoreSecret != nil {
		caps = append(caps, "store_secret")
	}
	if p.Advise != nil {
		caps = append(caps, "advise")
	}
	resp := DiscoverResponse{
		Name:            p.Name,
		Version:         p.Version,
		Description:     p.Description,
		ProtocolVersion: ProtocolVersion,
		Consumes:        p.Consumes,
		Produces:        p.Produces,
		ConfigSchema:    p.ConfigSchema,
		Capabilities:    caps,
		OutputSchema:    p.OutputSchema,
	}
	if p.Consumes == nil {
		resp.Consumes = []string{}
	}
	if p.Produces == nil {
		resp.Produces = []string{}
	}
	if p.Advise != nil && len(p.AdvisePhases) > 0 {
		resp.AdvisePhases = p.AdvisePhases
	}
	return resp
}

// SecretBackend is the interface for plugins that act purely as secret
// providers. Implementing this and passing the result to SecretPlugin
// is a one-liner alternative to setting ResolveSecret + StoreSecret on
// a Plugin directly.
type SecretBackend interface {
	Resolve(ctx context.Context, ref string) (string, error)
	Store(ctx context.Context, ref, value, mode string) error
}

// SecretPlugin wraps a SecretBackend into a Plugin ready for .Main().
// Name and Version are required; everything else is wired automatically.
// The resulting plugin produces no artifacts (Plan returns an empty
// action list), so it can be registered solely for secret resolution.
func SecretPlugin(name, version string, backend SecretBackend) *Plugin {
	return &Plugin{
		Name:    name,
		Version: version,
		Plan: func(ctx context.Context, req PlanRequest) (PlanResponse, error) {
			return PlanResponse{Actions: []ActionSpec{}, Outputs: map[string]string{}}, nil
		},
		ResolveSecret: backend.Resolve,
		StoreSecret: func(ctx context.Context, req StoreSecretRequest) error {
			return backend.Store(ctx, req.Ref, req.Value, req.Mode)
		},
	}
}
