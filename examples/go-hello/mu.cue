package mu

toolchains: [{
	toolchain: "bb"
	from:      "scratch"
	config: {
		version: "1.12.216"
		url:     "https://github.com/babashka/babashka/releases/download/v1.12.216/babashka-1.12.216-macos-aarch64.tar.gz"
		sha256:  "91499b3f430038f9b40e433215256a6e5392942780dca9984d493d2bcca7055d"
	}
}, {
	// The go plugin builds inside mu's sandbox with this toolchain.
	toolchain: "go"
	from:      "scratch"
	config: {
		version:      "1.26.10"
		url:          "https://go.dev/dl/go1.26.10.darwin-arm64.tar.gz"
		sha256:       "0b5183492292ada170f9b561a834863808dd02edba599e4ad0e89899330020b3"
		strip_prefix: "go"
	}
}]
plugins: [{
	name:   "go"
	script: "../../plugins/go/plugin.bb"
}]
targets: [{
	target:    "//hello"
	toolchain: "go"
	sources: ["go.mod", "go.sum", "cmd/hello/main.go"]
	config: {
		output: "hello"
		pkg:    "./cmd/hello"
	}
}]
