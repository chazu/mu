package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/parser"
	"cuelang.org/go/cue/token"
)

// updateMuCuePlugin edits the root mu.cue to add or replace a plugin
// entry with the given digest. The edit operates on the CUE AST: if a
// `plugins` list field exists with an entry whose `name` matches, its
// `digest` field is set (replacing any prior value); otherwise a new
// entry is appended. If no `plugins` field exists yet, one is created.
func updateMuCuePlugin(projectRoot, pluginName, digest string) error {
	path := filepath.Join(projectRoot, "mu.cue")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading mu.cue: %w", err)
	}

	file, err := parser.ParseFile(path, data, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("parsing mu.cue: %w", err)
	}

	pluginsField := findTopField(file, "plugins")
	newEntry := pluginEntryStruct(pluginName, digest)

	if pluginsField == nil {
		// Append a fresh `plugins: [...]` declaration at file end.
		file.Decls = append(file.Decls, &ast.Field{
			Label: ast.NewIdent("plugins"),
			Value: &ast.ListLit{Elts: []ast.Expr{newEntry}},
		})
	} else {
		list, ok := pluginsField.Value.(*ast.ListLit)
		if !ok {
			return fmt.Errorf("mu.cue: top-level `plugins` is not a list literal")
		}
		replaced := false
		for i, elt := range list.Elts {
			s, ok := elt.(*ast.StructLit)
			if !ok {
				continue
			}
			if cueFieldStringValue(s, "name") != pluginName {
				continue
			}
			list.Elts[i] = mergeDigestField(s, digest)
			replaced = true
			break
		}
		if !replaced {
			list.Elts = append(list.Elts, newEntry)
		}
	}

	out, err := format.Node(file, format.Simplify())
	if err != nil {
		return fmt.Errorf("formatting mu.cue: %w", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return fmt.Errorf("writing mu.cue: %w", err)
	}
	return nil
}

// findTopField returns the first top-level *ast.Field with the given
// identifier label, or nil if none exists.
func findTopField(file *ast.File, name string) *ast.Field {
	for _, d := range file.Decls {
		f, ok := d.(*ast.Field)
		if !ok {
			continue
		}
		if id, ok := f.Label.(*ast.Ident); ok && id.Name == name {
			return f
		}
	}
	return nil
}

// cueFieldStringValue returns the string-literal value of the named field
// inside s, or "" if it is missing or not a basic string literal.
func cueFieldStringValue(s *ast.StructLit, name string) string {
	for _, e := range s.Elts {
		f, ok := e.(*ast.Field)
		if !ok {
			continue
		}
		var label string
		switch lbl := f.Label.(type) {
		case *ast.Ident:
			label = lbl.Name
		case *ast.BasicLit:
			if lbl.Kind == token.STRING {
				label = strings.Trim(lbl.Value, `"`)
			}
		}
		if label != name {
			continue
		}
		if lit, ok := f.Value.(*ast.BasicLit); ok && lit.Kind == token.STRING {
			return strings.Trim(lit.Value, `"`)
		}
	}
	return ""
}

// pluginEntryStruct builds an AST struct literal {name: <name>, digest: <digest>}.
func pluginEntryStruct(name, digest string) *ast.StructLit {
	return &ast.StructLit{
		Elts: []ast.Decl{
			&ast.Field{
				Label: ast.NewIdent("name"),
				Value: ast.NewString(name),
			},
			&ast.Field{
				Label: ast.NewIdent("digest"),
				Value: ast.NewString(digest),
			},
		},
	}
}

// mergeDigestField returns a copy of s with its `digest` field set to the
// given value. An existing `digest` field is replaced in place; otherwise
// a new one is appended. #PluginDef allows exactly one source form, so any
// command, script, url or sha256 field is dropped.
func mergeDigestField(s *ast.StructLit, digest string) *ast.StructLit {
	digestField := &ast.Field{
		Label: ast.NewIdent("digest"),
		Value: ast.NewString(digest),
	}
	elts := s.Elts[:0]
	placed := false
	for _, e := range s.Elts {
		if f, ok := e.(*ast.Field); ok {
			if id, ok := f.Label.(*ast.Ident); ok {
				switch id.Name {
				case "command", "script", "url", "sha256":
					continue
				case "digest":
					elts = append(elts, digestField)
					placed = true
					continue
				}
			}
		}
		elts = append(elts, e)
	}
	if !placed {
		elts = append(elts, digestField)
	}
	s.Elts = elts
	return s
}
