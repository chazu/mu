package definitions

import "pudl.schemas/pudl/brick"

// mu binary — the main build artifact.
build_mu: brick.#Target & {
	name:      "//cmd/mu"
	kind:      "component"
	toolchain: "go"
	desc:      "mu build coordinator binary"
	sources: [
		"go.mod", "go.sum",
		"cmd/mu/*.go",
		"internal/**/*.go",
	]
	config: {
		output:   "mu"
		pkg:      "./cmd/mu"
		trimpath: true
	}
}

// Plugin build targets — each plugin is a component.
plugin_go: brick.#Target & {
	name:      "//plugins/go"
	kind:      "component"
	toolchain: "shell"
	desc:      "Go toolchain plugin"
	sources: ["plugins/go/plugin.bb"]
	config: {
		command: ["cp", "plugins/go/plugin.bb", "go-plugin.bb"]
		outputs: ["go-plugin.bb"]
		impure: false
	}
}

plugin_lint: brick.#Target & {
	name:      "//plugins/lint"
	kind:      "component"
	toolchain: "shell"
	desc:      "Lint plugin"
	sources: ["plugins/lint/plugin.bb"]
	config: {
		command: ["cp", "plugins/lint/plugin.bb", "lint-plugin.bb"]
		outputs: ["lint-plugin.bb"]
		impure: false
	}
}

plugin_k8s: brick.#Target & {
	name:      "//plugins/k8s"
	kind:      "component"
	toolchain: "shell"
	desc:      "Kubernetes convergence plugin"
	sources: ["plugins/k8s/plugin.bb"]
	config: {
		command: ["cp", "plugins/k8s/plugin.bb", "k8s-plugin.bb"]
		outputs: ["k8s-plugin.bb"]
		impure: false
	}
}

// Plugins kit — all bundled plugins.
plugins_kit: brick.#Kit & {
	name: "//plugins"
	kind: "kit"
	desc: "All bundled mu plugins"
	composes: [
		"//plugins/go",
		"//plugins/cowsay",
		"//plugins/docker",
		"//plugins/file",
		"//plugins/k8s",
		"//plugins/zig",
		"//plugins/terraform",
		"//plugins/scratch",
		"//plugins/lint",
	]
}
