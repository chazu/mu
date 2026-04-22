package coordinator

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/chau/mu/internal/config"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

// ValidateTargetConfig checks a target's config map against the plugin's
// declared config_schema from DiscoverResponse. Returns nil if valid.
// Logs warnings to stderr for unknown fields. Returns an error for type
// mismatches, missing required fields, or enum violations.
//
// Conventions (preserved from the legacy hand-rolled validator):
//
//   - The schema is a field-keyed map where each value is itself a
//     JSON-Schema-like object (e.g. {"type": "string", "default": "foo"}).
//   - A field is required iff it has an explicit "required": true flag.
//     Presence or absence of "default" does not affect required-ness;
//     unset optional fields rely on plugin-side defaults at plan time.
//   - Unknown fields produce a stderr warning but do not fail validation.
//
// Under the hood the field map is rewritten into canonical Draft-7 JSON
// Schema and compiled with santhosh-tekuri/jsonschema/v5 — so plugin
// schemas automatically get real pattern / enum / nested-object support
// without the coordinator re-implementing it.
func ValidateTargetConfig(target config.Target, schema map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	cfg := target.Config
	if cfg == nil {
		cfg = map[string]any{}
	}

	// Warn on unknown fields (in config but not in schema). Not an error:
	// the plugin may accept extensions the schema doesn't enumerate.
	unknownWarnings(target.Name, cfg, schema)

	compiled, err := compileFieldSchema(target.Name, schema)
	if err != nil {
		return fmt.Errorf("target %q: %w", target.Name, err)
	}

	instance, err := normalizeForValidation(cfg)
	if err != nil {
		return fmt.Errorf("target %q: %w", target.Name, err)
	}

	if err := compiled.Validate(instance); err != nil {
		return renderValidationError(target.Name, schema, instance, err)
	}
	return nil
}

// compileFieldSchema translates the legacy field-keyed schema into a
// canonical Draft-7 JSON Schema object and compiles it.
func compileFieldSchema(targetName string, schema map[string]any) (*jsonschema.Schema, error) {
	properties := make(map[string]any, len(schema))
	required := []string{}

	for field, raw := range schema {
		sub, ok := raw.(map[string]any)
		if !ok {
			// Malformed plugin schema entry: skip (legacy validator did the same).
			continue
		}

		// Copy so we don't mutate the plugin's discover response.
		clone := make(map[string]any, len(sub))
		for k, v := range sub {
			clone[k] = v
		}
		// Strip fields that aren't standard JSON Schema keywords but are
		// used by the legacy convention.
		delete(clone, "default") // compiled schema doesn't need defaults
		reqFlag, _ := clone["required"].(bool)
		delete(clone, "required") // legacy per-field flag

		// Legacy shorthand: some plugin schemas write
		//   "items": "string"
		// where canonical JSON Schema requires
		//   "items": {"type": "string"}
		// Rewrite transparently so existing plugins keep working.
		if items, ok := clone["items"].(string); ok {
			clone["items"] = map[string]any{"type": items}
		}
		properties[field] = clone

		if reqFlag {
			required = append(required, field)
		}
	}
	sort.Strings(required)

	root := map[string]any{
		"$schema":              "http://json-schema.org/draft-07/schema#",
		"type":                 "object",
		"properties":           properties,
		"required":             required,
		"additionalProperties": true,
	}

	schemaJSON, err := json.Marshal(root)
	if err != nil {
		return nil, fmt.Errorf("marshalling compiled schema: %w", err)
	}

	compiler := jsonschema.NewCompiler()
	url := "mu://schema/" + targetName
	if err := compiler.AddResource(url, bytes.NewReader(schemaJSON)); err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}
	compiled, err := compiler.Compile(url)
	if err != nil {
		return nil, fmt.Errorf("compiling schema: %w", err)
	}
	return compiled, nil
}

// normalizeForValidation round-trips the config map through JSON so that
// all numeric values land as float64 (the validator expects the
// encoding/json number representation) and map keys are canonical.
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

func unknownWarnings(targetName string, cfg, schema map[string]any) {
	for key := range cfg {
		if _, ok := schema[key]; !ok {
			fmt.Fprintf(os.Stderr, "warning: target %q: unknown config field %q (not in plugin schema)\n", targetName, key)
		}
	}
}

// renderValidationError walks a *jsonschema.ValidationError tree and
// renders each leaf into the coordinator's error style. Enum violations
// pick up a "did you mean" suggestion when the value and an enum entry
// are similar strings.
func renderValidationError(targetName string, schema map[string]any, instance any, err error) error {
	vErr, ok := err.(*jsonschema.ValidationError)
	if !ok {
		return fmt.Errorf("target %q: %v", targetName, err)
	}

	var msgs []string
	collectLeaves(vErr, func(leaf *jsonschema.ValidationError) {
		msg := formatLeaf(targetName, schema, instance, leaf)
		if msg != "" {
			msgs = append(msgs, msg)
		}
	})
	if len(msgs) == 0 {
		// Fall back to the library's own summary if we couldn't pin anything.
		return fmt.Errorf("target %q: %s", targetName, vErr.Message)
	}
	return fmt.Errorf("%s", strings.Join(msgs, "; "))
}

func collectLeaves(e *jsonschema.ValidationError, visit func(*jsonschema.ValidationError)) {
	if len(e.Causes) == 0 {
		visit(e)
		return
	}
	for _, c := range e.Causes {
		collectLeaves(c, visit)
	}
}

func formatLeaf(targetName string, schema map[string]any, instance any, leaf *jsonschema.ValidationError) string {
	field := strings.TrimPrefix(leaf.InstanceLocation, "/")
	msg := leaf.Message

	// Preserve legacy wording for the most common failure modes so the
	// existing tests and user-facing error formatting stay stable.
	switch {
	case strings.Contains(msg, "missing properties"):
		// Extract the property name(s).
		return fmt.Sprintf("target %q: missing required config field %s", targetName, extractMissingProps(msg))
	case strings.HasPrefix(msg, "expected "):
		// e.g. `expected boolean, but got string`
		// Desired legacy wording: `target "X": config field "goos" must be a boolean, got string`
		if field == "" {
			return fmt.Sprintf("target %q: %s", targetName, msg)
		}
		return fmt.Sprintf("target %q: config field %q must be %s", targetName, field, rewriteExpected(msg))
	case strings.Contains(msg, "must be one of") || strings.Contains(msg, "enum failed"):
		fieldSchema, _ := schema[field].(map[string]any)
		enum, _ := fieldSchema["enum"].([]any)
		// The library renders "value must be one of ..." without the
		// offending value; read it back from the leaf's InstancePtr via
		// the original cfg (not available here) or fall back to a shorter
		// form. For the "did you mean" hint we need the actual value, so
		// pull it from leaf.InstanceLocation + a re-walk of the instance.
		value := instanceValueString(instance, leaf.InstanceLocation)
		base := fmt.Sprintf("target %q: config field %q has value %s, must be one of %v", targetName, field, value, enum)
		if hint := suggestEnum(value, enum); hint != "" {
			base += " — did you mean " + hint + "?"
		}
		return base
	}
	if field != "" {
		return fmt.Sprintf("target %q: %s: %s", targetName, field, msg)
	}
	return fmt.Sprintf("target %q: %s", targetName, msg)
}

func rewriteExpected(msg string) string {
	// Convert "expected X, but got Y" → `a X, got Y` — closest to legacy.
	msg = strings.TrimPrefix(msg, "expected ")
	parts := strings.SplitN(msg, ", but got ", 2)
	if len(parts) != 2 {
		return msg
	}
	want, got := parts[0], parts[1]
	article := "a"
	if strings.HasPrefix(want, "a") || strings.HasPrefix(want, "e") || strings.HasPrefix(want, "i") || strings.HasPrefix(want, "o") || strings.HasPrefix(want, "u") {
		article = "an"
	}
	return fmt.Sprintf("%s %s, got %s", article, want, got)
}

func extractMissingProps(msg string) string {
	// Library format: `missing properties: 'foo', 'bar'`
	idx := strings.Index(msg, ":")
	if idx < 0 {
		return msg
	}
	tail := strings.TrimSpace(msg[idx+1:])
	tail = strings.ReplaceAll(tail, "'", `"`)
	return tail
}

// instanceValueString resolves a JSON pointer against the instance and
// returns its string representation (quoted for strings).
func instanceValueString(instance any, pointer string) string {
	if pointer == "" {
		return "<unknown>"
	}
	cur := instance
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	for _, p := range parts {
		m, ok := cur.(map[string]any)
		if !ok {
			return "<unknown>"
		}
		cur, ok = m[p]
		if !ok {
			return "<unknown>"
		}
	}
	if s, ok := cur.(string); ok {
		return `"` + s + `"`
	}
	return fmt.Sprintf("%v", cur)
}

// suggestEnum returns the closest enum value by Levenshtein distance
// within 2 edits (or ⅓ of the shorter string), or "" if nothing qualifies.
func suggestEnum(value string, enum []any) string {
	value = strings.Trim(value, `"`)
	if value == "" {
		return ""
	}
	var best string
	bestDist := math.MaxInt
	for _, e := range enum {
		candidate, ok := e.(string)
		if !ok {
			continue
		}
		d := levenshtein(value, candidate)
		limit := 2
		if n := len(candidate) / 3; n > limit {
			limit = n
		}
		if d <= limit && d < bestDist {
			best = candidate
			bestDist = d
		}
	}
	if best == "" {
		return ""
	}
	return `"` + best + `"`
}

func levenshtein(a, b string) int {
	ar := []rune(a)
	br := []rune(b)
	rows := len(ar) + 1
	cols := len(br) + 1
	dp := make([][]int, rows)
	for i := range dp {
		dp[i] = make([]int, cols)
		dp[i][0] = i
	}
	for j := 0; j < cols; j++ {
		dp[0][j] = j
	}
	for i := 1; i < rows; i++ {
		for j := 1; j < cols; j++ {
			cost := 1
			if ar[i-1] == br[j-1] {
				cost = 0
			}
			dp[i][j] = min3(dp[i-1][j]+1, dp[i][j-1]+1, dp[i-1][j-1]+cost)
		}
	}
	return dp[rows-1][cols-1]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}
