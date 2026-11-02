package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
)

// VerifyReader wraps rc so that the bytes it yields are checked against want.
// It hashes as it streams; when the underlying reader reaches EOF and the
// content does not hash to want, the final Read returns an error matching
// ErrDigestMismatch instead of io.EOF, and onMismatch (if non-nil) runs once.
//
// Verification happens only at EOF, so callers must read to the end before
// trusting what they consumed — io.Copy and io.ReadAll do. A caller that
// stops early (an archive reader that ignores trailing padding, say) must
// drain the rest with io.Copy(io.Discard, r) and check that error before it
// publishes anything derived from the content.
//
// Every Store's Get returns a reader wrapped this way; it is the single place
// CAS reads are verified.
func VerifyReader(rc io.ReadCloser, want Digest, onMismatch func()) io.ReadCloser {
	return &verifyingReader{rc: rc, want: want, h: sha256.New(), onMismatch: onMismatch}
}

type verifyingReader struct {
	rc         io.ReadCloser
	want       Digest
	h          hash.Hash
	onMismatch func()
	final      error // sticky result once the underlying reader hit EOF
}

func (v *verifyingReader) Read(p []byte) (int, error) {
	if v.final != nil {
		return 0, v.final
	}
	n, err := v.rc.Read(p)
	v.h.Write(p[:n])
	if err != io.EOF {
		return n, err
	}
	got := NewSHA256(hex.EncodeToString(v.h.Sum(nil)))
	if v.want.Algorithm != "sha256" || got != v.want {
		v.final = fmt.Errorf("cas: blob %s: %w: content hashes to %s", v.want, ErrDigestMismatch, got)
		if v.onMismatch != nil {
			v.onMismatch()
		}
	} else {
		v.final = io.EOF
	}
	return n, v.final
}

func (v *verifyingReader) Close() error { return v.rc.Close() }
