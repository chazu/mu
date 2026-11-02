package archive

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
)

// ErrExceeded marks an archive rejected for exceeding an extraction limit.
var ErrExceeded = errors.New("archive exceeds extraction limit")

// EnvMaxBytes, when set to a positive integer, replaces both the per-file and
// the total expanded-byte limit of every profile. Entry counts have no
// override; the defaults are far above any real archive.
const EnvMaxBytes = "MU_ARCHIVE_MAX_BYTES"

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

// NewBudget returns a budget for base with any MU_ARCHIVE_MAX_BYTES override
// applied. A malformed override is an error rather than an ignored limit.
func NewBudget(base Limits) (*Budget, error) {
	l, err := withEnv(base, os.LookupEnv)
	if err != nil {
		return nil, err
	}
	return NewBudgetWithLimits(l)
}

// NewBudgetWithLimits returns a budget for exactly l, ignoring the environment.
func NewBudgetWithLimits(l Limits) (*Budget, error) {
	if l.MaxEntries <= 0 || l.MaxFileBytes <= 0 || l.MaxTotalBytes <= 0 {
		return nil, fmt.Errorf("archive: limits must be positive: %+v", l)
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
		return fmt.Errorf("%w: more than %d entries at %q", ErrExceeded, b.limits.MaxEntries, name)
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
		return fmt.Errorf("%w: %q declares %d bytes, over the %d-byte file limit (raise with %s)", ErrExceeded, name, size, b.limits.MaxFileBytes, EnvMaxBytes)
	}
	if size > b.limits.MaxTotalBytes-b.total {
		return fmt.Errorf("%w: %q would expand the archive past %d bytes (raise with %s)", ErrExceeded, name, b.limits.MaxTotalBytes, EnvMaxBytes)
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
			return n, fmt.Errorf("%w: %q expands past the %d-byte file limit (raise with %s)", ErrExceeded, name, b.limits.MaxFileBytes, EnvMaxBytes)
		}
		return n, fmt.Errorf("%w: %q expands the archive past %d bytes (raise with %s)", ErrExceeded, name, b.limits.MaxTotalBytes, EnvMaxBytes)
	}
	return n, nil
}

func withEnv(base Limits, lookup func(string) (string, bool)) (Limits, error) {
	raw, ok := lookup(EnvMaxBytes)
	if !ok || raw == "" {
		return base, nil
	}
	v, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || v <= 0 {
		return Limits{}, fmt.Errorf("archive: %s must be a positive integer, got %q", EnvMaxBytes, raw)
	}
	l := base
	l.MaxFileBytes = v
	l.MaxTotalBytes = v
	return l, nil
}
