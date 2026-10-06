package dag

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
