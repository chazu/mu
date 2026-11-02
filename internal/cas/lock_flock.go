//go:build darwin || linux || freebsd || netbsd || openbsd || dragonfly

package cas

import (
	"errors"
	"os"
	"syscall"
)

const flockSupported = true

// flock applies a shared or exclusive flock to f. Without wait it reports
// busy instead of blocking when another holder conflicts.
func flock(f *os.File, exclusive, wait bool) (busy bool, err error) {
	how := syscall.LOCK_SH
	if exclusive {
		how = syscall.LOCK_EX
	}
	if !wait {
		how |= syscall.LOCK_NB
	}
	for {
		err = syscall.Flock(int(f.Fd()), how)
		if !errors.Is(err, syscall.EINTR) {
			break
		}
	}
	if !wait && errors.Is(err, syscall.EWOULDBLOCK) {
		return true, nil
	}
	return false, err
}
