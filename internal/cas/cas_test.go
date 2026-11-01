package cas

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
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

func TestParseFullDigest(t *testing.T) {
	full := strings.Repeat("ab", 32)
	d, err := ParseFullDigest("sha256:" + full)
	if err != nil || d != NewSHA256(full) {
		t.Fatalf("ParseFullDigest(full) = %+v, %v", d, err)
	}

	cases := map[string]string{
		"missing colon":      "sha256" + full,
		"empty hash":         "sha256:",
		"short prefix":       "sha256:" + full[:12],
		"too long":           "sha256:" + full + "00",
		"uppercase hex":      "sha256:" + strings.ToUpper(full),
		"non-hex":            "sha256:" + strings.Repeat("zz", 32),
		"path traversal":     "sha256:../../" + full[:58],
		"embedded separator": "sha256:" + full[:63] + "/",
		"unsupported alg":    "sha512:" + strings.Repeat("ab", 64),
		"malformed alg":      "SHA256:" + full,
		"empty alg":          ":" + full,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseFullDigest(input)
			if !errors.Is(err, ErrInvalidDigest) {
				t.Fatalf("ParseFullDigest(%q) error = %v, want ErrInvalidDigest", input, err)
			}
		})
	}
}

// Syntactic parsing keeps working for abbreviated inspection references.
func TestParseDigestAcceptsInspectionPrefixes(t *testing.T) {
	d, err := ParseDigest("sha256:abc123")
	if err != nil {
		t.Fatalf("ParseDigest(prefix) error: %v", err)
	}
	if err := d.Validate(); !errors.Is(err, ErrInvalidDigest) {
		t.Fatalf("prefix Validate() = %v, want ErrInvalidDigest", err)
	}
}

func TestComputedDigestValidates(t *testing.T) {
	d, err := ComputeDigest(strings.NewReader("hello"))
	if err != nil {
		t.Fatal(err)
	}
	if err := d.Validate(); err != nil {
		t.Fatalf("computed digest failed validation: %v", err)
	}
}
