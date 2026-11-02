package cas

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ExpandPath expands a leading ~ and any ${VAR} references to an
// absolute filesystem path. Missing environment variables expand to
// empty string (os.ExpandEnv's default). An error is returned if the
// result is not a well-formed path.
func ExpandPath(p string) (string, error) {
	if p == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(p, "~") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("expanding ~: %w", err)
		}
		if p == "~" {
			p = home
		} else if strings.HasPrefix(p, "~/") {
			p = filepath.Join(home, p[2:])
		}
	}
	p = os.ExpandEnv(p)
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	return abs, nil
}
