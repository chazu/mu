// Empty toolchain string should fail — must be non-empty when provided.
targets: [
	{
		target: "//foo"
		toolchain: ""
		sources: ["a.go"]
	},
]
