package scenario

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Match compares an actual response against the scenario's Expect
// block. Returns nil on match. Error messages are suitable for
// display to the plugin author.
func (s *Scenario) Match(actual map[string]any) error {
	// Round-trip actual through JSON so numeric types normalize.
	norm, err := normalize(actual)
	if err != nil {
		return err
	}

	switch s.Expect.Match {
	case "exact":
		m, ok := norm.(map[string]any)
		if !ok {
			return fmt.Errorf("exact match requires object response, got %T", norm)
		}
		return matchExact(s.Expect.Exact, m)
	case "shape":
		return matchShape("", s.Expect.Shape, norm)
	case "golden":
		return fmt.Errorf("scenario %q: golden match must be dispatched by the runner (it handles file I/O and --update)", s.Name)
	default:
		return fmt.Errorf("scenario %q: unknown match mode %q", s.Name, s.Expect.Match)
	}
}

// MatchGolden compares the actual response against the contents of a
// golden file. The runner, not the scenario itself, loads the file so
// it can support --update.
func (s *Scenario) MatchGolden(actual map[string]any, goldenBytes []byte) error {
	var expected map[string]any
	if err := json.Unmarshal(goldenBytes, &expected); err != nil {
		return fmt.Errorf("scenario %q: parsing golden file: %w", s.Name, err)
	}
	norm, err := normalize(actual)
	if err != nil {
		return err
	}
	return matchExact(expected, norm.(map[string]any))
}

func matchExact(expected, actual map[string]any) error {
	if !deepEqualJSON(expected, actual) {
		expBuf, _ := json.MarshalIndent(expected, "", "  ")
		actBuf, _ := json.MarshalIndent(actual, "", "  ")
		return fmt.Errorf("exact mismatch:\n  expected: %s\n  actual:   %s", expBuf, actBuf)
	}
	return nil
}

// matchShape checks that actual satisfies the expected shape template.
// Sentinels: "<string>", "<string,non-empty>", "<int>", "<bool>",
// "<any>", "<array>", "<object>", "<regex:PATTERN>".
// Literal values (non-sentinel strings, numbers, bools) must compare
// equal. Nested maps are recursively matched; keys present in actual but
// not in expected are allowed (so scenarios can assert a subset).
func matchShape(path string, expected, actual any) error {
	switch exp := expected.(type) {
	case string:
		if strings.HasPrefix(exp, "<") && strings.HasSuffix(exp, ">") {
			return matchSentinel(path, exp, actual)
		}
		got, ok := actual.(string)
		if !ok || got != exp {
			return fmt.Errorf("%s: expected %q, got %v", pathOrRoot(path), exp, actual)
		}
		return nil
	case map[string]any:
		got, ok := actual.(map[string]any)
		if !ok {
			return fmt.Errorf("%s: expected object, got %T", pathOrRoot(path), actual)
		}
		for k, v := range exp {
			sub, exists := got[k]
			if !exists {
				return fmt.Errorf("%s/%s: missing", pathOrRoot(path), k)
			}
			if err := matchShape(path+"/"+k, v, sub); err != nil {
				return err
			}
		}
		return nil
	case []any:
		got, ok := actual.([]any)
		if !ok {
			return fmt.Errorf("%s: expected array, got %T", pathOrRoot(path), actual)
		}
		if len(exp) != len(got) {
			return fmt.Errorf("%s: expected %d elements, got %d", pathOrRoot(path), len(exp), len(got))
		}
		for i := range exp {
			if err := matchShape(fmt.Sprintf("%s[%d]", path, i), exp[i], got[i]); err != nil {
				return err
			}
		}
		return nil
	default:
		// Numbers, booleans, nil.
		if !deepEqualJSON(expected, actual) {
			return fmt.Errorf("%s: expected %v, got %v", pathOrRoot(path), expected, actual)
		}
		return nil
	}
}

func matchSentinel(path, sentinel string, actual any) error {
	inner := strings.TrimPrefix(strings.TrimSuffix(sentinel, ">"), "<")
	parts := strings.Split(inner, ",")
	kind := strings.TrimSpace(parts[0])
	var modifiers []string
	for _, p := range parts[1:] {
		modifiers = append(modifiers, strings.TrimSpace(p))
	}

	switch kind {
	case "any":
		return nil
	case "string":
		s, ok := actual.(string)
		if !ok {
			return fmt.Errorf("%s: expected string, got %T", pathOrRoot(path), actual)
		}
		for _, m := range modifiers {
			if m == "non-empty" && s == "" {
				return fmt.Errorf("%s: expected non-empty string", pathOrRoot(path))
			}
		}
		return nil
	case "int":
		f, ok := actual.(float64)
		if !ok || f != float64(int64(f)) {
			return fmt.Errorf("%s: expected integer, got %v", pathOrRoot(path), actual)
		}
		return nil
	case "number":
		if _, ok := actual.(float64); !ok {
			return fmt.Errorf("%s: expected number, got %T", pathOrRoot(path), actual)
		}
		return nil
	case "bool":
		if _, ok := actual.(bool); !ok {
			return fmt.Errorf("%s: expected bool, got %T", pathOrRoot(path), actual)
		}
		return nil
	case "array":
		if _, ok := actual.([]any); !ok {
			return fmt.Errorf("%s: expected array, got %T", pathOrRoot(path), actual)
		}
		return nil
	case "object":
		if _, ok := actual.(map[string]any); !ok {
			return fmt.Errorf("%s: expected object, got %T", pathOrRoot(path), actual)
		}
		return nil
	case "regex":
		if len(modifiers) == 0 {
			return fmt.Errorf("%s: <regex,...> requires a pattern", pathOrRoot(path))
		}
		s, ok := actual.(string)
		if !ok {
			return fmt.Errorf("%s: regex sentinel expects string, got %T", pathOrRoot(path), actual)
		}
		re, err := regexp.Compile(strings.Join(modifiers, ","))
		if err != nil {
			return fmt.Errorf("%s: bad regex: %w", pathOrRoot(path), err)
		}
		if !re.MatchString(s) {
			return fmt.Errorf("%s: %q does not match /%s/", pathOrRoot(path), s, re.String())
		}
		return nil
	default:
		return fmt.Errorf("%s: unknown sentinel %q", pathOrRoot(path), sentinel)
	}
}

func pathOrRoot(p string) string {
	if p == "" {
		return "<root>"
	}
	return p
}

func deepEqualJSON(a, b any) bool {
	ab, _ := json.Marshal(a)
	bb, _ := json.Marshal(b)
	return string(ab) == string(bb)
}

func normalize(v any) (any, error) {
	buf, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var out any
	if err := json.Unmarshal(buf, &out); err != nil {
		return nil, err
	}
	return out, nil
}
