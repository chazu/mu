package definitions

import "pudl.schemas/pudl/brick"

// Lint interface — contract for code quality targets.
// Any component implementing this interface must declare a linter command.
lint_interface: brick.#Interface & {
	name: "//interface/lint"
	kind: "interface"
	desc: "Contract for code linting and formatting targets"
	contract: {
		toolchain: "lint"
		config: {
			command: [...string]
		}
	}
}

// Go vet — static analysis for the mu codebase.
lint_go_vet: brick.#Target & {
	name:       "//lint/go-vet"
	kind:       "component"
	toolchain:  "lint"
	implements: "//interface/lint"
	desc:       "Go vet static analysis"
	sources: ["cmd/mu/*.go", "internal/**/*.go"]
	config: {
		command: ["go", "vet", "./..."]
	}
}

// Go fmt — formatting check for the mu codebase.
lint_gofmt: brick.#Target & {
	name:       "//lint/gofmt"
	kind:       "component"
	toolchain:  "lint"
	implements: "//interface/lint"
	desc:       "Go formatting check"
	sources: ["cmd/mu/*.go", "internal/**/*.go"]
	config: {
		command:     ["gofmt", "-l", "."]
		fix_command: ["gofmt", "-w", "."]
	}
}

// Lint kit — composes all linting targets.
lint_kit: brick.#Kit & {
	name: "//lint"
	kind: "kit"
	desc: "All code quality checks for the mu project"
	composes: ["//lint/go-vet", "//lint/gofmt"]
}
