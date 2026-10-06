package dag

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenWorkDir confines resolution through os.Root and returns the directory
// itself, not a checked pathname. The descriptor can be passed to the child.
func OpenWorkDir(projectRoot, workDir string) (*os.File, error) {
	projectRoot, err := filepath.Abs(projectRoot)
	if err != nil {
		return nil, err
	}
	if workDir == "" {
		workDir = projectRoot
	}
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(projectRoot, workDir)
	if err != nil || !filepath.IsLocal(rel) {
		return nil, fmt.Errorf("work_dir %q escapes project root", workDir)
	}
	root, err := os.OpenRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		f.Close()
		if err == nil {
			err = fmt.Errorf("work_dir %q is not a directory", workDir)
		}
		return nil, err
	}
	return f, nil
}
