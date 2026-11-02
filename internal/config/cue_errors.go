package config

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"cuelang.org/go/cue"
	cueerrors "cuelang.org/go/cue/errors"
	"cuelang.org/go/cue/token"
)

// formatCueError wraps a CUE error tree into a friendly multi-line message.
//
// Each diagnostic produced by cue/errors.Errors is rendered as one line:
//
//	<where>: <hint> at <file>:<line>:<col>
//
// where <where> names the offending target (e.g. "target //foo/bar") when
// the CUE path goes through a targets[n] element and the target name can
// be resolved from root, or a dotted path ("toolchains[0].from") otherwise.
// <hint> rewrites common CUE diagnostic shapes — type mismatch, missing
// required field, regex violation, closed-struct violation — into plain
// English. Unknown shapes fall through to the raw Msg() format so nothing
// is silently lost.
//
// ctx is an optional leading phrase, such as "unifying mu.cue with
// #ProjectConfig". root is the user's decoded cue.Value, from which target
// names and fallback positions are looked up; unified is root unified with
// the schema definition, consulted for the fields a closed struct allows so
// an unknown field gets a did-you-mean. Either may be the zero cue.Value,
// which simply skips that lookup.
func formatCueError(ctx string, root, unified cue.Value, err error) error {
	if err == nil {
		return nil
	}
	es := cueerrors.Errors(err)
	if len(es) == 0 {
		if ctx != "" {
			return fmt.Errorf("%s: %w", ctx, err)
		}
		return err
	}
	lines := make([]string, 0, len(es))
	seen := make(map[string]bool, len(es))
	for _, e := range es {
		l := formatCueDiagnostic(root, unified, e)
		if seen[l] {
			continue
		}
		seen[l] = true
		lines = append(lines, l)
	}
	sort.Strings(lines)
	body := strings.Join(lines, "\n  ")
	if ctx != "" {
		return fmt.Errorf("%s:\n  %s", ctx, body)
	}
	return fmt.Errorf("%s", body)
}

// formatCueDiagnostic renders a single CUE error as one line.
func formatCueDiagnostic(root, unified cue.Value, e cueerrors.Error) string {
	path := stripDefPrefix(e.Path())
	where := describeWhere(root, path)
	hint := rewriteHint(root, path, e)
	if format, _ := e.Msg(); format == "field not allowed" {
		if s := suggestField(unified, path); s != "" {
			hint += fmt.Sprintf("; did you mean %q?", s)
		}
	}
	p := bestPos(e)
	if !p.IsValid() || isSchemaFile(p.Filename()) {
		// A missing required field has no position in the user's file;
		// point at the nearest enclosing value the user did write.
		if up := ancestorPos(root, path); up.IsValid() {
			p = up
		}
	}
	pos := describePos(p)
	var b strings.Builder
	if where != "" {
		b.WriteString(where)
		b.WriteString(": ")
	}
	b.WriteString(hint)
	if pos != "" {
		b.WriteString(" at ")
		b.WriteString(pos)
	}
	return b.String()
}

// describeWhere turns a CUE path such as ["targets","0","sources"] into a
// user-facing prefix. When the second-to-last segment (or earlier) is a
// numeric index under "targets" and root is non-empty, the target's
// declared name is substituted so the message reads "target //foo/bar:
// field sources ...". Otherwise the path is rendered with bracketed
// indices, trimming any trailing "_|_" bottom markers CUE occasionally
// appends.
func describeWhere(root cue.Value, path []string) string {
	if len(path) == 0 {
		return ""
	}
	// Try target-name lookup: path[0] == "targets", path[1] is an index.
	// Skip substitution when the offending field IS the target name
	// itself — the message would otherwise embed the invalid name as if
	// it were a valid identifier.
	if len(path) >= 2 && path[0] == "targets" && isNumeric(path[1]) {
		isTargetName := len(path) == 3 && path[2] == "target"
		if !isTargetName {
			if name := lookupTargetName(root, path[1]); name != "" {
				field := strings.Join(path[2:], ".")
				if field == "" {
					return fmt.Sprintf("target %s", name)
				}
				return fmt.Sprintf("target %s: field %s", name, field)
			}
		}
	}
	// Fallback: dotted path with bracketed indices.
	var b strings.Builder
	for i, seg := range path {
		if isNumeric(seg) {
			b.WriteByte('[')
			b.WriteString(seg)
			b.WriteByte(']')
			continue
		}
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(seg)
	}
	return b.String()
}

// lookupTargetName looks up targets[idx].target on root. Returns "" on any
// failure (root invalid, index out of range, value non-concrete).
func lookupTargetName(root cue.Value, idx string) string {
	if !root.Exists() {
		return ""
	}
	n, err := strconv.Atoi(idx)
	if err != nil || n < 0 {
		return ""
	}
	targets := root.LookupPath(cue.ParsePath("targets"))
	if !targets.Exists() {
		return ""
	}
	it, err := targets.List()
	if err != nil {
		return ""
	}
	i := 0
	for it.Next() {
		if i == n {
			nv := it.Value().LookupPath(cue.ParsePath("target"))
			s, err := nv.String()
			if err != nil {
				return ""
			}
			return s
		}
		i++
	}
	return ""
}

// rewriteHint inspects the CUE diagnostic format string and returns a
// plain-English hint. Unknown shapes return the rendered original message.
// root and path locate the user's value, which a type mismatch needs to tell
// the expected type from the given one.
func rewriteHint(root cue.Value, path []string, e cueerrors.Error) string {
	format, args := e.Msg()
	switch {
	case format == "field not allowed":
		return "unknown field"
	case format == "field is required but not present":
		return "missing required field"
	case strings.HasPrefix(format, "conflicting values ") &&
		strings.Contains(format, "(mismatched types"):
		// "conflicting values %s and %s (mismatched types %s and %s)"
		if len(args) >= 4 {
			return mismatchHint(root, path, args)
		}
	case strings.HasPrefix(format, "conflicting values "):
		// "conflicting values %s and %s"
		if len(args) >= 2 {
			return fmt.Sprintf("conflicting values %s and %s",
				fmtArg(args[0]), fmtArg(args[1]))
		}
	case strings.HasPrefix(format, "invalid value ") &&
		strings.Contains(format, "(does not satisfy"):
		// "invalid value %s (does not satisfy %s)" — used for regex,
		// string/int bounds, and other unary constraints.
		if len(args) >= 2 {
			constraint := fmtArg(args[1])
			value := fmtArg(args[0])
			if strings.Contains(constraint, "=~") {
				return fmt.Sprintf("value %s does not match pattern %s",
					value, extractRegex(constraint))
			}
			return fmt.Sprintf("value %s does not satisfy %s",
				value, constraint)
		}
	case strings.HasPrefix(format, "invalid value ") &&
		strings.Contains(format, "(out of bound"):
		// Regex constraints (=~"pat") report as "out of bound" in v0.14.
		if len(args) >= 2 {
			constraint := fmtArg(args[1])
			value := fmtArg(args[0])
			if strings.HasPrefix(constraint, "=~") {
				return fmt.Sprintf("value %s does not match pattern %s",
					value, extractRegex(constraint))
			}
			return fmt.Sprintf("value %s out of bound %s",
				value, constraint)
		}
	case strings.HasPrefix(format, "incomplete value "):
		// Fired when a field has a non-concrete constraint like `!=""`
		// and no value was supplied — effectively "missing required".
		return "missing required field"
	case strings.HasPrefix(format, "explicit error (_|_ literal)"):
		// Disjunction variant that forbids this field combination by
		// unifying with _|_. Treat as closed-struct violation.
		return "field combination rejected by schema disjunction"
	case strings.Contains(format, "errors in empty disjunction"):
		// "N errors in empty disjunction" — aggregate marker. Keep the
		// per-variant explicit errors below it; this top line adds no
		// information a user can act on, so flatten it.
		return "no disjunction variant accepts this value"
	}
	// Fallback: render raw format with its args.
	return fmt.Sprintf(format, args...)
}

// mismatchHint renders a "mismatched types" diagnostic. CUE names the two
// conflicting values and their types in no fixed order, so the side whose
// type matches the value the user wrote at path is the one they gave; the
// other is what the schema expects. When neither side matches, both types
// are reported without assigning blame.
func mismatchHint(root cue.Value, path []string, args []interface{}) string {
	types := [2]string{fmtArg(args[2]), fmtArg(args[3])}
	given := -1
	if root.Exists() && len(path) > 0 {
		if v := root.LookupPath(selectorPath(path)); v.Exists() {
			kind := v.IncompleteKind().String()
			for i, t := range types {
				if t == kind {
					given = i
					break
				}
			}
		}
	}
	if given < 0 {
		return fmt.Sprintf("mismatched types %s and %s", types[0], types[1])
	}
	return fmt.Sprintf("expected %s, got %s (value %s)",
		types[1-given], types[given], fmtArg(args[given]))
}

// suggestField returns the field allowed at path's parent in unified whose
// name is closest to path's last segment, or "" when none is close enough
// to be a likely typo.
func suggestField(unified cue.Value, path []string) string {
	if !unified.Exists() || len(path) == 0 {
		return ""
	}
	bad := path[len(path)-1]
	parent := unified.LookupPath(selectorPath(path[:len(path)-1]))
	if !parent.Exists() {
		return ""
	}
	it, err := parent.Fields(cue.Optional(true))
	if err != nil {
		return ""
	}
	maxDist := 2
	if len(bad) <= 4 {
		maxDist = 1
	}
	best, bestDist := "", maxDist+1
	for it.Next() {
		name := it.Selector().Unquoted()
		if name == bad || !parent.Allows(cue.Str(name)) {
			continue
		}
		if d := editDistance(bad, name); d < bestDist || (d == bestDist && name < best) {
			best, bestDist = name, d
		}
	}
	return best
}

// ancestorPos returns the position of the nearest value along path (the
// path itself first, then each parent) that exists in root and has a
// position, or token.NoPos.
func ancestorPos(root cue.Value, path []string) token.Pos {
	if !root.Exists() {
		return token.NoPos
	}
	for n := len(path); n > 0; n-- {
		if v := root.LookupPath(selectorPath(path[:n])); v.Exists() {
			if p := v.Pos(); p.IsValid() {
				return p
			}
		}
	}
	return token.NoPos
}

// selectorPath turns error-path segments into a cue.Path, treating numeric
// segments as list indices.
func selectorPath(segs []string) cue.Path {
	sels := make([]cue.Selector, 0, len(segs))
	for _, s := range segs {
		if isNumeric(s) {
			n, _ := strconv.Atoi(s)
			sels = append(sels, cue.Index(n))
			continue
		}
		sels = append(sels, cue.Str(s))
	}
	return cue.MakePath(sels...)
}

// editDistance is the optimal-string-alignment distance between a and b:
// insertions, deletions, substitutions, and adjacent transpositions each
// cost 1.
func editDistance(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	d := make([][]int, len(ra)+1)
	for i := range d {
		d[i] = make([]int, len(rb)+1)
		d[i][0] = i
	}
	for j := range d[0] {
		d[0][j] = j
	}
	for i := 1; i <= len(ra); i++ {
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			d[i][j] = min(d[i-1][j]+1, d[i][j-1]+1, d[i-1][j-1]+cost)
			if i > 1 && j > 1 && ra[i-1] == rb[j-2] && ra[i-2] == rb[j-1] {
				d[i][j] = min(d[i][j], d[i-2][j-2]+1)
			}
		}
	}
	return d[len(ra)][len(rb)]
}

// fmtArg formats a single CUE diagnostic argument. CUE hands us types
// ranging from primitive go values to adt.Value implementations; the
// latter already stringify sensibly via fmt.
func fmtArg(a interface{}) string {
	return fmt.Sprintf("%v", a)
}

// extractRegex extracts the quoted regex body from a CUE "=~\"pat\""
// constraint rendering, falling back to the raw string if no match.
func extractRegex(c string) string {
	c = strings.TrimPrefix(c, "=~")
	c = strings.TrimSpace(c)
	return c
}

// describePos renders a token.Pos as "basename:line:col", or "" for an
// invalid position.
func describePos(p token.Pos) string {
	if !p.IsValid() {
		return ""
	}
	name := p.Filename()
	if name != "" {
		name = filepath.Base(name)
	}
	return fmt.Sprintf("%s:%d:%d", name, p.Line(), p.Column())
}

// stripDefPrefix drops a leading "#Foo" segment from a CUE error path.
// CUE prepends the definition name when reporting errors from a Unify
// against a definition value; users think in terms of their own mu.cue
// fields, not our schema's top-level reference.
func stripDefPrefix(path []string) []string {
	if len(path) > 0 && strings.HasPrefix(path[0], "#") {
		return path[1:]
	}
	return path
}

// bestPos returns the most helpful position for a diagnostic. CUE's
// Position() is often NoPos for errors reported against a definition
// constraint; in that case we fall back to the first InputPosition that
// points into a user file (i.e. not schema.cue itself). If we only have
// schema.cue positions, we still return one so the reader has somewhere
// to look.
func bestPos(e cueerrors.Error) token.Pos {
	primary := e.Position()
	if primary.IsValid() && !isSchemaFile(primary.Filename()) {
		return primary
	}
	for _, p := range e.InputPositions() {
		if p.IsValid() && !isSchemaFile(p.Filename()) {
			return p
		}
	}
	if primary.IsValid() {
		return primary
	}
	for _, p := range e.InputPositions() {
		if p.IsValid() {
			return p
		}
	}
	return token.NoPos
}

func isSchemaFile(name string) bool {
	return filepath.Base(name) == "schema.cue"
}

func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
