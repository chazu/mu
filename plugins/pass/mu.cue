// #PluginConfig: pass secret-provider plugin manifest.
package mu

// Go plugin: the entrypoint is the compiled binary, which is not committed.
// Build it into this directory before registering the plugin with
// `script:` (see `mu guide plugins`, GO PLUGINS):
//
//   go build -trimpath -o plugins/pass/plugin ./plugins/pass
//
// No toolchain: mu executes the binary directly.
plugin: {
	entrypoint: "plugin"
	files: ["plugin", "GUIDE.md"]
	guide: "GUIDE.md"
}
