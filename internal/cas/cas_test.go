package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

func TestDigestString(t *testing.T) {
	d := Digest{Algorithm: "sha256", Hash: "abc123"}
	got := d.String()
	want := "sha256:abc123"
	if got != want {
		t.Errorf("Digest.String() = %q, want %q", got, want)
	}
}

func TestParseDigest(t *testing.T) {
	original := Digest{Algorithm: "sha256", Hash: "deadbeef"}
	s := original.String()

	parsed, err := ParseDigest(s)
	if err != nil {
		t.Fatalf("ParseDigest(%q) returned error: %v", s, err)
	}
	if parsed != original {
		t.Errorf("ParseDigest(%q) = %+v, want %+v", s, parsed, original)
	}
}

func TestParseDigestErrors(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"missing colon", "sha256deadbeef"},
		{"empty algorithm", ":deadbeef"},
		{"empty hash", "sha256:"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseDigest(tc.input)
			if err == nil {
				t.Errorf("ParseDigest(%q) expected error, got nil", tc.input)
			}
		})
	}
}

func TestDigestIsZero(t *testing.T) {
	zero := Digest{}
	if !zero.IsZero() {
		t.Error("zero-value Digest.IsZero() = false, want true")
	}

	nonZero := NewSHA256("abc")
	if nonZero.IsZero() {
		t.Error("non-zero Digest.IsZero() = true, want false")
	}
}

func TestComputeDigest(t *testing.T) {
	content := "hello, world"
	h := sha256.Sum256([]byte(content))
	wantHash := hex.EncodeToString(h[:])

	dgst, err := ComputeDigest(strings.NewReader(content))
	if err != nil {
		t.Fatalf("ComputeDigest returned error: %v", err)
	}
	if dgst.Algorithm != "sha256" {
		t.Errorf("Algorithm = %q, want %q", dgst.Algorithm, "sha256")
	}
	if dgst.Hash != wantHash {
		t.Errorf("Hash = %q, want %q", dgst.Hash, wantHash)
	}
}
