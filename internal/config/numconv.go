// Package config — numconv.go
//
// Numeric coercion helpers for values pulled from free-form config blocks
// (Target.Config, ToolchainConfig, PluginManifest fields, and any other
// map[string]any originating from a decoded mu.json).
//
// Why this exists
// ---------------
// When mu.json is decoded by encoding/json, JSON numbers land as float64
// regardless of whether they were written as `3` or `3.14`. Downstream
// consumers that performed `v.(float64)` worked fine under that assumption.
//
// However, mu is growing a CUE front-end (see MCM-11 dogfood migration). The
// CUE → Go decoder preserves integer/float distinction and emits integer
// literals as int64, not float64. That means any consumer performing
// `v.(float64)` on a config-originating value will panic the moment a target
// is authored in CUE instead of JSON.
//
// ToFloat64 / ToInt64 accept both representations (plus json.Number, which
// appears if a caller uses json.Decoder.UseNumber()), and reject everything
// else. Callers that previously wrote `v.(float64)` should switch to
// `ToFloat64(v)`.
//
// Audit report — call sites migrated
// ----------------------------------
// A grep across cmd/ and internal/ for `.(float64)`, `.(int64)`, and
// `.(int)` was performed on 2026-04-23. Only three float64 assertions were
// found, none of them operating on config-originating values:
//
//   - cmd/mu/verify_test.go:158       — reads JSON stdout of `mu verify
//                                        --json`; always goes through
//                                        encoding/json, never CUE. Left as-is.
//   - internal/plugin/scenario/match.go:139,145
//                                      — matches plugin RPC responses
//                                        transported as JSON NDJSON. Plugin
//                                        wire format is JSON by contract, not
//                                        CUE. Left as-is.
//
// No `.(int64)` or `.(int)` assertions on config-derived values were found.
// ToFloat64 / ToInt64 are therefore added preemptively for MCM-11, which
// will introduce the first CUE-decoded Target.Config blocks and expose the
// drift the moment any consumer starts reading a numeric field out of
// Target.Config. Future consumers MUST use these helpers rather than
// re-introducing direct type assertions.

package config

import (
	"encoding/json"
	"fmt"
)

// ToFloat64 coerces a value decoded from a free-form config block into a
// float64. It accepts float64 (encoding/json), int64 / int (CUE decoder,
// hand-built maps), and json.Number (when a caller opts into
// Decoder.UseNumber()). Any other type — including string — returns an
// error. A nil input is rejected so callers can distinguish "absent" from
// "present but wrong type" themselves.
func ToFloat64(v any) (float64, error) {
	switch n := v.(type) {
	case float64:
		return n, nil
	case float32:
		return float64(n), nil
	case int64:
		return float64(n), nil
	case int:
		return float64(n), nil
	case int32:
		return float64(n), nil
	case json.Number:
		f, err := n.Float64()
		if err != nil {
			return 0, fmt.Errorf("config: json.Number %q not a float: %w", n.String(), err)
		}
		return f, nil
	case nil:
		return 0, fmt.Errorf("config: expected number, got nil")
	default:
		return 0, fmt.Errorf("config: expected number, got %T", v)
	}
}

// ToInt64 coerces a value decoded from a free-form config block into an
// int64. It accepts int64 / int (CUE), float64 (encoding/json) so long as
// the value is integral and in range, and json.Number. Non-integral floats
// and out-of-range values return an error; so do strings and bools.
func ToInt64(v any) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case int32:
		return int64(n), nil
	case float64:
		if n != float64(int64(n)) {
			return 0, fmt.Errorf("config: %v is not an integer", n)
		}
		return int64(n), nil
	case float32:
		f := float64(n)
		if f != float64(int64(f)) {
			return 0, fmt.Errorf("config: %v is not an integer", n)
		}
		return int64(f), nil
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0, fmt.Errorf("config: json.Number %q not an int: %w", n.String(), err)
		}
		return i, nil
	case nil:
		return 0, fmt.Errorf("config: expected integer, got nil")
	default:
		return 0, fmt.Errorf("config: expected integer, got %T", v)
	}
}
