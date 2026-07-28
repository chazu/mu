package plugincatalog

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

const LockSchemaVersion = 1

// LockFile pins source packages selected for a project.
type LockFile struct {
	SchemaVersion int            `json:"schema_version"`
	Catalog       LockedCatalog  `json:"catalog"`
	Plugins       []LockedPlugin `json:"plugins"`
}

// LockedCatalog records the catalog revision used to resolve the entries.
type LockedCatalog struct {
	URL        string `json:"url"`
	Repository string `json:"repository"`
	ReleaseTag string `json:"release_tag"`
}

// LockedPlugin records both the immutable source asset and the local CAS
// bundle produced from it.
type LockedPlugin struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	SourceRevision string `json:"source_revision"`
	AssetURL       string `json:"asset_url"`
	AssetSHA256    string `json:"asset_sha256"`
	Path           string `json:"path"`
	Entrypoint     string `json:"entrypoint"`
	Toolchain      string `json:"toolchain,omitempty"`
	BundleDigest   string `json:"bundle_digest"`
}

// NewLock returns an empty, valid lockfile.
func NewLock() *LockFile {
	return &LockFile{SchemaVersion: LockSchemaVersion, Plugins: []LockedPlugin{}}
}

// LoadLock reads path. A missing file is treated as an empty lockfile.
func LoadLock(path string) (*LockFile, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return NewLock(), nil
	}
	if err != nil {
		return nil, fmt.Errorf("open lockfile: %w", err)
	}
	defer f.Close()
	var lock LockFile
	dec := json.NewDecoder(f)
	if err := dec.Decode(&lock); err != nil {
		return nil, fmt.Errorf("decode lockfile: %w", err)
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return nil, fmt.Errorf("lockfile contains trailing JSON")
		}
		return nil, fmt.Errorf("read lockfile tail: %w", err)
	}
	if err := lock.Validate(); err != nil {
		return nil, err
	}
	return &lock, nil
}

// Validate checks lockfile structure and duplicate names.
func (l *LockFile) Validate() error {
	if l.SchemaVersion != LockSchemaVersion {
		return fmt.Errorf("lockfile schema_version %d is unsupported (want %d)", l.SchemaVersion, LockSchemaVersion)
	}
	seen := make(map[string]struct{}, len(l.Plugins))
	for i, p := range l.Plugins {
		if p.Name == "" || p.Version == "" || p.SourceRevision == "" || p.AssetSHA256 == "" || p.BundleDigest == "" {
			return fmt.Errorf("lockfile plugin %d is missing name, version, source_revision, asset_sha256, or bundle_digest", i)
		}
		if _, ok := seen[p.Name]; ok {
			return fmt.Errorf("lockfile contains duplicate plugin %q", p.Name)
		}
		seen[p.Name] = struct{}{}
	}
	return nil
}

// Find returns a locked plugin by name.
func (l *LockFile) Find(name string) (*LockedPlugin, bool) {
	for i := range l.Plugins {
		if l.Plugins[i].Name == name {
			return &l.Plugins[i], true
		}
	}
	return nil, false
}

// Upsert replaces or appends a plugin entry and keeps entries sorted.
func (l *LockFile) Upsert(entry LockedPlugin) {
	for i := range l.Plugins {
		if l.Plugins[i].Name == entry.Name {
			l.Plugins[i] = entry
			sort.Slice(l.Plugins, func(i, j int) bool { return l.Plugins[i].Name < l.Plugins[j].Name })
			return
		}
	}
	l.Plugins = append(l.Plugins, entry)
	sort.Slice(l.Plugins, func(i, j int) bool { return l.Plugins[i].Name < l.Plugins[j].Name })
}

// WriteLock writes a lockfile atomically with stable indentation.
func WriteLock(path string, lock *LockFile) error {
	if err := lock.Validate(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create lockfile directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".mu-lock-*.tmp")
	if err != nil {
		return fmt.Errorf("create lockfile temp: %w", err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	enc := json.NewEncoder(tmp)
	enc.SetIndent("", "  ")
	if err := enc.Encode(lock); err != nil {
		tmp.Close()
		return fmt.Errorf("encode lockfile: %w", err)
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return fmt.Errorf("chmod lockfile: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close lockfile: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace lockfile: %w", err)
	}
	return nil
}
