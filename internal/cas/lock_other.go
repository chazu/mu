//go:build !(darwin || linux || freebsd || netbsd || openbsd || dragonfly)

package cas

import "os"

// Advisory cache locking is a no-op where flock is unavailable.
const flockSupported = false

func flock(*os.File, bool, bool) (bool, error) { return false, nil }
