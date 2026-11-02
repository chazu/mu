package plugincatalog

import (
	"context"

	"github.com/chazu/mu/internal/archive"
)

// ExtractArchive extracts a catalog source archive (.tar.gz) into destDir.
// Only regular files and directories are accepted; symlinks and hard links
// are rejected so an untrusted archive cannot write outside the destination
// or create an executable path with surprising resolution behavior.
func ExtractArchive(archivePath, destDir string) error {
	return archive.ExtractFile(context.Background(), archivePath, destDir, archive.TarGz, archive.Policy{Limits: archive.Plugin})
}
