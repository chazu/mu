// #PluginConfig for the envsecret plugin.
package mu

plugin: {
	entrypoint: "envsecret"
	files: ["envsecret"]
	guide: "GUIDE.md"
}
targets: [{
	target:    "build"
	toolchain: "shell"
	sources: ["main.go"]
	config: {
		command: ["go", "build", "-o", "envsecret", "."]
		impure: false
	}
}]
