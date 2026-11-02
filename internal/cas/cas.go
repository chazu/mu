// Package cas defines the core content-addressable storage types for mu.
package cas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ErrNotFound marks a confirmed miss from a remote store, as opposed to a
// failure to reach it. The local DiskStore reports misses as fs.ErrNotExist.
var ErrNotFound = errors.New("not found")

// ErrDigestMismatch marks content whose bytes do not hash to the digest it
// was requested or stored under.
var ErrDigestMismatch = errors.New("digest mismatch")

// Digest identifies a blob by its hash algorithm and hex-encoded hash value.
type Digest struct {
	Algorithm string // "sha256"
	Hash      string // hex-encoded hash
}

// String returns the digest in "algorithm:hash" format.
func (d Digest) String() string {
	return d.Algorithm + ":" + d.Hash
}

// IsZero reports whether the digest is the zero value.
func (d Digest) IsZero() bool {
	return d.Algorithm == "" && d.Hash == ""
}

// NewSHA256 returns a Digest with algorithm "sha256" and the given hex hash.
func NewSHA256(hash string) Digest {
	return Digest{Algorithm: "sha256", Hash: hash}
}

// ErrInvalidDigest marks a value that is not a complete content identity.
var ErrInvalidDigest = errors.New("cas: invalid digest")

// SHA256HexLen is the length of a hex-encoded SHA-256 hash.
const SHA256HexLen = sha256.Size * 2

// IsSHA256Hex reports whether s is exactly 64 lowercase hexadecimal characters,
// the canonical OCI encoding of a SHA-256 hash.
func IsSHA256Hex(s string) bool {
	if len(s) != SHA256HexLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// Validate reports whether d is a complete identity mu can store and fetch:
// algorithm "sha256" and 64 lowercase hex characters. Abbreviated display
// prefixes are not identities and fail here; inspection code that accepts
// prefixes must match them against full digests instead.
func (d Digest) Validate() error {
	if d.Algorithm != "sha256" {
		return fmt.Errorf("%w: unsupported algorithm %q (want sha256)", ErrInvalidDigest, d.Algorithm)
	}
	if !IsSHA256Hex(d.Hash) {
		return fmt.Errorf("%w: sha256 hash must be %d lowercase hex characters", ErrInvalidDigest, SHA256HexLen)
	}
	return nil
}

// ParseFullDigest parses "sha256:<64 lowercase hex>" and rejects anything
// else. Use it wherever a digest crosses a trust boundary (configuration,
// CLI flags, registries, plugin or program input) and will be used to fetch,
// store, or name content.
func ParseFullDigest(s string) (Digest, error) {
	d, err := ParseDigest(s)
	if err != nil {
		return Digest{}, fmt.Errorf("%w: %v", ErrInvalidDigest, err)
	}
	if err := d.Validate(); err != nil {
		return Digest{}, err
	}
	return d, nil
}

// ParseDigest splits "algorithm:hash" syntactically without checking the
// algorithm or hash encoding. It is for display, inspection, and legacy
// fixtures; content lookups at external boundaries use ParseFullDigest.
func ParseDigest(s string) (Digest, error) {
	alg, hash, ok := strings.Cut(s, ":")
	if !ok {
		return Digest{}, errors.New("cas: invalid digest format: missing ':'")
	}
	if alg == "" {
		return Digest{}, errors.New("cas: invalid digest format: empty algorithm")
	}
	if hash == "" {
		return Digest{}, errors.New("cas: invalid digest format: empty hash")
	}
	return Digest{Algorithm: alg, Hash: hash}, nil
}

// ActionKey identifies a cached action by the hash of its canonical key material.
type ActionKey struct {
	Digest Digest // hash of the canonical key material
}

// ActionResultVersion identifies receipts containing permission metadata.
const ActionResultVersion = 2

// ActionResult holds the output digests, permission bits, and exit code.
// Version zero denotes legacy receipts without permission metadata.
type ActionResult struct {
	Version     int               `json:"version,omitempty"`
	OutputModes map[string]uint32 `json:"output_modes,omitempty"` // POSIX permission bits only

	Outputs  map[string]Digest `json:"outputs"` // output name -> content digest
	ExitCode int               `json:"exit_code"`
}

// Store is the interface for a content-addressable store that also supports
// action-result caching.
//
// Get must return a digest-verified reader (wrap it with VerifyReader): a
// caller that reads to EOF without error has received exactly the bytes named
// by the digest. Callers rely on this and do not re-hash what they read.
type Store interface {
	Has(ctx context.Context, dgst Digest) (bool, error)
	Get(ctx context.Context, dgst Digest) (io.ReadCloser, error)
	Put(ctx context.Context, r io.Reader) (Digest, error)
	Delete(ctx context.Context, dgst Digest) error
	GetActionResult(ctx context.Context, key ActionKey) (*ActionResult, error)
	PutActionResult(ctx context.Context, key ActionKey, result *ActionResult) error
}

// ComputeDigest hashes an io.Reader with SHA-256 and returns the Digest.
func ComputeDigest(r io.Reader) (Digest, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return Digest{}, fmt.Errorf("cas: computing digest: %w", err)
	}
	return NewSHA256(hex.EncodeToString(h.Sum(nil))), nil
}
