package scenario

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/chazu/mu/internal/plugin"
)

// Result reports the outcome of a single scenario.
type Result struct {
	Name    string        `json:"name"`
	Status  string        `json:"status"` // "pass" | "fail" | "skip" | "error"
	Reason  string        `json:"reason,omitempty"`
	Elapsed time.Duration `json:"elapsed_ns"`
	Request any           `json:"request,omitempty"`
	Actual  any           `json:"actual,omitempty"`
}

// RunOptions tunes the Runner.
type RunOptions struct {
	UpdateGolden bool
	Verbose      bool
}

// Runner executes scenarios against a spawned plugin. Callers are
// responsible for starting the plugin process and passing it in;
// the runner does NOT own the lifecycle.
type Runner struct {
	Proc     *plugin.Process
	Discover *plugin.DiscoverResponse
	Opts     RunOptions
}

// Run executes one scenario end-to-end.
func (r *Runner) Run(ctx context.Context, s *Scenario) Result {
	start := time.Now()
	result := Result{Name: s.Name, Request: s.Request}

	// Skip-if-missing-capability gate.
	if s.SkipIfMissingCapability != "" && r.Discover != nil {
		if !r.Discover.HasCapability(s.SkipIfMissingCapability) {
			result.Status = "skip"
			result.Reason = "plugin does not declare " + s.SkipIfMissingCapability
			result.Elapsed = time.Since(start)
			return result
		}
	}
	if s.SkipIfMissingEnv != "" && os.Getenv(s.SkipIfMissingEnv) == "" {
		result.Status = "skip"
		result.Reason = "environment variable " + s.SkipIfMissingEnv + " is not set"
		result.Elapsed = time.Since(start)
		return result
	}

	// Send request.
	method, _ := s.Request["method"].(string)
	if method == "" {
		result.Status = "error"
		result.Reason = "scenario.request.method is required"
		result.Elapsed = time.Since(start)
		return result
	}

	req := buildRequest(s.Request)
	var actual any
	if err := r.send(ctx, req, &actual); err != nil {
		result.Status = "fail"
		result.Reason = err.Error()
		result.Elapsed = time.Since(start)
		return result
	}
	result.Actual = actual

	// Normalize actual to map[string]any for Match.
	actualMap, ok := actual.(map[string]any)
	if !ok {
		result.Status = "fail"
		result.Reason = fmt.Sprintf("response is not a JSON object: %T", actual)
		result.Elapsed = time.Since(start)
		return result
	}

	// Dispatch match mode.
	switch s.Expect.Match {
	case "golden":
		goldenPath := s.Expect.Golden
		if !filepath.IsAbs(goldenPath) && s.Path != "" {
			goldenPath = filepath.Join(filepath.Dir(s.Path), goldenPath)
		}
		if r.Opts.UpdateGolden {
			buf, _ := json.MarshalIndent(actualMap, "", "  ")
			if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
				result.Status = "error"
				result.Reason = err.Error()
			} else if err := os.WriteFile(goldenPath, buf, 0o644); err != nil {
				result.Status = "error"
				result.Reason = err.Error()
			} else {
				result.Status = "pass"
				result.Reason = "golden updated: " + goldenPath
			}
			result.Elapsed = time.Since(start)
			return result
		}
		goldenBytes, err := os.ReadFile(goldenPath)
		if err != nil {
			result.Status = "fail"
			result.Reason = "reading golden: " + err.Error()
			result.Elapsed = time.Since(start)
			return result
		}
		if err := s.MatchGolden(actualMap, goldenBytes); err != nil {
			result.Status = "fail"
			result.Reason = err.Error()
		} else {
			result.Status = "pass"
		}
	default:
		if err := s.Match(actualMap); err != nil {
			result.Status = "fail"
			result.Reason = err.Error()
		} else {
			result.Status = "pass"
		}
	}

	result.Elapsed = time.Since(start)
	return result
}

// send dispatches a protocol request through the underlying Process by
// method name. We don't use Process's typed Plan/Observe methods because
// scenarios may send arbitrary requests (including malformed ones) that
// don't fit those signatures.
//
// The Process type doesn't currently expose a generic send, so we rely
// on the typed methods when the scenario's method matches one of them.
// Unknown methods return an error.
func (r *Runner) send(ctx context.Context, req plugin.Request, out *any) error {
	switch req.Method {
	case "discover":
		resp, err := r.Proc.Discover(ctx)
		if err != nil {
			return err
		}
		return roundTripInto(resp, out)
	case "plan":
		if req.Target == nil {
			return fmt.Errorf("plan request missing target")
		}
		resp, err := r.Proc.Plan(ctx, *req.Target, req.Deps, req.ToolchainArtifacts)
		if err != nil {
			*out = map[string]any{"error": err.Error()}
			return nil
		}
		return roundTripInto(resp, out)
	case "observe":
		if req.Target == nil {
			return fmt.Errorf("observe request missing target")
		}
		resp, err := r.Proc.Observe(ctx, *req.Target, req.ToolchainArtifacts, req.Secrets)
		if err != nil {
			*out = map[string]any{"error": err.Error()}
			return nil
		}
		return roundTripInto(resp, out)
	case "resolve_secret":
		val, err := r.Proc.ResolveSecret(ctx, req.SecretRef)
		if err != nil {
			*out = map[string]any{"error": err.Error()}
			return nil
		}
		*out = map[string]any{"value": val}
		return nil
	case "store_secret":
		if err := r.Proc.StoreSecret(ctx, req.SecretRef, req.SecretValue, req.SecretMode); err != nil {
			*out = map[string]any{"error": err.Error()}
			return nil
		}
		*out = map[string]any{}
		return nil
	default:
		return fmt.Errorf("unknown method %q (supported: discover, plan, observe, resolve_secret, store_secret)", req.Method)
	}
}

func roundTripInto(v any, out *any) error {
	buf, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return json.Unmarshal(buf, out)
}

// buildRequest reshapes the scenario's flat request map into a typed
// plugin.Request. Unknown keys are ignored.
func buildRequest(m map[string]any) plugin.Request {
	req := plugin.Request{}
	if s, ok := m["method"].(string); ok {
		req.Method = s
	}
	if t, ok := m["target"].(map[string]any); ok {
		req.Target = buildTarget(t)
	}
	if ta, ok := m["toolchain_artifacts"].(map[string]any); ok {
		req.ToolchainArtifacts = asStringMap(ta)
	}
	if secrets, ok := m["secrets"].(map[string]any); ok {
		req.Secrets = asStringMap(secrets)
	}
	if ref, ok := m["secret_ref"].(string); ok {
		req.SecretRef = ref
	}
	if v, ok := m["secret_value"].(string); ok {
		req.SecretValue = v
	}
	if mode, ok := m["secret_mode"].(string); ok {
		req.SecretMode = mode
	}
	if deps, ok := m["deps"].([]any); ok {
		for _, d := range deps {
			if dm, ok := d.(map[string]any); ok {
				req.Deps = append(req.Deps, plugin.DepInfo{
					Target:    str(dm, "target"),
					Artifacts: asStringMap(mapOr(dm, "artifacts")),
				})
			}
		}
	}
	return req
}

func buildTarget(m map[string]any) *plugin.TargetInfo {
	t := &plugin.TargetInfo{
		Name:      str(m, "name"),
		Toolchain: str(m, "toolchain"),
	}
	if srcs, ok := m["sources"].([]any); ok {
		for _, s := range srcs {
			if ss, ok := s.(string); ok {
				t.Sources = append(t.Sources, ss)
			}
		}
	}
	if cfg, ok := m["config"].(map[string]any); ok {
		t.Config = cfg
	}
	return t
}

func str(m map[string]any, k string) string {
	if v, ok := m[k].(string); ok {
		return v
	}
	return ""
}

func asStringMap(m map[string]any) map[string]string {
	if m == nil {
		return nil
	}
	out := make(map[string]string, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			out[k] = s
		}
	}
	return out
}

func mapOr(m map[string]any, k string) map[string]any {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}
