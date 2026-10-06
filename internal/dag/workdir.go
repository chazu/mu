package dag

import (
	"fmt"
	"os"
	"path/filepath"
)

// OpenWorkDir confines resolution through os.Root and returns the directory
// itself, not a checked pathname. The descriptor can be passed to the child.
func OpenWorkDir(projectRoot, workDir string) (*os.File, error) {
	root, err := OpenWorkRoot(projectRoot, workDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	return root.Open(".")
}

// OpenWorkRoot pins the confined directory for output and source operations.
func OpenWorkRoot(projectRoot, workDir string) (*os.Root, error) {
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
	project, err := os.OpenRoot(projectRoot)
	if err != nil {
		return nil, err
	}
	defer project.Close()
	return project.OpenRoot(rel)
}
