package coordinator

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/chau/mu/internal/config"
	"github.com/chau/mu/internal/schemacache"
)

// VendoredSchema is one schema module shipped with a plugin: the
// declared (module, version) plus the .cue files that constitute it,
// keyed by their path relative to the schema directory root.
type VendoredSchema struct {
	Module  string
	Version string
	Files   []schemacache.File
}

// LoadVendoredSchemas reads a plugin's manifest from pluginDir and
// returns one VendoredSchema per declared SchemaDecl. .cue file
// contents are read from disk relative to pluginDir.
//
// Returns an empty slice (no error) if the manifest declares no
// schemas.
func LoadVendoredSchemas(pluginDir string) ([]VendoredSchema, error) {
	pcfg, err := config.LoadPluginManifest(pluginDir)
	if err != nil {
		return nil, err
	}
	if pcfg.Plugin == nil || len(pcfg.Plugin.Schemas) == 0 {
		return nil, nil
	}
	out := make([]VendoredSchema, 0, len(pcfg.Plugin.Schemas))
	for _, decl := range pcfg.Plugin.Schemas {
		files, err := readSchemaFiles(pluginDir, decl.Path)
		if err != nil {
			return nil, fmt.Errorf("schema %s@%s: %w", decl.Module, decl.Version, err)
		}
		out = append(out, VendoredSchema{
			Module:  decl.Module,
			Version: decl.Version,
			Files:   files,
		})
	}
	return out, nil
}

func readSchemaFiles(pluginDir, schemaPath string) ([]schemacache.File, error) {
	root := filepath.Join(pluginDir, schemaPath)
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", schemaPath, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", schemaPath)
	}
	var files []schemacache.File
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".cue") {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files = append(files, schemacache.File{
			RelPath: filepath.ToSlash(rel),
			Content: content,
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(files) == 0 {
		return nil, fmt.Errorf("%s contains no .cue files", schemaPath)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].RelPath < files[j].RelPath })
	return files, nil
}
