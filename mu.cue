package mu

// Remote build cache lives on the loosh platform registry (akashic), under the
// shared loosh-industries/mu-cache repo — the same content-addressed action
// cache the akashic project pushes to. akashic requires a namespace/name repo,
// so a bare "mu" is rejected. Auth: `mu login registry.platform.loosh.cloud`
// with a push-capable snk_ PAT.
cache: {
	remote: "registry.platform.loosh.cloud/loosh-industries/mu-cache"
	push:   "registry.platform.loosh.cloud/loosh-industries/mu-cache"
}

// `mu build --publish <target>` pushes that target's outputs to the platform
// registry (akashic) as a first-class artifact under loosh-industries/<slug>,
// tagged by git sha + latest. Same auth as the cache push.
publish: repository: "registry.platform.loosh.cloud/loosh-industries"
toolchains: [{
	toolchain: "bb"
	from:      "scratch"
	config: {
		version: "1.12.216"
		url:     "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-macos-aarch64.tar.gz"
		sha256:  "91499b3f430038f9b40e433215256a6e5392942780dca9984d493d2bcca7055d"
	}
}, {
	// The go and lint plugins run go inside the sandbox from this toolchain;
	// it must satisfy go.mod's `go` line. Toolchain configs name one archive,
	// so this pins darwin-arm64 (sha256 from go.dev/dl/?mode=json).
	toolchain: "go"
	from:      "scratch"
	config: {
		version:      "1.26.10"
		url:          "https://go.dev/dl/go1.26.10.darwin-arm64.tar.gz"
		sha256:       "0b5183492292ada170f9b561a834863808dd02edba599e4ad0e89899330020b3"
		strip_prefix: "go"
	}
}]
plugins: [
	{name: "go", script: "plugins/go"},
	{name: "lint", script: "plugins/lint"}]
targets: [{
	target:    "//cmd/mu"
	toolchain: "go"
	// Must cover every module file `go list -deps ./cmd/mu` reports (Go
	// sources and go:embed files); TestSelfBuildSourcesCoverDeps in cmd/mu
	// enforces this. mu globs have no exclusions, so _test.go files are
	// included too (test-only edits cause a rebuild, never a stale hit).
	sources: ["go.mod", "go.sum", "cmd/**/*.go", "internal/**/*.go", "internal/**/*.cue", "sdk/**/*.go", "docs/guide/*.go", "docs/guide/*.md"]
	config: {
		output:   "mu"
		pkg:      "./cmd/mu"
		trimpath: true
	}
}, {
	target:    "//lint/go-vet"
	toolchain: "lint"
	// Sandboxed: vet sees only these files, so they must cover every package
	// of the root module plus its go:embed files. Modules are downloaded.
	sources: ["go.mod", "go.sum", "cmd/**/*.go", "internal/**/*.go", "internal/**/*.cue", "sdk/**/*.go", "plugins/**/*.go", "examples/*.go", "examples/plugins/**/*.go", "docs/guide/*.go", "docs/guide/*.md"]
	config: {
		command: ["go", "vet", "./..."]
		toolchains: ["go"]
		network: true
		env: {GOTOOLCHAIN: "local", GOFLAGS: "-modcacherw"}
	}
}, {
	target:    "//lint/gofmt"
	toolchain: "lint"
	sources: ["cmd/**/*.go", "internal/**/*.go", "sdk/**/*.go", "plugins/**/*.go", "examples/**/*.go"]
	config: {
		toolchains: ["go"]
		// `gofmt -l` exits 0 even when it lists files, so fail explicitly.
		command: ["sh", "-c", "out=$(gofmt -l cmd internal sdk plugins examples) || exit 1; [ -z \"$out\" ] || { echo \"$out\"; exit 1; }"]
		// Planned only with `fix: true`; plain `mu build` never rewrites sources.
		fix_command: ["gofmt", "-w", "cmd", "internal", "sdk", "plugins", "examples"]
	}
}, {
	target:    "//lint"
	toolchain: "shell"
	sources: []
	deps: ["//lint/go-vet", "//lint/gofmt"]
	config: {
		command: ["true"]
		impure: false
	}
}]
