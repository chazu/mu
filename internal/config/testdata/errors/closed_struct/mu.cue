// #PluginDef disjunction forbids declaring both script and command; each
// form sets the other to _|_ (bottom), producing a closed-struct-style
// rejection.
plugins: [
	{
		name:    "oops"
		script:  "plugins/oops"
		command: ["also-bad"]
	},
]
