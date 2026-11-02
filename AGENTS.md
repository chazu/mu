# Agent Instructions

This project uses **bd** (beads) for issue tracking. Run `bd onboard` to get started.

## Quick Reference

```bash
bd ready              # Find available work
bd show <id>          # View issue details
bd update <id> --status in_progress  # Claim work
bd close <id>         # Complete work
bd sync               # Sync with git
```

## Landing the Plane (Session Completion)

**When ending a work session**, you MUST complete ALL steps below. Work is NOT complete until `git push` succeeds.

**MANDATORY WORKFLOW:**

1. **File issues for remaining work** - Create issues for anything that needs follow-up
2. **Run quality gates** (if code changed) - Tests, linters, builds
3. **Update issue status** - Close finished work, update in-progress items
4. **PUSH TO REMOTE** - This is MANDATORY:
   ```bash
   git pull --rebase
   bd sync
   git push
   git status  # MUST show "up to date with origin"
   ```
5. **Clean up** - Clear stashes, prune remote branches
6. **Verify** - All changes committed AND pushed
7. **Hand off** - Provide context for next session

**CRITICAL RULES:**
- Work is NOT complete until `git push` succeeds
- NEVER stop before pushing - that leaves work stranded locally
- NEVER say "ready to push when you are" - YOU must push
- If push fails, resolve and retry until it succeeds


## What mu is

mu is a language-agnostic build tool. Targets are declared in `mu.cue`.
Plugins (external executables speaking NDJSON over stdin/stdout) turn each
target into actions; mu runs the combined action graph in parallel and caches
results in a content-addressed store. `shell` and `secret-gen` are built-in
toolchains that need no plugin.

`mu guide` is the reference and ships inside the binary; its sources are in
`docs/guide/*.md`. Start with `mu guide overview`, then `mu guide mu.cue`.

## Build config (`mu.cue`)

See [`docs/cue-conventions.md`](docs/cue-conventions.md). In short:

- mu reads only the file named `mu.cue` in each directory. Configs in
  subdirectories are merged, and their bare target names are prefixed with
  the directory (`lib` in `pkg/mu.cue` becomes `//pkg/lib`).
- `cue.mod/module.cue` is optional; add it only to import your own CUE
  packages. Do not use `@embed`; mu cannot load it.
- Only files listed in a target's `sources` feed the cache key. List any
  script a target runs in its `sources`.
- Check config changes with `mu build --plan //...` before committing.

## Everyday commands

```bash
mu build --plan //...           # load, validate and plan without running
mu build //target               # build; --no-cache to ignore cached results
mu build --emit-manifest //...  # build and print a JSON manifest (for CI)
mu target list                  # targets defined in this project
mu graph //target               # dependency chain (--reverse, --dot, --json)
mu plugin list                  # registered plugins; `mu plugin info <name>`
mu cache ls                     # cached action results; also size, clean, push
mu guide <topic>                # reference topics; `mu guide` lists them
```
