// Package discovercache persists plugin discover responses keyed by CAS
// digest. Because plugins are content-addressed, a given digest produces
// the same discover response every time — making it safe to cache and
// skip the discover RPC on warm builds.
//
// The cache is a single JSON file (~/.mu/discover-cache.json) for easy
// inspection and one-line garbage collection. It is best-effort: a
// corrupt file is treated as empty, concurrent writers may lose a single
// entry across a race (recomputed on the next build — no correctness
// impact), and callers should not treat Put failures as fatal.
package discovercache

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/chau/mu/internal/cas"
	"github.com/chau/mu/internal/plugin"
)

// SchemaVersion is the top-level cache schema version. Bumping this value
// invalidates every entry in every user's cache file on the next run.
const SchemaVersion = 1

// Entry is a single cached discover response.
type Entry struct {
	Name            string                  `json:"name"`
	ProtocolVersion int                     `json:"protocol_version"`
	CachedAt        time.Time               `json:"cached_at"`
	Response        plugin.DiscoverResponse `json:"response"`
}

// file is the on-disk layout.
type file struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

// Cache is an on-disk map from plugin CAS digest → discover response.
// The zero value is not usable; obtain one via Open.
type Cache struct {
	path string

	mu      sync.Mutex
	entries map[string]Entry
}

// Open loads the cache file at path. A missing file, a corrupt file, or a
// schema mismatch all yield an empty cache; the underlying condition is
// not surfaced because a stale cache is never fatal. Callers may still
// pass a nil *Cache — all Get / Put calls are safe on nil.
func Open(path string) *Cache {
	c := &Cache{path: path, entries: make(map[string]Entry)}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return c
	}
	if f.Version != SchemaVersion {
		return c
	}
	if f.Entries != nil {
		c.entries = f.Entries
	}
	return c
}

// Get returns the cached discover response for digest if present and its
// captured protocol version matches the running binary. A mismatch is
// treated as a miss.
func (c *Cache) Get(digest cas.Digest) (*plugin.DiscoverResponse, bool) {
	if c == nil || digest.Hash == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[digest.String()]
	if !ok {
		return nil, false
	}
	if entry.ProtocolVersion != plugin.ProtocolVersion {
		return nil, false
	}
	resp := entry.Response
	return &resp, true
}

// Put stores a discover response for digest and persists the cache to
// disk atomically (write-temp + rename). A zero digest is ignored so
// command plugins (which have no content hash) are never cached.
//
// Concurrent Put calls in different processes may race; the loser's
// write will be overwritten, and its entry will be re-computed on the
// next build. This is acceptable for a best-effort cache.
func (c *Cache) Put(digest cas.Digest, resp *plugin.DiscoverResponse) error {
	if c == nil || resp == nil || digest.Hash == "" {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	c.entries[digest.String()] = Entry{
		Name:            resp.Name,
		ProtocolVersion: resp.ProtocolVersion,
		CachedAt:        time.Now().UTC(),
		Response:        *resp,
	}
	return c.flushLocked()
}

// flushLocked writes the in-memory map to disk, sorting keys for
// deterministic output. Caller must hold c.mu.
func (c *Cache) flushLocked() error {
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("discovercache: mkdir: %w", err)
	}

	keys := make([]string, 0, len(c.entries))
	for k := range c.entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	sorted := make(map[string]Entry, len(c.entries))
	for _, k := range keys {
		sorted[k] = c.entries[k]
	}

	payload := file{Version: SchemaVersion, Entries: sorted}
	buf, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("discovercache: marshal: %w", err)
	}

	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return fmt.Errorf("discovercache: write tmp: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("discovercache: rename: %w", err)
	}
	return nil
}

// DefaultPath returns ~/.mu/discover-cache.json. If the home directory
// cannot be resolved, returns an empty string — callers should treat
// that as "cache disabled."
func DefaultPath() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".mu", "discover-cache.json")
}
