package config

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/load"
)

// schemaCUE embeds internal/config/schema.cue so the CUE decoder can
// unify decoded values against the canonical schema without depending on
// the source tree at runtime.
//
//go:embed schema.cue
var schemaCUE []byte

// cueDecoder is the CUE-based implementation of the Decoder interface.
//
// Behavior:
//   - Loads <dir>/mu.cue via cuelang.org/go/cue/load.
//   - Unifies the decoded value against #ProjectConfig (for Decode) or
//     #PluginConfig (for DecodePlugin) from the embedded schema.cue.
//   - Decodes into the corresponding Go struct using the existing `json:`
//     field tags (cue.Value.Decode honors json tags).
//   - Sorts Targets by Target name after decoding, because CUE struct
//     fields are unordered whereas JSON arrays preserve declaration
//     order. Downstream code must not assume CUE-declaration order.
//
// Numeric type drift: cue.Value.Decode produces int64 for integers and
// float64 for non-integer numbers. The JSON decoder, by contrast,
// produces float64 uniformly for map[string]any destinations. Callers
// that inspect schema-free maps such as Target.Config MUST handle both
// shapes. The ToFloat64 / ToInt64 helpers in numconv.go (mcm-07) do
// exactly this and should be used at every such inspection site.
type cueDecoder struct{}

// Decode loads mu.cue from projectRoot and unifies it with #ProjectConfig
// before decoding into ProjectConfig.
func (cueDecoder) Decode(projectRoot string) (*ProjectConfig, error) {
	val, err := loadCUEFile(projectRoot, "mu.cue")
	if err != nil {
		return nil, err
	}
	_, projectDef, _, err := compileSchemaDefs(val.Context())
	if err != nil {
		return nil, err
	}
	unified := projectDef.Unify(val)
	if err := unified.Err(); err != nil {
		return nil, formatCueError("unifying mu.cue with #ProjectConfig", val, err)
	}
	if err := unified.Validate(cue.Concrete(true)); err != nil {
		return nil, formatCueError("validating mu.cue against #ProjectConfig", val, err)
	}
	var cfg ProjectConfig
	if err := unified.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decoding mu.cue into ProjectConfig: %w", err)
	}
	sortTargetsByName(cfg.Targets)
	return &cfg, nil
}

// DecodePlugin loads mu.cue from pluginDir and unifies it with
// #PluginConfig before decoding into PluginConfig.
func (cueDecoder) DecodePlugin(pluginDir string) (*PluginConfig, error) {
	val, err := loadCUEFile(pluginDir, "mu.cue")
	if err != nil {
		return nil, err
	}
	_, _, pluginDef, err := compileSchemaDefs(val.Context())
	if err != nil {
		return nil, err
	}
	unified := pluginDef.Unify(val)
	if err := unified.Err(); err != nil {
		return nil, formatCueError("unifying mu.cue with #PluginConfig", val, err)
	}
	if err := unified.Validate(cue.Concrete(true)); err != nil {
		return nil, formatCueError("validating mu.cue against #PluginConfig", val, err)
	}
	var cfg PluginConfig
	if err := unified.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decoding mu.cue into PluginConfig: %w", err)
	}
	path := filepath.Join(pluginDir, "mu.cue")
	if cfg.Plugin == nil {
		return nil, fmt.Errorf("plugin manifest %s: missing \"plugin\" key", path)
	}
	if cfg.Plugin.Entrypoint == "" {
		return nil, fmt.Errorf("plugin manifest %s: \"entrypoint\" is required", path)
	}
	sortTargetsByName(cfg.Targets)
	return &cfg, nil
}

// IsCuePluginDir reports whether the given mu.cue file (raw bytes)
// contains a top-level "plugin" field. This mirrors IsPluginDir for the
// JSON decoder and is used by callers (e.g. the project loader) to
// decide whether a sub-tree mu.cue is a plugin manifest.
func IsCuePluginDir(data []byte) bool {
	ctx := cuecontext.New()
	v := ctx.CompileBytes(data, cue.Filename("mu.cue"))
	if v.Err() != nil {
		return false
	}
	p := v.LookupPath(cue.ParsePath("plugin"))
	return p.Exists()
}

// compileSchemaDefs compiles the embedded schema.cue in the given context
// and returns the root value together with the #ProjectConfig and
// #PluginConfig definitions.
func compileSchemaDefs(ctx *cue.Context) (schema, projectDef, pluginDef cue.Value, err error) {
	schema = ctx.CompileBytes(schemaCUE, cue.Filename("schema.cue"))
	if e := schema.Err(); e != nil {
		err = fmt.Errorf("compile embedded schema.cue: %w", e)
		return
	}
	projectDef = schema.LookupPath(cue.ParsePath("#ProjectConfig"))
	if e := projectDef.Err(); e != nil {
		err = fmt.Errorf("lookup #ProjectConfig: %w", e)
		return
	}
	pluginDef = schema.LookupPath(cue.ParsePath("#PluginConfig"))
	if e := pluginDef.Err(); e != nil {
		err = fmt.Errorf("lookup #PluginConfig: %w", e)
		return
	}
	return
}

// loadCUEFile loads a single .cue file from dir via cuelang.org/go/cue/load
// and returns the built cue.Value. Missing-file errors are returned with
// the full path for clarity.
func loadCUEFile(dir, file string) (cue.Value, error) {
	path := filepath.Join(dir, file)
	if _, err := os.Stat(path); err != nil {
		return cue.Value{}, fmt.Errorf("reading %s: %w", path, err)
	}
	cfg := &load.Config{Dir: dir}
	insts := load.Instances([]string{file}, cfg)
	if len(insts) == 0 {
		return cue.Value{}, fmt.Errorf("no CUE instances loaded from %s", path)
	}
	inst := insts[0]
	if inst.Err != nil {
		return cue.Value{}, fmt.Errorf("loading %s: %w", path, inst.Err)
	}
	ctx := cuecontext.New()
	val := ctx.BuildInstance(inst)
	if err := val.Err(); err != nil {
		return cue.Value{}, fmt.Errorf("building %s: %w", path, err)
	}
	return val, nil
}

// sortTargetsByName sorts targets by Name in ascending order. CUE struct
// fields are unordered, so any decoded Target slice must be normalized
// before downstream consumers compare or hash it.
func sortTargetsByName(ts []Target) {
	sort.SliceStable(ts, func(i, j int) bool {
		return ts[i].Name < ts[j].Name
	})
}
