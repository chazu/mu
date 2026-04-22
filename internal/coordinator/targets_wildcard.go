package coordinator

import (
	"fmt"
	"path"
	"strings"
)

// expandTargetWildcards expands wildcard patterns in `patterns` against
// `available` target names. Supported forms:
//
//   - "//pkg/..." — matches every target name that starts with "//pkg/"
//     (recursive).
//   - Patterns containing "*" — matched with path.Match, which treats "*"
//     as "any run of non-slash characters within a segment". This lets
//     callers use multi-wildcard forms like "//plugins/*/*" or
//     "//plugins/*/build".
//   - Anything else — passed through literally.
//
// Results preserve input order and never contain duplicates.
//
// When errOnNoMatch is true, a pattern that matches nothing is a hard
// error; otherwise such patterns are silently dropped (this is useful for
// side-effect passes that consume an already-validated target list).
func expandTargetWildcards(patterns, available []string, errOnNoMatch bool) ([]string, error) {
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	for _, p := range patterns {
		matched := 0
		switch {
		case strings.HasSuffix(p, "/..."):
			prefix := strings.TrimSuffix(p, "...")
			for _, t := range available {
				if strings.HasPrefix(t, prefix) {
					add(t)
					matched++
				}
			}
		case strings.ContainsRune(p, '*'):
			for _, t := range available {
				ok, err := path.Match(p, t)
				if err != nil {
					return nil, fmt.Errorf("invalid target pattern %q: %w", p, err)
				}
				if ok {
					add(t)
					matched++
				}
			}
		default:
			add(p)
			matched = 1
		}
		if matched == 0 && errOnNoMatch {
			return nil, fmt.Errorf("pattern %q matched no targets", p)
		}
	}
	return out, nil
}
