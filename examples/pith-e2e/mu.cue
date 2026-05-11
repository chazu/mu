// Pith VM end-to-end example: inline Plan + Transform programs.
//
// The "data" target uses a pith Plan program to emit an action that
// generates a JSON metrics file. The "report" target depends on "data"
// and uses a pith Transform to read the dependency output, plus its
// own Plan to emit the report-writing action.
//
// No external plugins or toolchains needed — everything inline.
package mu

targets: [{
	target:  "//data:metrics"
	sources: []
	config: {
		service: "api-gateway"
		region:  "us-east-1"
	}

	// Plan phase: read target config, build an action spec, emit it.
	// pith words: target/config pushes config map, 'key is string literal,
	// get pops key+map and pushes value.
	plan: [
		// Emit a shell action that writes metrics JSON.
		// In real use the command might call an API or run a tool.
		{
			"id":      "generate-metrics"
			"command": ["sh", "-c", """
				mkdir -p "$MU_OUT"
				cat > "$MU_OUT/metrics.json" <<'JSONEOF'
				{
				  "service": "api-gateway",
				  "region": "us-east-1",
				  "timestamp": "2025-01-15T10:30:00Z",
				  "requests": 48201,
				  "errors": 37,
				  "p99_ms": 142.5,
				  "healthy": true
				}
				JSONEOF
				"""]
			"outputs": ["metrics.json"]
		},
		"action/emit",
	]
}, {
	target:  "//report:summary"
	sources: []
	deps: ["//data:metrics"]
	config: {
		format: "text"
	}

	// Transform phase: runs after //data:metrics completes.
	// Reads dependency output, extracts key fields for downstream use.
	// Transform programs can read outputs but cannot emit actions.
	transform: [
		"'//data:metrics", "target/output",
		// Stack: {metrics-map with all outputs}
		"dup", "'requests", "get",
		"swap",
		"dup", "'errors", "get",
		"swap", "'service", "get",
		// Stack: requests errors service
	]

	// Plan phase: emit an action to write the summary report.
	plan: [
		{
			"id":      "write-summary"
			"command": ["sh", "-c", """
				mkdir -p "$MU_OUT"
				echo "=== Service Health Report ===" > "$MU_OUT/summary.txt"
				echo "Service: api-gateway" >> "$MU_OUT/summary.txt"
				echo "Status: HEALTHY" >> "$MU_OUT/summary.txt"
				echo "Requests: 48201" >> "$MU_OUT/summary.txt"
				echo "Errors: 37 (0.08%)" >> "$MU_OUT/summary.txt"
				echo "P99 Latency: 142.5ms" >> "$MU_OUT/summary.txt"
				"""]
			"outputs": ["summary.txt"]
		},
		"action/emit",
	]
}]
