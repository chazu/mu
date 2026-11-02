package coordinator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/chazu/mu/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// legacySchemaWarned records the plugins already warned about for the
// deprecated field-map config_schema, once per plugin per process.
var legacySchemaWarned sync.Map

// ValidateTargetConfig checks a target's config against the config_schema
// its plugin reported from discover. It returns nil if the config is valid
// and otherwise an error listing each violation as the JSON Schema
// library reports it (instance path + reason).
//
// The schema is a JSON Schema (Draft 7 unless it names another with
// $schema) describing the whole config object; plugins close it with
// "additionalProperties": false so an unknown field is an error.
//
// Deprecated dialect: a field-keyed map ({"field": {"type": ...,
// "required": true}}) is still translated into JSON Schema for one
// release. It prints a deprecation warning naming the plugin, and an
// unknown field is only a warning.
func ValidateTargetConfig(target config.Target, schema map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	cfg := target.Config
	if cfg == nil {
		cfg = map[string]any{}
	}

	if !isJSONSchema(schema) {
		if _, already := legacySchemaWarned.LoadOrStore(target.Toolchain, true); !already {
			fmt.Fprintf(schemaWarnOut,
				"warning: plugin %q reports a legacy field-map config_schema; it is deprecated and will stop being accepted in the next release — report a JSON Schema ({\"type\": \"object\", \"properties\": {...}})\n",
				target.Toolchain)
		}
		for key := range cfg {
			if _, ok := schema[key]; !ok {
				fmt.Fprintf(schemaWarnOut, "warning: target %q: unknown config field %q (not in plugin schema)\n", target.Name, key)
			}
		}
		schema = translateLegacySchema(schema)
	}

	compiled, err := compileSchema(target.Name, schema)
	if err != nil {
		return fmt.Errorf("target %q: plugin %q config_schema: %w", target.Name, target.Toolchain, err)
	}
	instance, err := normalizeForValidation(cfg)
	if err != nil {
		return fmt.Errorf("target %q: %w", target.Name, err)
	}
	if err := compiled.Validate(instance); err != nil {
		return validationError(target.Name, err)
	}
	return nil
}

// isJSONSchema reports whether schema is a real JSON Schema rather than the
// legacy field map. A legacy map may name a field "type" or "properties",
// but its value is then a field descriptor (a map), never a type string;
// and a legacy "properties" field's descriptor carries a string "type".
func isJSONSchema(schema map[string]any) bool {
	if _, ok := schema["$schema"].(string); ok {
		return true
	}
	switch schema["type"].(type) {
	case string, []any, []string:
		return true
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		_, descriptor := props["type"].(string)
		return !descriptor
	}
	return false
}

// translateLegacySchema rewrites the deprecated field-keyed schema into an
// open JSON Schema object: per-field "required": true flags become the
// required list, and the "items": "string" shorthand becomes
// {"type": "string"}.
func translateLegacySchema(schema map[string]any) map[string]any {
	properties := make(map[string]any, len(schema))
	required := []string{}
	for field, raw := range schema {
		sub, ok := raw.(map[string]any)
		if !ok {
			continue // malformed entry: skipped, as before
		}
		clone := make(map[string]any, len(sub))
		for k, v := range sub {
			clone[k] = v
		}
		if req, _ := clone["required"].(bool); req {
			required = append(required, field)
		}
		delete(clone, "required")
		if items, ok := clone["items"].(string); ok {
			clone["items"] = map[string]any{"type": items}
		}
		properties[field] = clone
	}
	sort.Strings(required)
	return map[string]any{
		"type":       "object",
		"properties": properties,
		"required":   required,
	}
}

func compileSchema(targetName string, schema map[string]any) (*jsonschema.Schema, error) {
	schemaJSON, err := json.Marshal(schema)
	if err != nil {
		return nil, fmt.Errorf("marshalling: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft7 // used when the schema names no $schema
	url := "mu://config_schema/" + targetName
	if err := compiler.AddResource(url, bytes.NewReader(schemaJSON)); err != nil {
		return nil, err
	}
	return compiler.Compile(url)
}

// normalizeForValidation round-trips the config through JSON so numbers
// are float64 and maps are map[string]any, as the validator expects.
func normalizeForValidation(cfg map[string]any) (any, error) {
	buf, err := json.Marshal(cfg)
	if err != nil {
		return nil, fmt.Errorf("marshalling config: %w", err)
	}
	var out any
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, fmt.Errorf("unmarshalling config: %w", err)
	}
	return out, nil
}

// validationError renders each leaf violation as `config<path>: <reason>`,
// e.g. `config/goos: value must be one of "linux", "darwin"`.
func validationError(targetName string, err error) error {
	vErr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return fmt.Errorf("target %q: %v", targetName, err)
	}
	var msgs []string
	var walk func(*jsonschema.ValidationError)
	walk = func(e *jsonschema.ValidationError) {
		if len(e.Causes) == 0 {
			msgs = append(msgs, "config"+e.InstanceLocation+": "+e.Message)
			return
		}
		for _, c := range e.Causes {
			walk(c)
		}
	}
	walk(vErr)
	return fmt.Errorf("target %q: %s", targetName, strings.Join(msgs, "; "))
}
