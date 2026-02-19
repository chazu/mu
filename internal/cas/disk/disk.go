// Package disk implements a content-addressable store backed by the local filesystem.
package disk

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/chau/mu/internal/cas"
	"github.com/google/renameio"
)

// validHex matches a string of at least 2 lowercase hex characters.
var validHex = regexp.MustCompile(`^[0-9a-f]{2,}$`)

// DiskStore is a CAS backend that stores blobs and action results on disk.
type DiskStore struct {
	root    string
	blobDir string
	actDir  string
	tempDir string
}

// New creates a DiskStore rooted at the given directory, creating subdirectories
// as needed.
func New(root string) (*DiskStore, error) {
	blobDir := filepath.Join(root, "blobs", "sha256")
	actDir := filepath.Join(root, "actions")

	for _, d := range []string{blobDir, actDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("disk: create directory %s: %w", d, err)
		}
	}

	tmpDir := filepath.Join(root, "tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return nil, fmt.Errorf("disk: create temp directory: %w", err)
	}

	return &DiskStore{
		root:    root,
		blobDir: blobDir,
		actDir:  actDir,
		tempDir: tmpDir,
	}, nil
}

// validateHash checks that a hash string is at least 2 characters and contains
// only lowercase hex digits, preventing path traversal attacks.
func validateHash(hash string) error {
	if !validHex.MatchString(hash) {
		return fmt.Errorf("disk: invalid hash %q: must be lowercase hex and at least 2 characters", hash)
	}
	return nil
}

// blobPath returns the filesystem path for a blob with the given digest,
// using 2-level fan-out (first 2 hex chars as a subdirectory).
func (s *DiskStore) blobPath(dgst cas.Digest) (string, error) {
	if err := validateHash(dgst.Hash); err != nil {
		return "", err
	}
	h := dgst.Hash
	return filepath.Join(s.blobDir, h[:2], h[2:]), nil
}

// actionPath returns the filesystem path for an action result identified by the
// given action key.
func (s *DiskStore) actionPath(key cas.ActionKey) (string, error) {
	if err := validateHash(key.Digest.Hash); err != nil {
		return "", err
	}
	return filepath.Join(s.actDir, "sha256-"+key.Digest.Hash+".json"), nil
}

// Put streams data from r, computing a SHA-256 digest while writing to a
// temporary file, then atomically renames the temp file to the final blob path.
func (s *DiskStore) Put(_ context.Context, r io.Reader) (cas.Digest, error) {
	// Write to a temp file while hashing. We don't know the destination path
	// until hashing completes, so we use os.CreateTemp and os.Rename directly.
	tmp, err := os.CreateTemp(s.tempDir, "blob-*")
	if err != nil {
		return cas.Digest{}, fmt.Errorf("disk: create temp file: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { os.Remove(tmpName) }() // clean up on any error path

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), r); err != nil {
		tmp.Close()
		return cas.Digest{}, fmt.Errorf("disk: write blob: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return cas.Digest{}, fmt.Errorf("disk: sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return cas.Digest{}, fmt.Errorf("disk: close temp file: %w", err)
	}

	dgst := cas.NewSHA256(hex.EncodeToString(h.Sum(nil)))

	// Ensure the fan-out subdirectory exists.
	fanoutDir := filepath.Join(s.blobDir, dgst.Hash[:2])
	if err := os.MkdirAll(fanoutDir, 0o755); err != nil {
		return cas.Digest{}, fmt.Errorf("disk: create fan-out dir: %w", err)
	}

	// Atomic rename to final path. If two concurrent Puts produce the same
	// digest, both rename to the same path — idempotent by construction.
	dest, err := s.blobPath(dgst)
	if err != nil {
		return cas.Digest{}, err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return cas.Digest{}, fmt.Errorf("disk: rename blob: %w", err)
	}

	return dgst, nil
}

// Get opens the blob identified by dgst and returns a ReadCloser for its
// content. Returns an os.ErrNotExist-wrapped error if the blob is not found.
func (s *DiskStore) Get(_ context.Context, dgst cas.Digest) (io.ReadCloser, error) {
	p, err := s.blobPath(dgst)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("disk: get blob %s: %w", dgst, err)
	}
	return f, nil
}

// Has reports whether the blob identified by dgst exists in the store.
func (s *DiskStore) Has(_ context.Context, dgst cas.Digest) (bool, error) {
	p, err := s.blobPath(dgst)
	if err != nil {
		return false, err
	}
	_, err = os.Stat(p)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, fmt.Errorf("disk: stat blob %s: %w", dgst, err)
}

// Delete removes the blob identified by dgst. It is not an error if the blob
// does not exist.
func (s *DiskStore) Delete(_ context.Context, dgst cas.Digest) error {
	p, err := s.blobPath(dgst)
	if err != nil {
		return err
	}
	err = os.Remove(p)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("disk: delete blob %s: %w", dgst, err)
	}
	return nil
}

// PutActionResult serialises result as JSON and writes it atomically to the
// action result path derived from key.
func (s *DiskStore) PutActionResult(_ context.Context, key cas.ActionKey, result *cas.ActionResult) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("disk: marshal action result: %w", err)
	}

	dest, err := s.actionPath(key)
	if err != nil {
		return err
	}
	if err := renameio.WriteFile(dest, data, 0o644); err != nil {
		return fmt.Errorf("disk: write action result: %w", err)
	}
	return nil
}

// GetActionResult reads and deserialises the action result for key. Returns
// (nil, nil) on a cache miss (file not found).
func (s *DiskStore) GetActionResult(_ context.Context, key cas.ActionKey) (*cas.ActionResult, error) {
	p, err := s.actionPath(key)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("disk: read action result: %w", err)
	}

	var result cas.ActionResult
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("disk: unmarshal action result: %w", err)
	}
	return &result, nil
}
