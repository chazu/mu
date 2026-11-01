// Package archivelimit is mu's shared extraction budget. Every path that
// expands an archive (scratch toolchains, catalog source archives, plugin CAS
// bundles) counts entries and expanded bytes against one policy, so a small
// compressed input cannot expand into unbounded disk use or inode churn.
//
// Limits are enforced on the bytes actually written, not only on sizes the
// archive declares, and entries are counted whether they are extracted or
// skipped. Defaults are sized from real toolchains with generous headroom;
// operators can override them with environment variables when a legitimate
// archive is larger.
package archivelimit

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

// ErrExceeded marks an archive rejected for exceeding an extraction limit.
var ErrExceeded = errors.New("archive exceeds extraction limit")

// Environment overrides, applied to every profile when set.
const (
	EnvMaxEntries    = "MU_EXTRACT_MAX_ENTRIES"
	EnvMaxFileBytes  = "MU_EXTRACT_MAX_FILE_BYTES"
	EnvMaxTotalBytes = "MU_EXTRACT_MAX_TOTAL_BYTES"
)

// Limits bounds one extraction. Every field must be positive.
type Limits struct {
	MaxEntries    int   // archive headers, including directories and skipped entries
	MaxFileBytes  int64 // expanded bytes of any single file
	MaxTotalBytes int64 // expanded bytes across the whole archive
}

// Profiles. Reference sizes measured from installed toolchains (2026-11):
// Go 1.26 ~17k entries / 260 MB; Zig 0.15 ~19k / 230 MB; Node 24 ~6k /
// 200 MB with a 116 MB binary; JDK 25 ~700 / 370 MB; Homebrew LLVM 22 ~10k /
// 1.8 GB with a 156 MB library; Rust toolchains with docs up to ~76k /
// 2.3 GB with files near 200 MB. Linux LLVM release tarballs expand to
// several GB. Toolchain limits leave roughly 6x headroom over the largest of
// these; plugin bundles are scripts plus at most a few binaries.
var (
	Toolchain = Limits{MaxEntries: 500_000, MaxFileBytes: 4 << 30, MaxTotalBytes: 16 << 30}
	Plugin    = Limits{MaxEntries: 100_000, MaxFileBytes: 1 << 30, MaxTotalBytes: 2 << 30}
)

// Budget tracks consumption for a single archive. It is not safe for
// concurrent use.
type Budget struct {
	limits  Limits
	entries int
	total   int64
}

// New returns a budget for base with any environment overrides applied.
// A malformed override is an error rather than a silently ignored limit.
func New(base Limits) (*Budget, error) {
	l, err := withEnv(base, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	return NewWithLimits(l)
}

// NewWithLimits returns a budget for exactly l, ignoring the environment.
func NewWithLimits(l Limits) (*Budget, error) {
	if l.MaxEntries <= 0 || l.MaxFileBytes <= 0 || l.MaxTotalBytes <= 0 {
		return nil, fmt.Errorf("archivelimit: limits must be positive: %+v", l)
	}
	return &Budget{limits: l}, nil
}

// Limits reports the effective limits.
func (b *Budget) Limits() Limits { return b.limits }

// Entry counts one archive header. Call it for every header read, before
// deciding whether to extract or skip it.
func (b *Budget) Entry(name string) error {
	b.entries++
	if b.entries > b.limits.MaxEntries {
		return fmt.Errorf("%w: more than %d entries at %q (raise with %s)", ErrExceeded, b.limits.MaxEntries, name, EnvMaxEntries)
	}
	return nil
}

// Declared rejects a file early when its declared expanded size cannot fit.
// A negative size means unknown and is enforced only while copying.
func (b *Budget) Declared(name string, size int64) error {
	if size < 0 {
		return nil
	}
	if size > b.limits.MaxFileBytes {
		return fmt.Errorf("%w: %q declares %d bytes, over the %d-byte file limit (raise with %s)", ErrExceeded, name, size, b.limits.MaxFileBytes, EnvMaxFileBytes)
	}
	if size > b.limits.MaxTotalBytes-b.total {
		return fmt.Errorf("%w: %q would expand the archive past %d bytes (raise with %s)", ErrExceeded, name, b.limits.MaxTotalBytes, EnvMaxTotalBytes)
	}
	return nil
}

// Copy streams one file's content into dst, failing as soon as the actual
// bytes exceed the per-file limit or the archive's remaining total.
func (b *Budget) Copy(dst io.Writer, src io.Reader, name string) (int64, error) {
	allowed := b.limits.MaxFileBytes
	fileLimited := true
	if remaining := b.limits.MaxTotalBytes - b.total; remaining < allowed {
		allowed = remaining
		fileLimited = false
	}
	n, err := io.Copy(dst, io.LimitReader(src, allowed))
	b.total += n
	if err != nil {
		return n, err
	}
	// Probe for one byte beyond the allowance without writing it.
	var probe [1]byte
	if m, _ := io.ReadFull(src, probe[:]); m > 0 {
		if fileLimited {
			return n, fmt.Errorf("%w: %q expands past the %d-byte file limit (raise with %s)", ErrExceeded, name, b.limits.MaxFileBytes, EnvMaxFileBytes)
		}
		return n, fmt.Errorf("%w: %q expands the archive past %d bytes (raise with %s)", ErrExceeded, name, b.limits.MaxTotalBytes, EnvMaxTotalBytes)
	}
	return n, nil
}

func withEnv(base Limits, lookup func(string) (string, bool)) (Limits, error) {
	l := base
	parse := func(key string) (int64, bool, error) {
		raw, ok := lookup(key)
		if !ok || raw == "" {
			return 0, false, nil
		}
		v, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || v <= 0 {
			return 0, false, fmt.Errorf("archivelimit: %s must be a positive integer, got %q", key, raw)
		}
		return v, true, nil
	}
	if v, ok, err := parse(EnvMaxEntries); err != nil {
		return Limits{}, err
	} else if ok {
		if v > int64(^uint(0)>>1) {
			return Limits{}, fmt.Errorf("archivelimit: %s is too large", EnvMaxEntries)
		}
		l.MaxEntries = int(v)
	}
	if v, ok, err := parse(EnvMaxFileBytes); err != nil {
		return Limits{}, err
	} else if ok {
		l.MaxFileBytes = v
	}
	if v, ok, err := parse(EnvMaxTotalBytes); err != nil {
		return Limits{}, err
	} else if ok {
		l.MaxTotalBytes = v
	}
	return l, nil
}
