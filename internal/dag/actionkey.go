package dag

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"

	"github.com/chau/mu/internal/cas"
)

// ComputeActionKey computes a deterministic cache key for an Action.
// The key is a SHA-256 hash of the canonical representation of:
//   - command args (in original order)
//   - sorted env vars (key=value)
//   - sorted input digests (name -> hash)
//   - network flag
//   - impure flag
//   - work_dir (if set)
//
// The impure flag is included so that if a plugin version flips an action's
// impurity without changing any other field, the cache key changes. Without
// this, a pure run could in principle match a previously-impure entry under
// future cache-policy changes.
//
// SealedInputs *values* are deliberately excluded: secrets must never appear
// in cache keys, stored action results, or any persistent artifact. The
// *refs* and *delivery modes* are non-secret metadata and are hashed —
// changing the destination ref or the mode (env vs file) changes observable
// behavior, so the cache must invalidate.
//
// SealedOutputs: only the *refs* (destinations) are included in the key —
// never values, since at key-time there is no value yet. In practice
// actions with non-empty SealedOutputs are forced impure by the resolver
// (caching would skip the store_secret side-effect), but hashing the refs
// keeps the key well-defined and means changing a destination ref
// invalidates any cached result if impurity is ever relaxed.
//
// All maps are sorted by key before hashing to ensure determinism.
// Command args preserve their original order since command order matters.
func ComputeActionKey(a *Action) cas.ActionKey {
	h := sha256.New()

	// Command args in original order.
	for _, arg := range a.Command {
		fmt.Fprintf(h, "cmd:%s\n", arg)
	}

	// Env vars sorted by key.
	envKeys := make([]string, 0, len(a.Env))
	for k := range a.Env {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		fmt.Fprintf(h, "env:%s=%s\n", k, a.Env[k])
	}

	// Input digests sorted by name.
	inputNames := make([]string, 0, len(a.Inputs))
	for name := range a.Inputs {
		inputNames = append(inputNames, name)
	}
	sort.Strings(inputNames)
	for _, name := range inputNames {
		fmt.Fprintf(h, "input:%s=%s\n", name, a.Inputs[name].String())
	}

	// Network flag.
	fmt.Fprintf(h, "network:%t\n", a.Network)

	// Impure flag.
	fmt.Fprintf(h, "impure:%t\n", a.Impure)

	// Include work_dir in the key.
	if a.WorkDir != "" {
		fmt.Fprintf(h, "work_dir:%s\n", a.WorkDir)
	}

	// Sealed-input refs and modes (refs and modes are non-secret;
	// changing either alters what the action observes, so the key must
	// invalidate). Values are NOT hashed — they are excluded by design.
	if len(a.SealedInputs) > 0 {
		inNames := make([]string, 0, len(a.SealedInputs))
		for k := range a.SealedInputs {
			inNames = append(inNames, k)
		}
		sort.Strings(inNames)
		for _, k := range inNames {
			mode := a.SealedInputModes[k]
			if mode == "" {
				mode = "env"
			}
			fmt.Fprintf(h, "sealed_in:%s=%s mode=%s\n", k, a.SealedInputs[k], mode)
		}
	}

	// Sealed-output destination refs (names + refs only — values never exist
	// at key time). Sorted by name for determinism.
	if len(a.SealedOutputs) > 0 {
		outNames := make([]string, 0, len(a.SealedOutputs))
		for k := range a.SealedOutputs {
			outNames = append(outNames, k)
		}
		sort.Strings(outNames)
		for _, k := range outNames {
			fmt.Fprintf(h, "sealed_out:%s=%s\n", k, a.SealedOutputs[k])
		}
	}

	return cas.ActionKey{Digest: cas.NewSHA256(hex.EncodeToString(h.Sum(nil)))}
}
