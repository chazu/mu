// sources expects [...string] but we give it an int.
targets: [
	{
		target:    "//foo/bar"
		toolchain: "go"
		sources:   123
	},
]
