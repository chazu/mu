mu guide sdk — writing plugins in Go with sdk/muplugin

WHAT THIS IS

  sdk/muplugin is mu's public plugin SDK. A Go plugin is one struct
  literal and one Main() call. The SDK handles the NDJSON loop,
  capability advertisement, error envelopes, and request dispatch.

  Import path: github.com/chau/mu/sdk/muplugin

MINIMAL PLUGIN (30 LINES)

  package main

  import (
      "context"
      "github.com/chau/mu/sdk/muplugin"
  )

  func main() {
      (&muplugin.Plugin{
          Name:     "hello",
          Version:  "0.1.0",
          Produces: []string{"text"},
          Plan:     plan,
      }).Main()
  }

  func plan(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
      out, _ := req.Target.Config["out"].(string)
      return muplugin.PlanResponse{
          Actions: []muplugin.ActionSpec{{
              ID:      "write",
              Command: []string{"echo", "hello", ">", out},
              Outputs: []string{out},
          }},
          Outputs: map[string]string{"text": out},
      }, nil
  }

  Build: go build -o hello . Reference it from mu.cue with
  plugins: [{name: "hello", command: ["./hello"]}].

  See examples/plugins/hello-go/ for the canonical version.

CAPABILITY DERIVATION

  You don't list capabilities. The SDK derives them from which optional
  function fields are non-nil on Plugin:

    Plan           always advertised (required)
    Observe        adds "observe"
    ResolveSecret  adds "resolve_secret"
    StoreSecret    adds "store_secret"
    Advise         adds "advise" (also set AdvisePhases)

  This eliminates the most common bb-plugin bug: declaring a capability
  in the discover response but forgetting to wire the handler (or vice
  versa).

THE PLUGIN STRUCT

  type Plugin struct {
      Name, Version, Description string
      Consumes, Produces         []string
      ConfigSchema               map[string]any
      OutputSchema               *SchemaRef

      Plan          func(ctx, PlanRequest) (PlanResponse, error)        // required
      Observe       func(ctx, ObserveRequest) (ObserveResponse, error)  // optional
      ResolveSecret func(ctx, ref string) (string, error)               // optional
      StoreSecret   func(ctx, StoreSecretRequest) error                 // optional
      Advise        func(ctx, AdviseRequest) error                      // optional
      AdvisePhases  []string                                            // used only if Advise != nil
  }

  Methods:
    .Main()  — runs against os.Stdin/os.Stdout, exits process on fatal
    .Run(ctx, r io.Reader, w io.Writer) error  — for embedded use/tests

SECRET-PROVIDER SHORTCUT

  Implement SecretBackend and pass it to SecretPlugin:

    type SecretBackend interface {
        Resolve(ctx context.Context, ref string) (string, error)
        Store(ctx context.Context, ref, value, mode string) error
    }

    func main() {
        muplugin.SecretPlugin("pass", "0.1.0", &myPassBackend{}).Main()
    }

  The resulting plugin advertises resolve_secret + store_secret
  automatically and stubs Plan to an empty action list.

TESTING WITHOUT A SUBPROCESS

  Two helpers run the dispatch loop in-process:

    resp, err := muplugin.Exchange(t.Context(), p, muplugin.NewDiscoverRequest())
    // resp is map[string]any — assert wire shape

    var typed muplugin.PlanResponse
    err := muplugin.ExchangeInto(t.Context(), p, muplugin.NewPlanRequest(target, deps, nil), &typed)

  Both encode the request to NDJSON, run Plugin.Run against in-memory
  buffers, and decode the response. No subprocess, no goroutines, no
  fixtures — just function calls.

ERROR HANDLING

  Return error from any handler — the SDK puts it in the response's
  Error field and continues serving. Returning a non-nil error does
  NOT abort the loop; it surfaces to the coordinator, which decides
  whether the build fails.

  For unrecoverable bugs in the plugin itself (panics, IO errors),
  the SDK lets them propagate from Run() and Main() exits 1.

WIRE TYPES

  All wire types live in sdk/muplugin/types.go. They are the single
  source of truth — internal/plugin in the mu codebase re-exports
  them via type aliases. See `mu guide protocol` for the full NDJSON
  spec; see `go doc github.com/chau/mu/sdk/muplugin` for the Go API.

PORTING A BB PLUGIN

  Typical Babashka plugin shape maps directly:

    bb form                                    Go form
    (case (get req "method") ...)              SDK dispatch
    (defn discover-response [] {...})          Plugin{Name, Version, ...}
    (defn plan-response [req] {...})           Plan: func(ctx, req) {...}
    (defn observe-response [req] {...})        Observe: func(ctx, req) {...}
    (json/generate-string)                     handled by SDK
    (read-line) loop                           handled by SDK

  Action shapes (id/command/inputs/outputs/depends_on/env/network)
  match field-for-field via muplugin.ActionSpec.

WHEN TO USE GO vs BB vs PITH

  Go SDK         when you need static compilation, fast startup, or
                 strong typing. Production plugins. Default choice.
  bb             when you're sketching and want REPL-style iteration.
                 Cost: bb toolchain dependency on every host.
  pith VM        when the plugin is pure data transformation and
                 doesn't need a process. See `mu guide pith-plugins`.

SEE ALSO

  mu guide plugins      — plugin structure, manifest, distribution
  mu guide protocol     — NDJSON wire format (language-agnostic spec)
  mu guide pith-plugins — inline VM-based plugins
  examples/plugins/hello-go/ — canonical Go example
