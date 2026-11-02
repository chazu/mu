package dag

import (
	"maps"
	"strings"
)

// DefaultActionPath is deliberately fixed, rather than copied from the parent.
// Toolchain-specific paths and variables must be explicitly declared by plugins.
const DefaultActionPath = "/usr/bin:/bin"

// ActionEnvironment defines the cacheable execution environment. Pure actions
// with no env declaration get an empty environment. Bare command lookup uses
// a fixed system PATH when PATH is absent. Explicit empty maps remain empty.
// Only impure bare commands can inherit the parent environment; VM and sandbox
// execution never imports the ambient environment.
func ActionEnvironment(env map[string]string, impure bool) map[string]string {
	if env == nil && !impure {
		return map[string]string{}
	}
	return env
}

// actionEnv is the single place an action's execution environment is built.
// Layers, later ones winning:
//
//  1. Base. Pure actions get ActionEnvironment(a.Env, false): the declared env
//     (already hashed into the action key), never the host. An impure action
//     with no env declaration (a.Env == nil) running a bare command starts from
//     the host environment; otherwise it gets its declared env.
//  2. a.EnvPassthrough: each named variable is copied from host when set there.
//     Only the names are part of the action key, never the values.
//  3. muVars: per-execution MU_* runtime variables (MU_OUT, MU_SEALED_OUT_DIR).
//  4. sealed: delivered sealed inputs (values for env mode, paths for file
//     mode). Secrets go last so they always win.
//
// The result is a fresh, non-nil map; a.Env is never mutated.
func actionEnv(a *Action, host []string, muVars, sealed map[string]string) map[string]string {
	hostVars := make(map[string]string, len(host))
	for _, entry := range host {
		if name, value, ok := strings.Cut(entry, "="); ok {
			hostVars[name] = value
		}
	}
	env := make(map[string]string)
	declared := ActionEnvironment(a.Env, a.Impure)
	// VM and sandbox execution never imports the ambient environment.
	bare := a.Toolchain == nil && a.EweRef.IsZero() && len(a.Body) == 0
	if declared == nil && bare {
		maps.Copy(env, hostVars)
	} else {
		maps.Copy(env, declared)
	}
	for _, name := range a.EnvPassthrough {
		if value, ok := hostVars[name]; ok {
			env[name] = value
		}
	}
	maps.Copy(env, muVars)
	maps.Copy(env, sealed)
	return env
}
