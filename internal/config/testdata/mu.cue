// Test fixture equivalent to the repo's current root mu.json.
// Paired with testdata/mu.json — cue_decoder_test asserts that the
// cueDecoder output deep-equals the jsonDecoder output on this pair.
cache: {
	backends: [
		{type: "disk", path: "~/.mu/cache"},
		{type: "oci", registry: "registry.loosh.cloud/mu", write: false},
	]
	read_repair: true
	push: {
		registry:   "registry.loosh.cloud"
		repository: "mu"
	}
}
toolchains: [
	{
		toolchain: "bb"
		from:      "scratch"
		config: {
			version: "1.12.216"
			url:     "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-macos-aarch64.tar.gz"
			sha256:  "91499b3f430038f9b40e433215256a6e5392942780dca9984d493d2bcca7055d"
		}
	},
	{
		toolchain: "go"
		from:      "scratch"
		config: {
			version:      "1.25.8"
			url:          "https://go.dev/dl/go1.25.8.darwin-arm64.tar.gz"
			sha256:       "c6547959f5dbe8440bf3da972bd65ba900168de5e7ab01464fbdc7ac8375c21c"
			strip_prefix: "go"
		}
	},
]
plugins: [
	{name: "go", script:   "plugins/go"},
	{name: "lint", script: "plugins/lint"},
]
targets: [
	{
		target:    "//cmd/mu"
		toolchain: "go"
		sources: [
			"go.mod",
			"go.sum",
		]
		config: {
			output:   "mu"
			pkg:      "./cmd/mu"
			trimpath: true
		}
	},
	{
		target:    "//lint"
		toolchain: "shell"
		kind:      "kit"
		sources: []
		deps: ["//lint/go-vet", "//lint/gofmt"]
		config: {
			command: ["true"]
			impure: false
		}
	},
	{
		target:    "//lint/go-vet"
		toolchain: "lint"
		sources: ["cmd/mu/main.go"]
		config: {
			command: ["go", "vet", "./..."]
		}
	},
]
