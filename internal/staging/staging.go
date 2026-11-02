// Package staging reclaims temporary files and directories that mu
// processes leave behind when they exit abruptly: extraction staging,
// download and plugin temp files, OCI ingest files, cache replay spools, and
// sealed-value scratch directories (which can hold secret material).
//
// Ownership policy. A staging path is owned by the process that created it,
// and mu never records that ownership anywhere else, so the sweeper infers
// abandonment from inactivity: a candidate is removed only when neither it
// nor anything beneath it has been modified for MinAge. Live writers touch
// their files continuously, and no mu operation runs anywhere near MinAge
// without writing, so concurrent processes are never disturbed. PID files
// were rejected because PIDs are reused and staging can outlive the host
// namespace; advisory locks were rejected because every creation site would
// need to hold a descriptor for its whole lifetime.
//
// Disk capacity policy. mu does not preflight free space. The expanded size
// of a compressed tar stream is unknown until it is read, free-space checks
// race with every other writer, and the extraction budget (archivelimit)
// already bounds the worst case. Instead every writer removes its partial
// output on failure (including ENOSPC), and this sweeper reclaims whatever a
// crash strands.
package staging

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	// MinAge is how long a candidate must be untouched before removal.
	MinAge = 24 * time.Hour
	// Interval rate-limits automatic sweeps.
	Interval = time.Hour
	// stampName records the last automatic sweep under ~/.mu.
	stampName = ".staging-sweep"
)

// Rule names leftovers under one directory.
type Rule struct {
	Dir string
	// Prefixes of entry names that mark staging leftovers. Empty means every
	// entry in Dir is transient (for example, OCI ingest).
	Prefixes []string
	// Depth is how many directory levels below Dir to search for matches;
	// 0 considers only Dir's direct children. Matched entries are not
	// descended into, and symlinks are never followed.
	Depth int
}

// Removal describes one reclaimed (or, in a dry run, reclaimable) path.
type Removal struct {
	Path  string `json:"path"`
	Bytes int64  `json:"bytes"`
}

// DefaultRules lists every staging location mu creates under home/.mu and
// in the system temporary directory tmp.
func DefaultRules(home, tmp string) []Rule {
	mu := filepath.Join(home, ".mu")
	cache := filepath.Join(mu, "cache")
	return []Rule{
		// Scratch extraction is transient; anything here older than MinAge
		// is a crashed staging tree or a pre-staging legacy tree.
		{Dir: filepath.Join(cache, "extract")},
		{Dir: filepath.Join(cache, "downloads"), Prefixes: []string{".fetch-"}},
		{Dir: filepath.Join(cache, "ingest")},
		{Dir: filepath.Join(mu, "plugins"), Prefixes: []string{".bundle-", ".plugin-"}, Depth: 1},
		{Dir: filepath.Join(mu, "toolchains"), Prefixes: []string{".tool-"}, Depth: 2},
		{Dir: mu, Prefixes: []string{".discover-cache-"}},
		{Dir: tmp, Prefixes: []string{
			"mu-cas-replay-", "mu-oci-blob-", "mu-plugin-fetch-", "mu-plugin-install-",
			"mu-sandbox-", "mu-sbpl-", "mu-out-", "mu-sealed-in-", "mu-sealed-out-",
		}},
	}
}

// Sweep removes (or with dryRun, reports) every candidate under rules that
// has been inactive for at least minAge as of now. Missing directories are
// ignored. It continues past individual failures and returns them joined.
func Sweep(rules []Rule, minAge time.Duration, now time.Time, dryRun bool) ([]Removal, error) {
	cutoff := now.Add(-minAge)
	var removed []Removal
	var errs []error
	for _, r := range rules {
		for _, path := range candidates(r) {
			bytes, active, err := inspect(path, cutoff)
			if err != nil {
				if !errors.Is(err, fs.ErrNotExist) {
					errs = append(errs, err)
				}
				continue
			}
			if active {
				continue
			}
			if !dryRun {
				if err := removeTree(path); err != nil {
					errs = append(errs, fmt.Errorf("staging: remove %s: %w", path, err))
					continue
				}
			}
			removed = append(removed, Removal{Path: path, Bytes: bytes})
		}
	}
	return removed, errors.Join(errs...)
}

// MaybeSweep runs the default sweep at most once per Interval. It is best
// effort: failures are ignored so that housekeeping never fails a command.
func MaybeSweep(home string, now time.Time) {
	if home == "" {
		return
	}
	stamp := filepath.Join(home, ".mu", stampName)
	if info, err := os.Stat(stamp); err == nil && now.Sub(info.ModTime()) < Interval {
		return
	}
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		return
	}
	// Stamp first so concurrent processes do not all sweep at once; a
	// duplicate sweep is harmless either way.
	if err := os.WriteFile(stamp, nil, 0o644); err != nil {
		return
	}
	_ = os.Chtimes(stamp, now, now)
	_, _ = Sweep(DefaultRules(home, os.TempDir()), MinAge, now, false)
}

func candidates(r Rule) []string {
	var out []string
	var walk func(dir string, depth int)
	walk = func(dir string, depth int) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return
		}
		for _, e := range entries {
			path := filepath.Join(dir, e.Name())
			if matches(e.Name(), r.Prefixes) {
				out = append(out, path)
				continue
			}
			if depth < r.Depth && e.Type().IsDir() {
				walk(path, depth+1)
			}
		}
	}
	walk(r.Dir, 0)
	return out
}

func matches(name string, prefixes []string) bool {
	if len(prefixes) == 0 {
		return true
	}
	for _, p := range prefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// inspect sums a candidate's size and reports whether anything in it was
// modified after cutoff. It stops at the first recent entry.
func inspect(path string, cutoff time.Time) (int64, bool, error) {
	var total int64
	errActive := errors.New("active")
	err := filepath.WalkDir(path, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := d.Info() // Lstat semantics: symlinks are not followed
		if err != nil {
			return err
		}
		if info.ModTime().After(cutoff) {
			return errActive
		}
		if info.Mode().IsRegular() {
			total += info.Size()
		}
		return nil
	})
	if errors.Is(err, errActive) {
		return 0, true, nil
	}
	return total, false, err
}

// removeTree deletes path, restoring owner write permission on directories
// that a sandbox or copied module cache left read-only.
func removeTree(path string) error {
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	_ = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			if info, ierr := d.Info(); ierr == nil {
				_ = os.Chmod(p, info.Mode().Perm()|0o700)
			}
		}
		return nil
	})
	return os.RemoveAll(path)
}
