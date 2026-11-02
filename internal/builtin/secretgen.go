package builtin

import (
	"fmt"
	"maps"
	"strings"

	"github.com/chazu/mu/internal/config"
	"github.com/chazu/mu/internal/plugin"
)

// SecretGenDefaults returns target with secret-gen's default store mode
// applied: when it declares a single sealed output with no mode, the mode is
// "create_if_absent" (re-running is cheap and idempotent, and the
// bootstrapping case — mint a credential once, never overwrite — is the
// load-bearing one). The coordinator validates sealed routing against the
// returned target so the action's claim and the declaration agree. Maps are
// cloned; target is not modified.
func SecretGenDefaults(target config.Target) config.Target {
	if len(target.SealedOutputs) != 1 {
		return target
	}
	for name := range target.SealedOutputs {
		if target.SealedOutputModes[name] == "" {
			modes := maps.Clone(target.SealedOutputModes)
			if modes == nil {
				modes = make(map[string]string, 1)
			}
			modes[name] = plugin.StoreSecretModeCreateIfAbsent
			target.SealedOutputModes = modes
		}
	}
	return target
}

// SecretGenPlan synthesizes the action for a "secret-gen" target. The
// toolchain runs a derivation command, captures its stdout into the
// per-action sealed-output side channel, and lets the executor route the
// value through the configured secret-provider plugin's store_secret
// method.
//
// The destination is the target's single sealed_outputs entry (any name,
// conventionally VALUE), with its optional sealed_output_modes entry:
// "create" | "overwrite" | "create_if_absent" (the default, see
// SecretGenDefaults).
//
// Config fields:
//   - derivation  ([]string, required) — argv whose stdout becomes the
//     stored value. A non-zero exit or an empty value fails the
//     action, so nothing is stored.
//   - env         (map[string]string, optional) — extra env for the
//     derivation; useful for pinning randomness sources.
//   - keep_trailing_newline (bool, optional) — if true, the value is
//     stored exactly as the derivation emitted it. Default
//     false: trim a trailing newline (the common case for
//     `openssl rand`, `uuidgen`, etc.).
//
// config.ref and config.mode were removed in favour of sealed_outputs and
// sealed_output_modes; either one present is an error.
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
	if v, ok := cfg["ref"]; ok {
		ref, _ := v.(string)
		if ref == "" {
			ref = "<ref>"
		}
		return nil, nil, fmt.Errorf("secret-gen target %q: config.ref was removed; declare sealed_outputs: {VALUE: %q}", target.Name, ref)
	}
	if v, ok := cfg["mode"]; ok {
		mode, _ := v.(string)
		if mode == "" {
			mode = "<mode>"
		}
		return nil, nil, fmt.Errorf("secret-gen target %q: config.mode was removed; declare sealed_output_modes: {VALUE: %q}", target.Name, mode)
	}

	target = SecretGenDefaults(target)
	if len(target.SealedOutputs) != 1 {
		return nil, nil, fmt.Errorf("secret-gen target %q: declare exactly one sealed_outputs entry naming the destination, e.g. sealed_outputs: {VALUE: \"pass:path\"} (got %d)", target.Name, len(target.SealedOutputs))
	}
	var outName, ref string
	for name, r := range target.SealedOutputs {
		outName, ref = name, r
	}
	if outName == "" || outName == "." || outName == ".." || strings.ContainsAny(outName, `/\`) {
		return nil, nil, fmt.Errorf("secret-gen target %q: sealed output name %q must be a plain file name", target.Name, outName)
	}
	if i := strings.IndexByte(ref, ':'); i <= 0 || i >= len(ref)-1 {
		return nil, nil, fmt.Errorf("secret-gen target %q: sealed_outputs[%q] %q must be of the form scheme:path", target.Name, outName, ref)
	}
	mode := target.SealedOutputModes[outName]
	switch mode {
	case plugin.StoreSecretModeCreate,
		plugin.StoreSecretModeOverwrite,
		plugin.StoreSecretModeCreateIfAbsent:
	default:
		return nil, nil, fmt.Errorf("secret-gen target %q: sealed_output_modes[%q] %q must be one of create | overwrite | create_if_absent", target.Name, outName, mode)
	}

	derivation, err := parseStringSlice(cfg, "derivation")
	if err != nil {
		return nil, nil, fmt.Errorf("secret-gen target %q: %w", target.Name, err)
	}
	if len(derivation) == 0 {
		return nil, nil, fmt.Errorf("secret-gen target %q: \"derivation\" is required and must be a non-empty array of strings", target.Name)
	}

	keepNewline, err := parseBool(cfg, "keep_trailing_newline", false)
	if err != nil {
		return nil, nil, fmt.Errorf("secret-gen target %q: %w", target.Name, err)
	}

	env, err := parseStringMap(cfg, "env")
	if err != nil {
		return nil, nil, fmt.Errorf("secret-gen target %q: %w", target.Name, err)
	}

	// The action runs the derivation through bash so we can both redirect
	// stdout into the side-channel file and (optionally) trim a single
	// trailing newline. Quoting argv via printf %q keeps spaces and weird
	// characters safe.
	//
	// Both paths must fail the action (so nothing is stored) when the
	// derivation exits non-zero or emits nothing. errexit does not fire
	// inside a command substitution, so the default path chains the
	// sentinel with && — a failing derivation then skips the sentinel and
	// the substitution's status propagates to the assignment.
	argv := derivationToShell(derivation)
	outFile := `"$MU_SEALED_OUT_DIR"/` + shellSingleQuote(outName)
	const emptyErr = `{ echo 'secret-gen: derivation produced an empty value' >&2; exit 1; }`
	var script string
	if keepNewline {
		script = fmt.Sprintf(`set -eu
%s > %s
[ -s %s ] || %s`, argv, outFile, outFile, emptyErr)
	} else {
		// Capture, strip exactly one trailing newline if present, write.
		// The trailing "x" sentinel keeps $(...) from stripping every
		// trailing newline.
		script = fmt.Sprintf(`set -eu
v=$(%s && printf x) || exit $?
v=${v%%x}
v=${v%%$'\n'}
[ -n "$v" ] || %s
printf '%%s' "$v" > %s`, argv, emptyErr, outFile)
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
		// The derivation may read the target's sealed inputs (e.g. a
		// plaintext to hash); claim them explicitly since this action
		// already claims its output and so gets no automatic claims.
		SealedInputs:     maps.Clone(target.SealedInputs),
		SealedInputModes: maps.Clone(target.SealedInputModes),
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
