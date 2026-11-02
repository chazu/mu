package dag

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"path/filepath"
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
// successful build's artifacts. The checkout location does not either: only
// the project-relative work directory is hashed (v4), never the project root.
// Whether the action runs sandboxed is hashed, so enabling MU_SANDBOX does not
// reuse results produced on the bare host.
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
	field("mu.action-key/v5")
	for _, arg := range a.Command {
		field("cmd", arg)
	}
	env := ActionEnvironment(a.Env, a.Impure)
	for _, name := range sortedKeys(env) {
		field("env", name, env[name])
	}
	// Only impure actions may inherit. Pure defaults are explicitly hashed.
	field("env_inherit", strconv.FormatBool(env == nil))
	// Passthrough names, never their host values.
	for _, name := range sortedStrings(a.EnvPassthrough) {
		field("env_passthrough", name)
	}
	for _, name := range sortedKeys(a.Inputs) {
		field("input", name, a.Inputs[name].String())
	}
	// Where each input lands in the sandbox work tree is visible to the
	// command, so it is part of the identity (v5).
	for _, name := range sortedKeys(a.InputPaths) {
		field("input_path", name, a.InputPaths[name])
	}
	for _, name := range sortedKeys(a.Toolchain) {
		field("toolchain", name, a.Toolchain[name].String())
	}
	field("sandbox", strconv.FormatBool(a.Sandboxed()))
	for _, name := range sortedStrings(a.Outputs) {
		field("output", name)
	}
	field("network", strconv.FormatBool(a.Network))
	field("impure", strconv.FormatBool(a.Impure))
	field("work_dir", keyWorkDir(a))
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

// keyWorkDir is the work directory as it enters the action key: relative to
// the project root and slash-separated, so checkouts of the same project at
// different paths (or on different machines) share cache entries. Actions
// without a project root have nothing to be relative to and keep WorkDir.
func keyWorkDir(a *Action) string {
	if a.ProjectRoot == "" {
		return a.WorkDir
	}
	if a.WorkDir == "" {
		return "."
	}
	rel, err := filepath.Rel(a.ProjectRoot, a.WorkDir)
	if err != nil {
		return a.WorkDir
	}
	return filepath.ToSlash(rel)
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
