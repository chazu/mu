package coordinator

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/chau/mu/internal/config"
)

// ValidateTargetConfig checks a target's config map against the plugin's
// declared config_schema from DiscoverResponse. Returns nil if valid.
// Logs warnings for unknown fields. Returns error for type mismatches
// or missing required fields.
func ValidateTargetConfig(target config.Target, schema map[string]any) error {
	if len(schema) == 0 {
		return nil
	}

	cfg := target.Config
	if cfg == nil {
		cfg = map[string]any{}
	}

	var errs []string

	// Check for unknown fields (in config but not in schema).
	for key := range cfg {
		if _, ok := schema[key]; !ok {
			fmt.Fprintf(os.Stderr, "warning: target %q: unknown config field %q (not in plugin schema)\n", target.Name, key)
		}
	}

	// Check each schema field.
	for field, schemaDef := range schema {
		fieldSchema, ok := schemaDef.(map[string]any)
		if !ok {
			continue
		}

		val, present := cfg[field]

		// Check required: a field is required if the schema explicitly sets
		// "required": true, OR if the field has no "default" value (implicit
		// required). A field with "default" is always optional.
		if !present {
			_, hasDefault := fieldSchema["default"]
			explicitRequired, _ := fieldSchema["required"].(bool)
			if explicitRequired || !hasDefault {
				errs = append(errs, fmt.Sprintf("target %q: missing required config field %q", target.Name, field))
			}
			continue
		}

		// Type check.
		schemaType, _ := fieldSchema["type"].(string)
		if schemaType != "" {
			if err := checkType(target.Name, field, val, schemaType); err != nil {
				errs = append(errs, err.Error())
				continue // skip enum check if type is wrong
			}
		}

		// Enum check.
		if enumRaw, ok := fieldSchema["enum"]; ok {
			if enumList, ok := enumRaw.([]any); ok {
				if !enumContains(enumList, val) {
					errs = append(errs, fmt.Sprintf("target %q: config field %q has value %v, must be one of %v", target.Name, field, val, enumList))
				}
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return nil
}

// checkType validates that val matches the expected JSON schema type.
func checkType(targetName, field string, val any, schemaType string) error {
	switch schemaType {
	case "string":
		if _, ok := val.(string); !ok {
			return fmt.Errorf("target %q: config field %q must be a string, got %T", targetName, field, val)
		}
	case "boolean":
		if _, ok := val.(bool); !ok {
			return fmt.Errorf("target %q: config field %q must be a boolean, got %T", targetName, field, val)
		}
	case "integer":
		f, ok := val.(float64)
		if !ok {
			return fmt.Errorf("target %q: config field %q must be an integer, got %T", targetName, field, val)
		}
		if f != math.Trunc(f) {
			return fmt.Errorf("target %q: config field %q must be an integer, got float %v", targetName, field, f)
		}
	case "number":
		if _, ok := val.(float64); !ok {
			return fmt.Errorf("target %q: config field %q must be a number, got %T", targetName, field, val)
		}
	case "array":
		if _, ok := val.([]any); !ok {
			return fmt.Errorf("target %q: config field %q must be an array, got %T", targetName, field, val)
		}
	case "object":
		if _, ok := val.(map[string]any); !ok {
			return fmt.Errorf("target %q: config field %q must be an object, got %T", targetName, field, val)
		}
	}
	return nil
}

// enumContains checks whether val is in the enum list.
func enumContains(enum []any, val any) bool {
	for _, e := range enum {
		if e == val {
			return true
		}
	}
	return false
}
