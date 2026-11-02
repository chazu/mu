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

// Plugin describes a mu plugin. Set the required identity fields, then set
// whichever operation handlers the plugin supports. Any non-nil handler is
// automatically advertised in the discover response's capabilities array.
// Then call Main().
type Plugin struct {
	// Identity
	Name        string
	Version     string
	Description string

	// Artifact types this plugin consumes (input dependencies) and produces.
	Consumes []string
	Produces []string

	// Optional: declarative config schema. The SDK passes it through to
	// the discover response unmodified.
	ConfigSchema map[string]any

	// Optional. A plugin that builds targets implements Plan.
	Plan func(ctx context.Context, req PlanRequest) (PlanResponse, error)

	// Optional method handlers. A non-nil handler implies the capability;
	// leaving it nil hides the capability from discover.
	ResolveSecret func(ctx context.Context, ref string) (string, error)
	StoreSecret   func(ctx context.Context, req StoreSecretRequest) error
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
		if p.Plan == nil {
			return PlanResponse{Error: "plan: capability not supported"}
		}
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
	default:
		return map[string]string{"error": fmt.Sprintf("unknown method %q", req.Method)}
	}
}

// discover builds the DiscoverResponse from the Plugin's configured fields.
// The capabilities array is derived from which handlers are non-nil.
func (p *Plugin) discover() DiscoverResponse {
	caps := []string{"discover"}
	if p.Plan != nil {
		caps = append(caps, "plan")
	}
	if p.ResolveSecret != nil {
		caps = append(caps, "resolve_secret")
	}
	if p.StoreSecret != nil {
		caps = append(caps, "store_secret")
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
	}
	if p.Consumes == nil {
		resp.Consumes = []string{}
	}
	if p.Produces == nil {
		resp.Produces = []string{}
	}
	return resp
}

// SecretResolver is the read side of a secret provider. Every backend
// passed to SecretPlugin must implement it.
type SecretResolver interface {
	Resolve(ctx context.Context, ref string) (string, error)
}

// SecretStorer is the optional write side of a secret provider. A backend
// that does not implement it is read-only: SecretPlugin then omits
// store_secret from the discover capabilities, so mu rejects any
// sealed_output routed to it at plan time rather than at execution.
type SecretStorer interface {
	Store(ctx context.Context, ref, value, mode string) error
}

// SecretBackend is a read-write secret provider.
type SecretBackend interface {
	SecretResolver
	SecretStorer
}

// SecretPlugin wraps a secret backend into a Plugin ready for .Main().
// Name and Version are required; everything else is wired automatically.
// The plugin advertises resolve_secret, plus store_secret only when the
// backend also implements SecretStorer. It does not advertise plan.
func SecretPlugin(name, version string, backend SecretResolver) *Plugin {
	p := &Plugin{
		Name:          name,
		Version:       version,
		ResolveSecret: backend.Resolve,
	}
	if storer, ok := backend.(SecretStorer); ok {
		p.StoreSecret = func(ctx context.Context, req StoreSecretRequest) error {
			return storer.Store(ctx, req.Ref, req.Value, req.Mode)
		}
	}
	return p
}
