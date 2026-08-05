# BRICK project guide

This is the current project layout for repositories that use both mu and PUDL.
BRICK metadata is optional; `mu.cue` and registered `#SystemModel` definitions
are the executable/configuration surfaces.

## Suggested layout

```text
repo/
├── mu.cue                         # root plugins, toolchains, targets, policy
├── app/
│   └── mu.cue                     # merged subdirectory targets
├── plugins/                       # local plugin packages, when needed
└── .pudl/
    ├── workspace.cue              # workspace name and sealed-write policy
    ├── schema/
    │   ├── models/                # registered #SystemModel definitions
    │   └── pudl/rules/            # repository Datalog rules
    └── definitions/               # legacy/general desired definitions
```

Initialize both surfaces with:

```bash
pudl init          # global built-in schemas/rules/#SystemModel
pudl repo init     # repository .pudl workspace
```

Create a model scaffold instead of hand-writing registration boilerplate:

```bash
pudl model new app --populate plugin:k8s --input namespace=default
pudl model describe app --json
pudl model validate app
```

## Mu target

```cue
package mu

plugins: [{name: "k8s", script: "plugins/k8s"}]

targets: [{
    target:    "//app"
    toolchain: "k8s"
    sources:   ["app/*.yaml"]
    config:    {namespace: "default"}
}]
```

Use `mu build --plan //app` before a direct mu-owned build. PUDL-owned
convergence invokes the same plugin through `pudl run app --converge`.

## Exact cross-model wiring

If model `app` consumes an observed value from `network`, declare a required
plain `inputs` slot plus a binding to a source schema field authorized with
`@pudl(binding=plain)`, then run the closed set:

```bash
pudl run-set network app
pudl run-set network app --converge
```

PUDL rejects omitted producers and cycles before execution. Mutating sets
finish read-only preflight for all members before the first apply.

For sealed values, author provider refs in the model's sealed declarations and
configure the workspace write allow-list:

```cue
// .pudl/workspace.cue
name: "example"
secrets: writable_refs: ["pass:example/*"]
```

PUDL-generated mu targets use `sealed_routing: "strict"`. Plugins must emit
explicit per-action claims. Sealed outputs are converge-only; a mutating
run-set containing one pauses for exact-plan approval:

```bash
pudl run-set network app --converge
pudl run-set report <run-set-id>
pudl run-set resume <run-set-id>   # or reject
```

## Verification and debugging

```bash
pudl run app --json
pudl run report [run-id] --json
pudl run-set report [run-set-id]
mu build --plan //app              # direct view of a handwritten mu target
mu observe --json //app            # direct plugin observation
```

PUDL reports own model/run/binding provenance. Mu manifests deliberately omit
sealed values and provider refs.
