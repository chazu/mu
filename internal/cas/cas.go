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

// ParseDigest parses a string in "algorithm:hash" format into a Digest.
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

// ActionResult holds the outputs and exit code produced by an action.
type ActionResult struct {
	Outputs  map[string]Digest `json:"outputs"` // output name -> content digest
	ExitCode int               `json:"exit_code"`
}

// Store is the interface for a content-addressable store that also supports
// action-result caching.
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
