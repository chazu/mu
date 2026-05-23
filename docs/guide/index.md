mu guide — quick-reference for agents and humans

Start here if you're new to mu:

  mu guide overview      What mu is, the mental model, and where to go next

Authoring:

  mu guide mu.cue        How to write and structure mu.cue config files
  mu guide build         Building targets: flags, plan mode, manifests
  mu guide observe       Drift detection: observing current state
  mu guide secrets       Sealed inputs/outputs, secret resolution, write policy

Built-in toolchains:

  mu guide shell         Run an arbitrary shell command as a target
  mu guide secret-gen    Mint a secret and store it via a provider plugin

Plugins and integration:

  mu guide plugins          Writing, loading, and distributing plugins
  mu guide sdk              Writing plugins in Go with sdk/muplugin (recommended)
  mu guide protocol         The NDJSON plugin protocol (discover, plan, observe, ...)
  mu guide secret-providers Authoring secret-aware plugins (resolve/store, sealed_outputs)
  mu guide pith-plugins     Writing inline plugins with pith VM programs
  mu guide plugin <name>    Show guide text bundled with a plugin
  mu guide pudl             How mu and pudl work together

Execution:

  mu guide sandbox       Hermetic isolation: copy, Seatbelt, namespaces
  mu guide advice        Build lifecycle observers (after-build hooks)

Operations:

  mu guide cache         Content-addressed storage and action caching
  mu guide toolchains    Bootstrapping toolchains from scratch
