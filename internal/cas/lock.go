package cas

import (
	"fmt"
	"os"
	"path/filepath"
)

// LockFileName is the advisory lock file at the root of a local cache
// directory. Commands that use the cache (builds, scratch, push, plugin
// install) hold it shared for as long as their store is open; `mu cache
// clean` holds it exclusively, so garbage collection never removes a blob a
// running build has written but not yet referenced from an action result.
const LockFileName = ".lock"

// CacheLock is a held advisory lock on a cache directory. The zero value and
// a nil *CacheLock are valid and release nothing.
type CacheLock struct {
	f *os.File
}

// LockShared takes the cache directory's lock in shared mode. If an
// exclusive holder (cache clean) is active, onWait is called once (when
// non-nil) and LockShared blocks until it finishes. On platforms without
// flock it is a no-op.
func LockShared(cacheDir string, onWait func()) (*CacheLock, error) {
	return lockCache(cacheDir, false, onWait)
}

// LockExclusive takes the cache directory's lock exclusively, waiting (after
// calling onWait once, when non-nil) for every shared holder to release it.
func LockExclusive(cacheDir string, onWait func()) (*CacheLock, error) {
	return lockCache(cacheDir, true, onWait)
}

func lockCache(cacheDir string, exclusive bool, onWait func()) (*CacheLock, error) {
	if !flockSupported {
		return &CacheLock{}, nil
	}
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return nil, fmt.Errorf("cache lock: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(cacheDir, LockFileName), os.O_RDWR|os.O_CREATE, 0o644)
	if err != nil {
		return nil, fmt.Errorf("cache lock: %w", err)
	}
	busy, err := flock(f, exclusive, false)
	if err == nil && busy {
		if onWait != nil {
			onWait()
		}
		_, err = flock(f, exclusive, true)
	}
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("cache lock %s: %w", f.Name(), err)
	}
	return &CacheLock{f: f}, nil
}

// Release drops the lock. It is safe to call more than once.
func (l *CacheLock) Release() {
	if l == nil || l.f == nil {
		return
	}
	l.f.Close() // closing the descriptor releases the flock
	l.f = nil
}
