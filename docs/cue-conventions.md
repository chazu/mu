# Writing `mu.cue`

How mu reads CUE build configs, and the conventions that follow from it. For
the field reference run `mu guide mu.cue`; the schema itself is
[`internal/config/schema.cue`](../internal/config/schema.cue).

## How mu loads config

- mu evaluates CUE in-process with the `cuelang.org/go` library pinned in
  `go.mod`. You do not need the `cue` CLI; it is useful only for `cue fmt`.
- The project root is the nearest directory, walking up from the working
  directory, that contains a `mu.cue`. `--config PATH` overrides discovery.
- In each directory mu reads **only** the file named `mu.cue`. Other `.cue`
  files next to it are not loaded, even in the same package. By convention
  the file says `package mu`.
- The root `mu.cue` is unified with `#ProjectConfig` and must evaluate to
  concrete values.
- Every `mu.cue` below the root is loaded the same way and merged in. Hidden
  directories, `testdata/` directories and symlinks are skipped. In a
  subdirectory config:
  - a target name without a leading `//` is prefixed with the directory, so
    `target: "lib"` in `pkg/mu.cue` becomes `//pkg/lib`;
  - `sources` and plugin `script`/`command` paths are relative to that
    directory.
- In the root `mu.cue`, target names must start with `//`.

## `cue.mod`, imports and `@embed`

- `cue.mod/module.cue` is **optional**. Projects without one load fine, and
  none of the examples in this repo have one.
- Add one only if `mu.cue` imports CUE packages from your own tree. The
  import path is the module path plus the package directory:

  ```cue
  // cue.mod/module.cue
  module: "example.com/myproject"
  language: version: "v0.13.0"
  ```

  ```cue
  // mu.cue
  package mu

  import "example.com/myproject/build/defaults"
  ```

- Do not use `@embed`. mu loads `mu.cue` as a single file, and CUE rejects
  embedding there ("cannot embed files when not in a module"), even when a
  `cue.mod` exists.

## What invalidates the cache

An action's cache key covers its command, environment, output names, the
content of its declared inputs and the toolchain and dependency artifacts it
consumes. Plugins, and the built-in `shell` toolchain, turn a target's
`sources` into those declared inputs. mu hashes nothing else from the working
tree. Impure actions are never cached.

So if a target runs a script, list the script in `sources`:

```cue
{
	target:    "//lint/gofmt"
	toolchain: "shell"
	sources:   ["scripts/lint.sh", "cmd/mu/*.go"]
	config: {
		command: ["sh", "scripts/lint.sh"]
		impure:  false
	}
}
```

Without the `sources` entry, editing `lint.sh` does not change the key, and
the old result is reused. `sources` accepts doublestar globs (`src/**/*.go`)
relative to the config's directory. Matches inside hidden directories are
dropped.

## Checking a config

```bash
mu build --plan //...
```

This loads and validates every `mu.cue`, runs plugin `discover`, checks each
target's `config` against the plugin's `config_schema`, and plans the full
action graph without running anything. `mu target list` is a faster check
when you only need the config to load.
