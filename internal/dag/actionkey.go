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
// SealedInputs are deliberately excluded: secrets must never appear in cache
// keys, stored action results, or any persistent artifact. Actions that use
// sealed inputs may still be cached based on their non-secret inputs.
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

	return cas.ActionKey{Digest: cas.NewSHA256(hex.EncodeToString(h.Sum(nil)))}
}
