// Pith + Go toolchain example: a pith-based target consuming a Go binary.
//
// Demonstrates how a pith target uses a non-pith plugin (the Go toolchain)
// via mu's cross-target dependency and artifact wiring:
//
//   1. "//cmd:build"  — Go plugin compiles a binary (greet)
//   2. "//cmd:run"    — Pith plan emits a shell action that invokes the binary;
//                       the binary path is wired via inputs → producerForPath
//   3. "//cmd:report" — Pith transform reads "run" output, plan emits summary
//
// The key mechanism is the `inputs` map in the pith-emitted action spec:
// when an input path matches a declared_output from a dependency target,
// the resolver automatically adds a DependsOn edge and defers hashing
// until the producer action has completed.
package mu

toolchains: [{
	toolchain: "bb"
	from:      "scratch"
	config: {
		version: "1.12.216"
		url:     "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-macos-aarch64.tar.gz"
		sha256:  "91499b3f430038f9b40e433215256a6e5392942780dca9984d493d2bcca7055d"
	}
}]

plugins: [{
	name:   "go"
	script: "../../plugins/go/plugin.bb"
}]

targets: [
	//
	// Target 1: Build the Go binary using the go plugin.
	// The go plugin emits mod-download + build actions and declares
	// {"executable": "greet"} as its output.
	//
	{
		target:    "//cmd:build"
		toolchain: "go"
		sources: ["go.mod", "go.sum", "cmd/greet/main.go"]
		config: {
			output: "greet"
			pkg:    "./cmd/greet"
		}
	},

	//
	// Target 2: Pith plan emits a shell action that runs the Go binary.
	//
	// The cross-target wiring works through the inputs map:
	//   inputs: {"binary": "greet"}
	// The resolver sees "greet" in producerForPath (from the build target's
	// declared_outputs) and adds an implicit DependsOn edge to build:build.
	// At execution time, the binary exists at WorkDir/greet because the
	// executor copied it from $MU_OUT after the build action completed.
	//
	{
		target:  "//cmd:run"
		deps: ["//cmd:build"]
		sources: []
		config: {
			greet_name: "Pith"
		}

		plan: [
			{
				"id":      "invoke"
				"command": ["sh", "-c", """
					mkdir -p "$MU_OUT"
					./greet Pith > "$MU_OUT/result.json"
					"""]
				"inputs": {"binary": "greet"}
				"outputs": ["result.json"]
				"env": {"PATH": "/usr/local/bin:/usr/bin:/bin"}
			},
			"action/emit",
		]
	},

	//
	// Target 3: Pith transform reads the run target's output, then
	// a pith plan emits a shell action that writes a summary.
	//
	// Transform phase: target/output reads "run"'s completed outputs
	// (result.json is JSON-decoded automatically by the executor's
	// getOutput closure). The transform extracts fields and leaves
	// them on the stack for downstream inspection.
	//
	// Plan phase: emits a shell action that writes a human-readable
	// report. The plan runs during planning (before execution), so
	// it cannot read transform results — but it can reference the
	// same input path to wire the dependency.
	//
	{
		target:  "//cmd:report"
		deps: ["//cmd:run"]
		sources: []
		config: {}

		transform: [
			"'//cmd:run", "target/output",
			// Stack: {result.json map with greeting/source/args}
			"dup", "'greeting", "get",
			// Stack: {output-map} "Hello, Pith!"
			"swap", "'source", "get",
			// Stack: "Hello, Pith!" "go-toolchain"
		]

		plan: [
			{
				"id":      "write-report"
				"command": ["sh", "-c", """
					mkdir -p "$MU_OUT"
					echo "=== Pith + Go Toolchain Report ===" > "$MU_OUT/report.txt"
					cat result.json >> "$MU_OUT/report.txt"
					"""]
				"inputs": {"data": "result.json"}
				"outputs": ["report.txt"]
				"env": {"PATH": "/usr/local/bin:/usr/bin:/bin"}
			},
			"action/emit",
		]
	},
]
