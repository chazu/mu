// Package main: `mu migrate` converts legacy mu.json files to mu.cue.
//
// The migration pipeline is:
//
//  1. Read mu.json bytes.
//  2. Parse with cuelang.org/go/encoding/json.Extract → ast.Expr.
//  3. Wrap the resulting StructLit's fields as top-level declarations of
//     a new *ast.File whose preamble is `package mu`.
//  4. Emit bytes with cue/format.Node(file, format.Simplify()).
//  5. Parity-check: load the emitted CUE back via cue/load, marshal both
//     the original JSON and the round-tripped CUE as canonical JSON (via
//     cue.Value.MarshalJSON which sorts struct keys) and compare. If the
//     canonical forms disagree, refuse to write.
//
// Note on schema wrapping: the task spec calls for wrapping the emission
// with `#ProjectConfig &` / `#PluginConfig &`. Doing so in a standalone
// mu.cue file would leave those identifiers unresolved, because the
// mu-package schema lives in the embedded schema.cue (internal/config)
// and is unified with user files *in Go*, not via CUE import. Emitting
// bare data keeps the output round-trippable through the existing
// cueDecoder. The `package mu` header is still prepended so the file
// participates in the mu package when/if the schema ever ships via a
// real CUE module.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/ast"
	"cuelang.org/go/cue/cuecontext"
	"cuelang.org/go/cue/format"
	"cuelang.org/go/cue/token"
	cuejson "cuelang.org/go/encoding/json"
)

// runMigrate is the entry point for `mu migrate`.
func runMigrate(args []string) int {
	fs := flag.NewFlagSet("mu migrate", flag.ContinueOnError)
	var (
		inPath    string
		outPath   string
		dryRun    bool
		keepJSON  bool
		recursive bool
	)
	fs.StringVar(&inPath, "in", "./mu.json", "input mu.json path (ignored with --recursive)")
	fs.StringVar(&outPath, "out", "", "output mu.cue path (default: sibling of --in)")
	fs.BoolVar(&dryRun, "dry-run", false, "print diff instead of writing")
	fs.BoolVar(&keepJSON, "keep-json", false, "preserve mu.json after successful migration")
	fs.BoolVar(&recursive, "recursive", false, "walk project and migrate every mu.json")
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if recursive {
		root := "."
		if inPath != "./mu.json" && inPath != "" {
			// Allow --in to specify a root directory when combined with --recursive.
			if st, err := os.Stat(inPath); err == nil && st.IsDir() {
				root = inPath
			}
		}
		if err := migrateRecursive(root, dryRun, keepJSON, os.Stdout); err != nil {
			fmt.Fprintf(os.Stderr, "mu migrate: %v\n", err)
			return 1
		}
		return 0
	}

	if outPath == "" {
		outPath = defaultOutPath(inPath)
	}
	if err := migrateOne(inPath, outPath, dryRun, keepJSON, os.Stdout); err != nil {
		fmt.Fprintf(os.Stderr, "mu migrate: %v\n", err)
		return 1
	}
	return 0
}

// defaultOutPath returns the sibling mu.cue for a given mu.json.
func defaultOutPath(in string) string {
	dir := filepath.Dir(in)
	return filepath.Join(dir, "mu.cue")
}

// migrateRecursive walks root and migrates every mu.json it finds.
// Hidden directories, testdata, and symlinks are skipped to mirror the
// existing project loader behavior.
func migrateRecursive(root string, dryRun, keepJSON bool, out io.Writer) error {
	var firstErr error
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			name := d.Name()
			if name != "." && (strings.HasPrefix(name, ".") || name == "testdata" || name == "node_modules") {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "mu.json" {
			return nil
		}
		outPath := defaultOutPath(path)
		if err := migrateOne(path, outPath, dryRun, keepJSON, out); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", path, err)
			}
			fmt.Fprintf(os.Stderr, "mu migrate: %s: %v\n", path, err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return firstErr
}

// migrateOne performs the JSON→CUE conversion for a single file pair.
func migrateOne(inPath, outPath string, dryRun, keepJSON bool, out io.Writer) error {
	jsonBytes, err := os.ReadFile(inPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", inPath, err)
	}

	cueBytes, err := convertJSONToCUE(inPath, jsonBytes)
	if err != nil {
		return err
	}

	// Parity check: semantic round-trip.
	if err := verifyParity(inPath, jsonBytes, outPath, cueBytes); err != nil {
		return fmt.Errorf("parity check failed (refusing to write): %w", err)
	}

	if dryRun {
		diff := unifiedDiff(inPath, outPath, string(jsonBytes), string(cueBytes))
		if _, err := io.WriteString(out, diff); err != nil {
			return err
		}
		return nil
	}

	if err := os.WriteFile(outPath, cueBytes, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", outPath, err)
	}
	if !keepJSON {
		// Only remove the JSON file once the CUE sibling has been written
		// and parity verified. Ignore ErrNotExist for already-migrated runs.
		if err := os.Remove(inPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removing %s: %w", inPath, err)
		}
	}
	return nil
}

// convertJSONToCUE parses JSON, extracts an ast.Expr, and re-emits it as
// formatted CUE with a package header. The detect-plugin-vs-project
// probe drives only the header comment; no wrapping is applied to keep
// the output loadable by the existing cueDecoder.
func convertJSONToCUE(path string, data []byte) ([]byte, error) {
	expr, err := cuejson.Extract(path, data)
	if err != nil {
		return nil, fmt.Errorf("parsing JSON %s: %w", path, err)
	}
	sl, ok := expr.(*ast.StructLit)
	if !ok {
		return nil, fmt.Errorf("%s: top-level JSON value is not an object", path)
	}

	// Probe for the "plugin" key to pick an informative header comment.
	isPlugin := hasField(sl, "plugin")

	decls := []ast.Decl{
		&ast.Package{Name: ast.NewIdent("mu")},
	}
	header := "#ProjectConfig: generated by `mu migrate` from mu.json."
	if isPlugin {
		header = "#PluginConfig: generated by `mu migrate` from mu.json."
	}
	decls[0].(*ast.Package).AddComment(&ast.CommentGroup{
		Doc:  true,
		List: []*ast.Comment{{Text: "// " + header}},
	})

	for _, elt := range sl.Elts {
		d, ok := elt.(ast.Decl)
		if !ok {
			return nil, fmt.Errorf("%s: unexpected non-decl element %T in top-level object", path, elt)
		}
		decls = append(decls, d)
	}

	file := &ast.File{Decls: decls}
	out, err := format.Node(file, format.Simplify(), format.TabIndent(true))
	if err != nil {
		return nil, fmt.Errorf("formatting CUE: %w", err)
	}
	return out, nil
}

// hasField reports whether the given StructLit contains a field with the
// given label (string or identifier).
func hasField(sl *ast.StructLit, name string) bool {
	for _, e := range sl.Elts {
		f, ok := e.(*ast.Field)
		if !ok {
			continue
		}
		switch lbl := f.Label.(type) {
		case *ast.Ident:
			if lbl.Name == name {
				return true
			}
		case *ast.BasicLit:
			if lbl.Kind == token.STRING {
				// Strip surrounding quotes.
				s := lbl.Value
				if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') {
					s = s[1 : len(s)-1]
				}
				if s == name {
					return true
				}
			}
		}
	}
	return false
}

// verifyParity re-parses both the original JSON and the emitted CUE and
// compares their canonical-JSON representations (keys sorted). Any
// divergence is reported as an error with a diff.
func verifyParity(jsonPath string, jsonBytes []byte, cuePath string, cueBytes []byte) error {
	ctx := cuecontext.New()

	// Build cue.Value from JSON via the json decoder.
	jsonExpr, err := cuejson.Extract(jsonPath, jsonBytes)
	if err != nil {
		return fmt.Errorf("re-parsing JSON: %w", err)
	}
	jsonVal := ctx.BuildExpr(jsonExpr)
	if err := jsonVal.Err(); err != nil {
		return fmt.Errorf("building JSON cue.Value: %w", err)
	}

	// Build cue.Value from the emitted CUE bytes.
	cueVal := ctx.CompileBytes(cueBytes, cue.Filename(cuePath))
	if err := cueVal.Err(); err != nil {
		return fmt.Errorf("compiling emitted CUE: %w", err)
	}

	// Canonicalize both by marshaling back to JSON (sorts keys).
	jsonCanon, err := jsonVal.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling JSON-value: %w", err)
	}
	cueCanon, err := cueVal.MarshalJSON()
	if err != nil {
		return fmt.Errorf("marshaling CUE-value: %w", err)
	}

	// Parse both canonicals into generic maps and deep-equal. This tolerates
	// JSON formatting differences (whitespace) while still asserting semantic
	// parity.
	var a, b any
	if err := json.Unmarshal(jsonCanon, &a); err != nil {
		return err
	}
	if err := json.Unmarshal(cueCanon, &b); err != nil {
		return err
	}
	if !reflect.DeepEqual(a, b) {
		return fmt.Errorf("semantic drift between JSON and emitted CUE\n--- json\n%s\n--- cue\n%s\n", jsonCanon, cueCanon)
	}
	return nil
}

// unifiedDiff renders a minimal unified diff between two text blobs.
// mu migrate ships its own tiny implementation rather than pulling in a
// diff library; the diff is purely advisory (dry-run output).
func unifiedDiff(aName, bName, a, b string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "--- %s\n+++ %s\n", aName, bName)
	aLines := strings.Split(strings.TrimRight(a, "\n"), "\n")
	bLines := strings.Split(strings.TrimRight(b, "\n"), "\n")
	for _, l := range aLines {
		fmt.Fprintf(&sb, "-%s\n", l)
	}
	for _, l := range bLines {
		fmt.Fprintf(&sb, "+%s\n", l)
	}
	return sb.String()
}
