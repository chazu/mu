// Missing required toolchain field on the target — should fail #Target unification.
targets: [
	{
		target: "//foo"
		sources: ["a.go"]
	},
]
