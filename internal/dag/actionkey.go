package dag

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strconv"

	"github.com/chazu/mu/internal/cas"
)

// ComputeActionKey hashes the resolved execution identity. Every string is
// length-prefixed: commands, names and values may contain newlines or '='
// without impersonating another field. The version separates this encoding
// from legacy keys. Maps and declaration sets are sorted; argv order matters.
//
// Sealed values never enter the key. Only refs and effective delivery/store
// modes are hashed. Action IDs and dependency IDs are excluded: resolved input
// digests identify upstream content. Timeout/retry policy does not identify a
// successful build's artifacts.
func ComputeActionKey(a *Action) cas.ActionKey {
	h := sha256.New()
	field := func(parts ...string) {
		for _, part := range parts {
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(len(part)))
			_, _ = h.Write(size[:])
			_, _ = h.Write([]byte(part))
		}
	}
	field("mu.action-key/v2")
	for _, arg := range a.Command {
		field("cmd", arg)
	}
	if len(a.Body) > 0 {
		if b, err := json.Marshal(a.Body); err == nil {
			field("body", string(b))
		}
	}
	if !a.EweRef.IsZero() {
		field("ewe", a.EweRef.String())
	}
	for _, name := range sortedKeys(a.Env) {
		field("env", name, a.Env[name])
	}
	// A nil environment inherits the parent; an explicitly empty map does
	// not. Preserve that execution distinction even though both have no keys.
	field("env_inherit", strconv.FormatBool(a.Env == nil))
	for _, name := range sortedKeys(a.Inputs) {
		field("input", name, a.Inputs[name].String())
	}
	for _, name := range sortedKeys(a.Toolchain) {
		field("toolchain", name, a.Toolchain[name].String())
	}
	field("sandbox", strconv.FormatBool(a.Toolchain != nil))
	for _, name := range sortedStrings(a.Outputs) {
		field("output", name)
	}
	for _, name := range sortedStrings(a.Sources) {
		field("source", name)
	}
	field("network", strconv.FormatBool(a.Network))
	field("impure", strconv.FormatBool(a.Impure))
	field("work_dir", a.WorkDir)
	for _, name := range sortedKeys(a.SealedInputs) {
		mode := a.SealedInputModes[name]
		if mode == "" {
			mode = "env"
		}
		field("sealed_in", name, a.SealedInputs[name], mode)
	}
	for _, name := range sortedKeys(a.SealedOutputs) {
		mode := a.SealedOutputModes[name]
		if mode == "" {
			mode = "overwrite"
		}
		field("sealed_out", name, a.SealedOutputs[name], mode)
	}
	return cas.ActionKey{Digest: cas.NewSHA256(hex.EncodeToString(h.Sum(nil)))}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedStrings(values []string) []string {
	copy := append([]string(nil), values...)
	sort.Strings(copy)
	return copy
}
