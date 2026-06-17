// keypair-gen plugin: generate an EC keypair (ed25519 or ecdsa) and route
// both halves into sealed_outputs so they land in a secret backend instead
// of on disk or in CAS.
//
// Port of the Babashka plugin (plugin.bb) to Go using sdk/muplugin. The
// discover response, action shape, and sealed-output wiring are preserved.
//
// Sealed outputs CONTRACT (target side):
//
//	sealed_outputs MUST contain exactly two keys named "PRIVATE" and
//	"PUBLIC". The plan rejects any other key set.
//
//	sealed_outputs:      {PRIVATE: "pass:raw:deploy/key", PUBLIC: "pass:deploy/key.pub"}
//	sealed_output_modes: {PRIVATE: "create_if_absent",     PUBLIC: "create_if_absent"}
//
// The action runs ssh-keygen (no passphrase) into a per-action tmp dir,
// moves both files into $MU_SEALED_OUT_DIR, chmods 0600, and cleans up.
package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/chazu/mu/sdk/muplugin"
)

var curveBits = map[string]string{
	"P-256": "256",
	"P-384": "384",
	"P-521": "521",
}

var requiredOutputNames = []string{"PRIVATE", "PUBLIC"}

func main() {
	(&muplugin.Plugin{
		Name:        "keypair-gen",
		Version:     "0.1.0",
		Description: "Generate SSH key pairs (Ed25519 or ECDSA) as sealed outputs",
		Produces:    []string{},
		Consumes:    []string{},
		ConfigSchema: map[string]any{
			"type": map[string]any{
				"type":        "string",
				"default":     "ed25519",
				"enum":        []string{"ed25519", "ecdsa"},
				"description": "Key algorithm",
			},
			"curve": map[string]any{
				"type":        "string",
				"default":     "P-256",
				"enum":        []string{"P-256", "P-384", "P-521"},
				"description": "ECDSA curve (ignored for Ed25519)",
			},
			"comment": map[string]any{
				"type":        "string",
				"description": "Key comment embedded in the public key",
			},
		},
		Plan: plan,
	}).Main()
}

// shellQuote single-quotes a string for safe inclusion in a bash command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// keygenArgs returns the ssh-keygen flags for the requested key type.
func keygenArgs(keyType, curve string) ([]string, error) {
	switch keyType {
	case "ed25519":
		return []string{"-t", "ed25519"}, nil
	case "ecdsa":
		bits, ok := curveBits[curve]
		if !ok {
			keys := make([]string, 0, len(curveBits))
			for k := range curveBits {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			return nil, fmt.Errorf("keypair-gen: unsupported curve %s (want one of %s)",
				curve, strings.Join(keys, ","))
		}
		return []string{"-t", "ecdsa", "-b", bits}, nil
	default:
		return nil, fmt.Errorf("keypair-gen: unsupported type %s (want \"ed25519\" or \"ecdsa\")", keyType)
	}
}

// buildScript renders the bash that ssh-keygen's into a temp dir, then
// moves both halves into $MU_SEALED_OUT_DIR/{PRIVATE,PUBLIC} at mode 0600.
func buildScript(keyType, curve, comment string) (string, error) {
	args, err := keygenArgs(keyType, curve)
	if err != nil {
		return "", err
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = shellQuote(a)
	}
	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("if [ -z \"${MU_SEALED_OUT_DIR:-}\" ]; then\n")
	b.WriteString("  echo 'keypair-gen: MU_SEALED_OUT_DIR is unset' >&2\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	b.WriteString("TMPDIR=$(mktemp -d)\n")
	b.WriteString("trap 'rm -rf \"$TMPDIR\"' EXIT\n")
	b.WriteString("chmod 700 \"$TMPDIR\"\n")
	b.WriteString("ssh-keygen -q -N '' ")
	b.WriteString(strings.Join(quoted, " "))
	b.WriteString(" -C ")
	b.WriteString(shellQuote(comment))
	b.WriteString(" -f \"$TMPDIR/k\"\n")
	b.WriteString("mv \"$TMPDIR/k\" \"$MU_SEALED_OUT_DIR/PRIVATE\"\n")
	b.WriteString("mv \"$TMPDIR/k.pub\" \"$MU_SEALED_OUT_DIR/PUBLIC\"\n")
	b.WriteString("chmod 600 \"$MU_SEALED_OUT_DIR/PRIVATE\" \"$MU_SEALED_OUT_DIR/PUBLIC\"\n")
	return b.String(), nil
}

func plan(_ context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	tgtName := req.Target.Name
	if tgtName == "" {
		tgtName = "keypair"
	}
	cfg := req.Target.Config

	keyType := "ed25519"
	if v, ok := cfg["type"].(string); ok && v != "" {
		keyType = v
	}
	curve := "P-256"
	if v, ok := cfg["curve"].(string); ok && v != "" {
		curve = v
	}
	comment := tgtName
	if v, ok := cfg["comment"].(string); ok && v != "" {
		comment = v
	}

	sealedOut := req.Target.SealedOutputs
	// The bb plugin reads sealed_output_modes from target, but the
	// coordinator never sends it on the target envelope (only on actions),
	// so it is effectively always empty here. The SDK's TargetInfo lacks
	// the field, matching that reality. See plugins/file/main.go for the
	// same precedent.
	sealedOutModes := map[string]string{}

	if len(sealedOut) == 0 {
		return muplugin.PlanResponse{
			Error: "keypair-gen: sealed_outputs is required (must declare PRIVATE and PUBLIC)",
		}, nil
	}

	if !hasExactKeys(sealedOut, requiredOutputNames) {
		gotKeys := make([]string, 0, len(sealedOut))
		for k := range sealedOut {
			gotKeys = append(gotKeys, k)
		}
		sort.Strings(gotKeys)
		return muplugin.PlanResponse{
			Error: fmt.Sprintf("keypair-gen: sealed_outputs keys must be exactly %s (got %s)",
				formatSet(requiredOutputNames), formatSet(gotKeys)),
		}, nil
	}

	script, err := buildScript(keyType, curve, comment)
	if err != nil {
		return muplugin.PlanResponse{Error: err.Error()}, nil
	}

	action := muplugin.ActionSpec{
		ID:            "generate",
		Command:       []string{"bash", "-c", script},
		Inputs:        map[string]string{},
		Outputs:       []string{},
		DependsOn:     []string{},
		Env:           map[string]string{},
		Network:       false,
		SealedOutputs: sealedOut,
	}
	if len(sealedOutModes) > 0 {
		action.SealedOutputModes = sealedOutModes
	}

	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{action},
		Outputs: map[string]string{},
	}, nil
}

// hasExactKeys reports whether m's key set equals the given names (as a set).
func hasExactKeys(m map[string]string, names []string) bool {
	if len(m) != len(names) {
		return false
	}
	for _, n := range names {
		if _, ok := m[n]; !ok {
			return false
		}
	}
	return true
}

// formatSet renders a sorted name slice as Clojure-style "#{a b}" so error
// strings echo the bb plugin's wording.
func formatSet(names []string) string {
	sorted := append([]string(nil), names...)
	sort.Strings(sorted)
	quoted := make([]string, len(sorted))
	for i, n := range sorted {
		quoted[i] = "\"" + n + "\""
	}
	return "#{" + strings.Join(quoted, " ") + "}"
}
