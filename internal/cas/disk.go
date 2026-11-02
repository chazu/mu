package cas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// DiskStore is mu's local content-addressed store. Every write is a private
// temporary file that is fsynced and then renamed into place, so concurrent
// mu processes sharing one directory never lose each other's entries and a
// crash never exposes a partially written blob or receipt. There is no shared
// index file to rewrite: an action result is one file named by its key.
//
// Layout under the root:
//
//	blobs/sha256/<hex>   blob bytes, named by their SHA-256
//	ac/<hex>             action result JSON, named by the action key's SHA-256
//	ingest/              in-flight temporary files (swept by internal/staging)
//
// The blob layout matches the OCI image layout, so caches written by earlier
// mu releases (oras-go's OCI layout store) keep their blobs. Their action
// results lived in a shared index.json; NewDiskStore migrates those once.
type DiskStore struct {
	root string
}

const (
	diskBlobsDir  = "blobs"
	diskActionDir = "ac"
	diskIngestDir = "ingest"

	legacyIndexName     = "index.json"
	legacyIndexMigrated = "index.json.migrated"
	legacyActionTagPfx  = "action-sha256-"
)

// MaxActionResultBytes bounds an action result read from disk. It matches the
// OCI action-config budget so a receipt that can be pushed can be read back.
const MaxActionResultBytes int64 = 8 << 20

// NewDiskStore opens (creating if needed) a disk store rooted at root.
func NewDiskStore(root string) (*DiskStore, error) {
	if root == "" {
		return nil, errors.New("cas: disk store requires a path")
	}
	s := &DiskStore{root: root}
	for _, dir := range []string{s.blobDir(), s.actionDir(), s.ingestDir()} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, fmt.Errorf("cas: open disk store %s: %w", root, err)
		}
	}
	s.migrateLegacyIndex()
	return s, nil
}

// Root returns the store's root directory.
func (s *DiskStore) Root() string { return s.root }

func (s *DiskStore) blobDir() string   { return filepath.Join(s.root, diskBlobsDir, "sha256") }
func (s *DiskStore) actionDir() string { return filepath.Join(s.root, diskActionDir) }
func (s *DiskStore) ingestDir() string { return filepath.Join(s.root, diskIngestDir) }

// BlobPath returns the file that holds dgst. Only complete identities name a
// file, so a prefix or path-like value can never resolve outside the store.
func (s *DiskStore) BlobPath(dgst Digest) (string, error) {
	if err := dgst.Validate(); err != nil {
		return "", err
	}
	return filepath.Join(s.blobDir(), dgst.Hash), nil
}

// ActionResultPath returns the file that holds the result for key.
func (s *DiskStore) ActionResultPath(key ActionKey) (string, error) {
	if err := key.Digest.Validate(); err != nil {
		return "", fmt.Errorf("cas: action key: %w", err)
	}
	return filepath.Join(s.actionDir(), key.Digest.Hash), nil
}

// Has reports whether the blob exists.
func (s *DiskStore) Has(_ context.Context, dgst Digest) (bool, error) {
	p, err := s.BlobPath(dgst)
	if err != nil {
		return false, fmt.Errorf("cas: check blob %s: %w", dgst, err)
	}
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("cas: check blob %s: %w", dgst, err)
	}
	return true, nil
}

// Get opens the blob. A missing blob yields an error matching fs.ErrNotExist.
func (s *DiskStore) Get(_ context.Context, dgst Digest) (io.ReadCloser, error) {
	p, err := s.BlobPath(dgst)
	if err != nil {
		return nil, fmt.Errorf("cas: get blob %s: %w", dgst, err)
	}
	f, err := os.Open(p)
	if err != nil {
		return nil, fmt.Errorf("cas: get blob %s: %w", dgst, err)
	}
	return f, nil
}

// Put streams r into a temporary file while hashing it, fsyncs it, and renames
// it to its digest. The name is derived from the bytes actually written, so a
// blob is digest-verified before it becomes visible.
func (s *DiskStore) Put(ctx context.Context, r io.Reader) (Digest, error) {
	return s.put(ctx, r, nil)
}

// PutExpected is Put for bytes claimed to be want, such as a remote fetch. A
// mismatch is discarded before anything becomes visible and returns an error
// matching ErrDigestMismatch.
func (s *DiskStore) PutExpected(ctx context.Context, want Digest, r io.Reader) error {
	if err := want.Validate(); err != nil {
		return fmt.Errorf("cas: put blob %s: %w", want, err)
	}
	_, err := s.put(ctx, r, &want)
	return err
}

func (s *DiskStore) put(ctx context.Context, r io.Reader, want *Digest) (Digest, error) {
	tmp, err := s.createTemp("blob-")
	if err != nil {
		return Digest{}, fmt.Errorf("cas: put blob: %w", err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	if _, err := io.Copy(io.MultiWriter(tmp, h), ContextReader(ctx, r)); err != nil {
		return Digest{}, fmt.Errorf("cas: put blob: %w", err)
	}
	dgst := NewSHA256(hex.EncodeToString(h.Sum(nil)))
	if want != nil && dgst != *want {
		return Digest{}, fmt.Errorf("cas: put blob %s: %w: content hashes to %s", *want, ErrDigestMismatch, dgst)
	}
	dest := filepath.Join(s.blobDir(), dgst.Hash)
	if _, err := os.Stat(dest); err == nil {
		// Content-addressed: an existing file already holds these bytes.
		return dgst, nil
	}
	if err := commitTemp(tmp, dest); err != nil {
		return Digest{}, fmt.Errorf("cas: put blob %s: %w", dgst, err)
	}
	committed = true
	return dgst, nil
}

// Delete removes the blob. A missing blob yields an error matching
// fs.ErrNotExist.
func (s *DiskStore) Delete(_ context.Context, dgst Digest) error {
	p, err := s.BlobPath(dgst)
	if err != nil {
		return fmt.Errorf("cas: delete blob %s: %w", dgst, err)
	}
	if err := os.Remove(p); err != nil {
		return fmt.Errorf("cas: delete blob %s: %w", dgst, err)
	}
	return nil
}

// PutActionResult writes result to its own file, atomically replacing any
// previous result for key.
func (s *DiskStore) PutActionResult(_ context.Context, key ActionKey, result *ActionResult) error {
	dest, err := s.ActionResultPath(key)
	if err != nil {
		return err
	}
	for name, out := range result.Outputs {
		if err := out.Validate(); err != nil {
			return fmt.Errorf("cas: action result output %q: %w", name, err)
		}
	}
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("cas: marshal action result: %w", err)
	}
	return s.writeFile(dest, data)
}

// GetActionResult reads the result for key. It returns (nil, nil) on a miss.
func (s *DiskStore) GetActionResult(_ context.Context, key ActionKey) (*ActionResult, error) {
	p, err := s.ActionResultPath(key)
	if err != nil {
		return nil, err
	}
	result, err := readActionResultFile(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return result, err
}

// WalkActionResults calls fn for every stored action result in key order.
// Unreadable or invalid entries are passed with a nil result and a non-nil
// error; fn decides whether to skip them. A non-nil return from fn stops the
// walk and is returned.
func (s *DiskStore) WalkActionResults(fn func(key ActionKey, result *ActionResult, err error) error) error {
	entries, err := os.ReadDir(s.actionDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("cas: list action results: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && IsSHA256Hex(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		key := ActionKey{Digest: NewSHA256(name)}
		result, rerr := readActionResultFile(filepath.Join(s.actionDir(), name))
		if errors.Is(rerr, fs.ErrNotExist) {
			continue // removed concurrently
		}
		if err := fn(key, result, rerr); err != nil {
			return err
		}
	}
	return nil
}

func readActionResultFile(p string) (*ActionResult, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxActionResultBytes+1))
	if err != nil {
		return nil, fmt.Errorf("cas: read action result %s: %w", filepath.Base(p), err)
	}
	if int64(len(data)) > MaxActionResultBytes {
		return nil, fmt.Errorf("cas: action result %s exceeds %d bytes", filepath.Base(p), MaxActionResultBytes)
	}
	result, err := decodeActionResult(data)
	if err != nil {
		return nil, fmt.Errorf("cas: action result %s: %w", filepath.Base(p), err)
	}
	return result, nil
}

// decodeActionResult parses exactly one JSON object and validates every output
// identity, so a receipt can never name a non-content path.
func decodeActionResult(data []byte) (*ActionResult, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	var result ActionResult
	if err := dec.Decode(&result); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if dec.More() {
		return nil, errors.New("decode: trailing data")
	}
	for name, out := range result.Outputs {
		if err := out.Validate(); err != nil {
			return nil, fmt.Errorf("output %q: %w", name, err)
		}
	}
	return &result, nil
}

// writeFile atomically publishes data at dest.
func (s *DiskStore) writeFile(dest string, data []byte) error {
	tmp, err := s.createTemp("ac-")
	if err != nil {
		return fmt.Errorf("cas: write %s: %w", filepath.Base(dest), err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return fmt.Errorf("cas: write %s: %w", filepath.Base(dest), err)
	}
	if err := commitTemp(tmp, dest); err != nil {
		os.Remove(tmp.Name())
		return fmt.Errorf("cas: write %s: %w", filepath.Base(dest), err)
	}
	return nil
}

// createTemp opens a private file in the ingest directory, recreating the
// directory if something removed it while the store was open. The ingest
// directory shares the root's filesystem, so the final rename is atomic.
func (s *DiskStore) createTemp(prefix string) (*os.File, error) {
	f, err := os.CreateTemp(s.ingestDir(), prefix+"*")
	if errors.Is(err, fs.ErrNotExist) {
		if mkErr := os.MkdirAll(s.ingestDir(), 0o755); mkErr != nil {
			return nil, mkErr
		}
		f, err = os.CreateTemp(s.ingestDir(), prefix+"*")
	}
	return f, err
}

// commitTemp fsyncs and closes tmp, then renames it to dest. On error the
// caller still owns (and must remove) tmp's path.
func commitTemp(tmp *os.File, dest string) error {
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	err := os.Rename(tmp.Name(), dest)
	if errors.Is(err, fs.ErrNotExist) {
		if mkErr := os.MkdirAll(filepath.Dir(dest), 0o755); mkErr != nil {
			return mkErr
		}
		err = os.Rename(tmp.Name(), dest)
	}
	return err
}

// migrateLegacyIndex converts action results recorded by earlier releases in
// an OCI layout index.json (tag action-sha256-<key> -> manifest -> config
// blob) into per-key files, then renames the index out of the way so the
// migration runs once. It is best effort: an unreadable index or entry is a
// cache miss, never an error. Concurrent migrations are idempotent.
func (s *DiskStore) migrateLegacyIndex() {
	indexPath := filepath.Join(s.root, legacyIndexName)
	data, err := os.ReadFile(indexPath)
	if err != nil {
		return
	}
	var index struct {
		Manifests []struct {
			Digest      string            `json:"digest"`
			Annotations map[string]string `json:"annotations"`
		} `json:"manifests"`
	}
	if json.Unmarshal(data, &index) == nil {
		for _, m := range index.Manifests {
			tag := m.Annotations["org.opencontainers.image.ref.name"]
			hash, ok := strings.CutPrefix(tag, legacyActionTagPfx)
			if !ok || !IsSHA256Hex(hash) {
				continue
			}
			s.migrateLegacyEntry(ActionKey{Digest: NewSHA256(hash)}, m.Digest)
		}
	}
	_ = os.Rename(indexPath, filepath.Join(s.root, legacyIndexMigrated))
}

func (s *DiskStore) migrateLegacyEntry(key ActionKey, manifestDigest string) {
	dest, err := s.ActionResultPath(key)
	if err != nil {
		return
	}
	if _, err := os.Stat(dest); err == nil {
		return
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if s.readLegacyJSON(manifestDigest, &manifest) != nil {
		return
	}
	configDigest, err := ParseFullDigest(manifest.Config.Digest)
	if err != nil {
		return
	}
	p, _ := s.BlobPath(configDigest)
	result, err := readActionResultFile(p)
	if err != nil {
		return
	}
	_ = s.PutActionResult(context.Background(), key, result)
}

func (s *DiskStore) readLegacyJSON(digest string, dest any) error {
	d, err := ParseFullDigest(digest)
	if err != nil {
		return err
	}
	p, _ := s.BlobPath(d)
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxActionResultBytes+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > MaxActionResultBytes {
		return errors.New("legacy manifest too large")
	}
	return json.Unmarshal(data, dest)
}
