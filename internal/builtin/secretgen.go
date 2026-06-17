package builtin

import (
	"fmt"
	"strings"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

// SecretGenPlan synthesizes the action for a "secret-gen" target. The
// toolchain runs a derivation command, captures its stdout into the
// per-action sealed-output side channel, and lets the executor route the
// value through the configured secret-provider plugin's store_secret
// method. By default the store mode is "create_if_absent" — re-running
// is cheap and idempotent and the bootstrapping case (mint a credential
// once, never overwrite) is the load-bearing one.
//
// Config fields:
//   - ref         (string, required) — destination secret ref, e.g.
//                 "pass:registry/admin". Must be of the form scheme:path.
//   - derivation  ([]string, required) — argv whose stdout becomes the
//                 stored value. Trailing newlines are stripped before
//                 storing.
//   - mode        (string, optional) — "create" | "overwrite" |
//                 "create_if_absent" (default).
//   - env         (map[string]string, optional) — extra env for the
//                 derivation; useful for pinning randomness sources.
//   - keep_trailing_newline (bool, optional) — if true, the value is
//                 stored exactly as the derivation emitted it. Default
//                 false: trim a trailing newline (the common case for
//                 `openssl rand`, `uuidgen`, etc.).
//
// The action is always impure (the resolver enforces this for any
// action with sealed_outputs), so the derivation runs every build. For
// "create_if_absent" the provider treats existing entries as a no-op,
// so wasted work is bounded to the derivation itself; pure-Go derived
// values are typically free.
func SecretGenPlan(target config.Target) ([]plugin.ActionSpec, map[string]string, error) {
	cfg := target.Config
	if cfg == nil {
		return nil, nil, fmt.Errorf("secret-gen target %q: config is required", target.Name)
	}

	ref, _ := cfg["ref"].(string)
	if ref == "" {
		return nil, nil, fmt.Errorf("secret-gen target %q: \"ref\" is required and must be a non-empty string", target.Name)
	}
	if i := strings.IndexByte(ref, ':'); i <= 0 || i >= len(ref)-1 {
		return nil, nil, fmt.Errorf("secret-gen target %q: \"ref\" %q must be of the form scheme:path", target.Name, ref)
	}

	derivation, err := parseStringSlice(cfg, "derivation")
	if err != nil {
		return nil, nil, fmt.Errorf("secret-gen target %q: %w", target.Name, err)
	}
	if len(derivation) == 0 {
		return nil, nil, fmt.Errorf("secret-gen target %q: \"derivation\" is required and must be a non-empty array of strings", target.Name)
	}

	mode := plugin.StoreSecretModeCreateIfAbsent
	if v, ok := cfg["mode"].(string); ok && v != "" {
		switch v {
		case plugin.StoreSecretModeCreate,
			plugin.StoreSecretModeOverwrite,
			plugin.StoreSecretModeCreateIfAbsent:
			mode = v
		default:
			return nil, nil, fmt.Errorf("secret-gen target %q: \"mode\" %q must be one of create | overwrite | create_if_absent", target.Name, v)
		}
	}

	keepNewline := parseBool(cfg, "keep_trailing_newline", false)

	env := parseStringMap(cfg, "env")

	// The action runs the derivation through bash so we can both redirect
	// stdout into the side-channel file and (optionally) trim a single
	// trailing newline. Quoting argv via printf %q keeps spaces and weird
	// characters safe.
	argv := derivationToShell(derivation)
	const outName = "VALUE"
	var script string
	if keepNewline {
		script = fmt.Sprintf(`set -eu; %s > "$MU_SEALED_OUT_DIR/%s"`, argv, outName)
	} else {
		// Capture, strip exactly one trailing newline if present, write.
		script = fmt.Sprintf(`set -eu
v=$(%s; printf x)
v=${v%%x}
v=${v%%$'\n'}
printf '%%s' "$v" > "$MU_SEALED_OUT_DIR/%s"`, argv, outName)
	}

	action := plugin.ActionSpec{
		ID:      "gen",
		Command: []string{"bash", "-c", script},
		Env:     env,
		Network: false,
		// Impure is forced true by Resolve when SealedOutputs is non-empty,
		// but mark it explicitly here so the intent is visible in plan output.
		Impure: true,
		SealedOutputs: map[string]string{
			outName: ref,
		},
		SealedOutputModes: map[string]string{
			outName: mode,
		},
	}

	return []plugin.ActionSpec{action}, nil, nil
}

// derivationToShell returns the derivation argv joined into a single
// bash command, with each element quoted via printf %q so spaces,
// quotes, and other metacharacters survive.
func derivationToShell(argv []string) string {
	var b strings.Builder
	for i, a := range argv {
		if i > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(shellSingleQuote(a))
	}
	return b.String()
}

// shellSingleQuote single-quotes s for safe inclusion in a bash
// command. Single quotes inside s are escaped via the usual
// '"'"' construction.
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
