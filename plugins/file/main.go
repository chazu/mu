// file plugin: converge files to a desired state.
//
// Port of the original Babashka plugin (plugin.bb) to Go using sdk/muplugin.
// Output bytes match the bb version action-for-action so existing scenarios
// continue to pass. Same minor structural divergences as scratch's Go port
// (capabilities field added by SDK, omitempty trims empty depends_on/env).
package main

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/chazu/mu/sdk/muplugin"
)

func main() {
	(&muplugin.Plugin{
		Name:        "file",
		Version:     "0.2.0",
		Description: "Manage local files: copy, template, create, symlink, or remove",
		Consumes:    []string{"source:any"},
		Produces:    []string{"file"},
		ConfigSchema: map[string]any{
			"path":                map[string]any{"type": "string", "description": "Destination path on the local filesystem"},
			"source":              map[string]any{"type": "string", "description": "Source file to copy from"},
			"content":             map[string]any{"type": "string", "description": "Inline content to write (alternative to source)"},
			"mode":                map[string]any{"type": "string", "default": "0644", "description": "File permission mode"},
			"symlink":             map[string]any{"type": "string", "description": "Create a symlink pointing to this path"},
			"absent":              map[string]any{"type": "boolean", "default": false, "description": "Ensure the file does not exist"},
			"owner":               map[string]any{"type": "string", "description": "File owner (user name or UID)"},
			"group":               map[string]any{"type": "string", "description": "File group (group name or GID)"},
			"sealed_output_files": map[string]any{"type": "object", "description": "Map of sealed output names to file paths"},
		},
		Plan: plan,
	}).Main()
}

func plan(_ context.Context, req muplugin.PlanRequest) (muplugin.PlanResponse, error) {
	target := req.Target
	cfg := target.Config
	if cfg == nil {
		cfg = map[string]any{}
	}

	path, _ := cfg["path"].(string)
	mode, _ := cfg["mode"].(string)
	if mode == "" {
		mode = "0644"
	}
	sealedOut := target.SealedOutputs
	sealedOutModes := target.SealedOutputModes
	sealedFiles := stringMap(cfg["sealed_output_files"])

	capture := len(sealedOut) > 0 || len(sealedFiles) > 0

	if capture {
		outKeys := keySet(sealedOut)
		fileKeys := keySet(sealedFiles)
		if !sameKeys(outKeys, fileKeys) {
			return muplugin.PlanResponse{
				Error: fmt.Sprintf("file: sealed_outputs keys %s must match config.sealed_output_files keys %s",
					formatKeySet(outKeys), formatKeySet(fileKeys)),
			}, nil
		}
		if path != "" || hasString(cfg, "content") || hasString(cfg, "source") ||
			hasString(cfg, "symlink") || boolVal(cfg, "absent") {
			return muplugin.PlanResponse{
				Error: "file: capture mode (sealed_output_files) is mutually exclusive with path/content/source/symlink/absent",
			}, nil
		}
		return planCapture(sealedOut, sealedOutModes, sealedFiles), nil
	}

	if path == "" {
		return muplugin.PlanResponse{}, fmt.Errorf("file plugin requires \"path\" in config")
	}

	if boolVal(cfg, "absent") {
		return planAbsent(path), nil
	}
	if sym, ok := cfg["symlink"].(string); ok && sym != "" {
		return planSymlink(path, sym), nil
	}
	if content, ok := cfg["content"].(string); ok && content != "" {
		return planWriteContent(cfg, path, content, mode), nil
	}
	if src, ok := cfg["source"].(string); ok && src != "" {
		return planCopySource(cfg, path, src, mode), nil
	}
	if len(target.Sources) > 0 {
		return planCopySource(cfg, path, target.Sources[0], mode), nil
	}

	return muplugin.PlanResponse{
		Error: "file plugin requires one of: content, source, symlink, absent, or sealed_output_files",
	}, nil
}

func planAbsent(path string) muplugin.PlanResponse {
	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{{
			ID:        "remove",
			Command:   []string{"sh", "-c", "rm -f '" + path + "'"},
			Inputs:    map[string]string{},
			Outputs:   []string{},
			DependsOn: []string{},
			Env:       map[string]string{},
		}},
		Outputs: map[string]string{},
	}
}

func planSymlink(path, targetPath string) muplugin.PlanResponse {
	cmd := "mkdir -p \"$(dirname '" + path + "')\" && " +
		"ln -sfn '" + targetPath + "' '" + path + "'"
	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{{
			ID:        "symlink",
			Command:   []string{"sh", "-c", cmd},
			Inputs:    map[string]string{},
			Outputs:   []string{path},
			DependsOn: []string{},
			Env:       map[string]string{},
		}},
		Outputs: map[string]string{"file": path},
	}
}

func planWriteContent(cfg map[string]any, path, content, mode string) muplugin.PlanResponse {
	writeCmd := "mkdir -p \"$(dirname '" + path + "')\" && " +
		"cat > '" + path + "' <<'__MU_EOF__'\n" + content + "\n__MU_EOF__"
	actions := []muplugin.ActionSpec{
		{
			ID:        "write",
			Command:   []string{"sh", "-c", writeCmd},
			Inputs:    map[string]string{},
			Outputs:   []string{path},
			DependsOn: []string{},
			Env:       map[string]string{},
		},
		{
			ID:        "chmod",
			Command:   []string{"chmod", mode, path},
			Inputs:    map[string]string{},
			Outputs:   []string{},
			DependsOn: []string{"write"},
			Env:       map[string]string{},
		},
	}
	if owner, ok := cfg["owner"].(string); ok && owner != "" {
		actions = append(actions, chownAction(cfg, owner, path, "write"))
	}
	return muplugin.PlanResponse{
		Actions: actions,
		Outputs: map[string]string{"file": path},
	}
}

func planCopySource(cfg map[string]any, path, source, mode string) muplugin.PlanResponse {
	copyCmd := "mkdir -p \"$(dirname '" + path + "')\" && " +
		"cp '" + source + "' '" + path + "'"
	actions := []muplugin.ActionSpec{
		{
			ID:        "copy",
			Command:   []string{"sh", "-c", copyCmd},
			Inputs:    map[string]string{source: source},
			Outputs:   []string{path},
			DependsOn: []string{},
			Env:       map[string]string{},
		},
		{
			ID:        "chmod",
			Command:   []string{"chmod", mode, path},
			Inputs:    map[string]string{},
			Outputs:   []string{},
			DependsOn: []string{"copy"},
			Env:       map[string]string{},
		},
	}
	if owner, ok := cfg["owner"].(string); ok && owner != "" {
		actions = append(actions, chownAction(cfg, owner, path, "copy"))
	}
	return muplugin.PlanResponse{
		Actions: actions,
		Outputs: map[string]string{"file": path},
	}
}

func chownAction(cfg map[string]any, owner, path, dep string) muplugin.ActionSpec {
	ownerArg := owner
	if g, ok := cfg["group"].(string); ok && g != "" {
		ownerArg = owner + ":" + g
	}
	return muplugin.ActionSpec{
		ID:        "chown",
		Command:   []string{"chown", ownerArg, path},
		Inputs:    map[string]string{},
		Outputs:   []string{},
		DependsOn: []string{dep},
		Env:       map[string]string{},
	}
}

func planCapture(sealedOut, sealedOutModes, sealedFiles map[string]string) muplugin.PlanResponse {
	// Stable sort by key, matching bb's (sort-by key sealed-files).
	names := make([]string, 0, len(sealedFiles))
	for k := range sealedFiles {
		names = append(names, k)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("set -eu\n")
	b.WriteString("if [ -z \"${MU_SEALED_OUT_DIR:-}\" ]; then\n")
	b.WriteString("  echo 'file: capture mode requires MU_SEALED_OUT_DIR' >&2\n")
	b.WriteString("  exit 1\n")
	b.WriteString("fi\n")
	for _, name := range names {
		src := sealedFiles[name]
		fmt.Fprintf(&b, "cp %s \"$MU_SEALED_OUT_DIR/%s\"\n", shellQuote(src), name)
		fmt.Fprintf(&b, "chmod 600 \"$MU_SEALED_OUT_DIR/%s\"\n", name)
	}

	action := muplugin.ActionSpec{
		ID:            "capture",
		Command:       []string{"bash", "-c", b.String()},
		Inputs:        map[string]string{},
		Outputs:       []string{},
		DependsOn:     []string{},
		Env:           map[string]string{},
		SealedOutputs: sealedOut,
	}
	if len(sealedOutModes) > 0 {
		action.SealedOutputModes = sealedOutModes
	}
	return muplugin.PlanResponse{
		Actions: []muplugin.ActionSpec{action},
		Outputs: map[string]string{},
	}
}

// shellQuote single-quotes a string for safe inclusion in a bash command.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

func stringMap(v any) map[string]string {
	m, ok := v.(map[string]any)
	if !ok {
		return map[string]string{}
	}
	out := make(map[string]string, len(m))
	for k, val := range m {
		if s, ok := val.(string); ok {
			out[k] = s
		}
	}
	return out
}

func hasString(cfg map[string]any, key string) bool {
	s, ok := cfg[key].(string)
	return ok && s != ""
}

func boolVal(cfg map[string]any, key string) bool {
	b, _ := cfg[key].(bool)
	return b
}

func keySet(m map[string]string) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func sameKeys(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func formatKeySet(m map[string]struct{}) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	// Mimic Clojure set printing: #{"a" "b"}
	var b strings.Builder
	b.WriteString("#{")
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%q", k)
	}
	b.WriteByte('}')
	return b.String()
}
