// Hello-go is the canonical minimum mu plugin written in Go.
//
// Build:  go build -o hello-go .
// Run as a plugin from mu.cue:
//
//	plugins: [{name: "hello", command: ["./examples/plugins/hello-go/hello-go"]}]
//	targets: [{
//	    target:    "//hello"
//	    toolchain: "hello"
//	    sources: []
//	    config: {out: "hello.txt", message: "hi mu"}
//	}]
package main

import (
	"context"
	"fmt"

	"github.com/chazu/mu/sdk/muplugin"
)

func main() {
	(&muplugin.Plugin{
		Name:        "hello",
		Version:     "0.1.0",
		Description: "Writes a greeting to a file. The canonical Go SDK example.",
		Produces:    []string{"text"},
		Plan:        plan,
	}).Main()
}

func plan(ctx context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	out, _ := req.Target.Config["out"].(string)
	if out == "" {
		out = "hello.txt"
	}
	msg, _ := req.Target.Config["message"].(string)
	if msg == "" {
		msg = "hello from mu"
	}
	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{{
			ID:      "write",
			Command: []string{"sh", "-c", fmt.Sprintf("echo %q > %q", msg, out)},
			Inputs:  map[string]string{},
			Outputs: []string{out},
		}},
		Outputs: map[string]string{"text": out},
	}, nil
}
