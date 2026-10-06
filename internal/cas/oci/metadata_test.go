package oci

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
	godigest "github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

func TestMetadataExactLimitAndOversizedTail(t *testing.T) {
	const limit = 128
	for _, tc := range []struct {
		name, body string
		wantError  bool
	}{
		{"boundary", "{}" + strings.Repeat(" ", limit-2), false},
		{"tail", "{}" + strings.Repeat(" ", limit-1), true},
		{"second value", "{} {}", true},
		{"malformed", `{"secret":"synthetic-private",BROKEN}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var v any
			err := decodeMetadata(context.Background(), strings.NewReader(tc.body), limit, &v)
			if (err != nil) != tc.wantError {
				t.Fatalf("decode: %v", err)
			}
			if tc.name == "tail" && !errors.Is(err, ErrMetadataTooLarge) {
				t.Fatalf("missing limit error: %v", err)
			}
			if err != nil && strings.Contains(err.Error(), "synthetic-private") {
				t.Fatal("wire bytes leaked")
			}
		})
	}
}

type countingMetadataReader struct{ remaining, read int64 }

func (r *countingMetadataReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if int64(n) > r.remaining {
		n = int(r.remaining)
	}
	for i := 0; i < n; i++ {
		p[i] = ' '
	}
	r.remaining -= int64(n)
	r.read += int64(n)
	return n, nil
}
func (r *countingMetadataReader) Close() error { return nil }

type metadataRegistry struct {
	Registry
	desc    ocispec.Descriptor
	reader  io.ReadCloser
	fetches int
}

func (r *metadataRegistry) Resolve(context.Context, string) (ocispec.Descriptor, error) {
	return r.desc, nil
}
func (r *metadataRegistry) Fetch(context.Context, ocispec.Descriptor) (io.ReadCloser, error) {
	r.fetches++
	return r.reader, nil
}

func TestEveryMetadataEntryPointRejectsDeclaredOversizeBeforeFetch(t *testing.T) {
	for _, name := range []string{"action", "plugin", "index"} {
		t.Run(name, func(t *testing.T) {
			repo := &metadataRegistry{desc: ocispec.Descriptor{Size: MaxManifestBytes + 1}}
			var err error
			switch name {
			case "action":
				_, err = New(repo).GetActionResult(context.Background(), cas.ActionKey{})
			case "plugin":
				_, err = FetchPluginConfig(context.Background(), repo, "ref")
			case "index":
				_, err = FetchPluginIndex(context.Background(), repo)
			}
			if !errors.Is(err, ErrMetadataTooLarge) || repo.fetches != 0 {
				t.Fatalf("oversize: %v, fetches=%d", err, repo.fetches)
			}
		})
	}
}

func TestActualTransferCannotExceedBudgetDespiteDeclaredSize(t *testing.T) {
	reader := &countingMetadataReader{remaining: 1 << 30}
	repo := &metadataRegistry{desc: ocispec.Descriptor{Size: 2}, reader: reader}
	_, err := FetchPluginConfig(context.Background(), repo, "ref")
	if !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("accepted oversized transfer: %v", err)
	}
	if reader.read != MaxManifestBytes+1 {
		t.Fatalf("read %d bytes beyond bounded probe", reader.read)
	}
}

func TestLargeRepresentativeActionMetadataFitsBudgets(t *testing.T) {
	result := cas.ActionResult{Outputs: map[string]cas.Digest{}, OutputModes: map[string]uint32{}}
	for i := 0; i < 20000; i++ {
		name := string(rune(0x1000 + i))
		result.Outputs[name] = cas.NewSHA256(strings.Repeat("a", 64))
		result.OutputModes[name] = 0o755
	}
	data, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	var decoded cas.ActionResult
	if err := decodeMetadata(context.Background(), strings.NewReader(string(data)), MaxActionConfigBytes, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Outputs) != 20000 {
		t.Fatal("lost outputs")
	}
}

func TestOversizedConfigurationRejectedBeforeFetchingIt(t *testing.T) {
	repo := newMemStore()
	cfg := ocispec.Descriptor{MediaType: MediaTypePluginConfig, Size: MaxPluginConfigBytes + 1, Digest: godigest.FromString("oversized")}
	manifest := ocispec.Manifest{Config: cfg}
	data, _ := json.Marshal(manifest)
	desc := ocispec.Descriptor{Size: int64(len(data)), Digest: godigest.FromBytes(data)}
	if err := repo.Push(context.Background(), desc, strings.NewReader(string(data))); err != nil {
		t.Fatal(err)
	}
	if err := repo.Tag(context.Background(), desc, "ref"); err != nil {
		t.Fatal(err)
	}
	if _, err := FetchPluginConfig(context.Background(), repo, "ref"); !errors.Is(err, ErrMetadataTooLarge) {
		t.Fatalf("config boundary: %v", err)
	}
}

func FuzzBoundedMetadataDecoder(f *testing.F) {
	for _, seed := range []string{"{}", "{} {}", `{"outputs":{}}`, strings.Repeat(" ", 1025)} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		var v any
		err := decodeMetadata(context.Background(), strings.NewReader(string(data)), 1024, &v)
		if len(data) > 1024 && !errors.Is(err, ErrMetadataTooLarge) {
			t.Fatalf("oversize accepted: %v", err)
		}
		if len(data) <= 1024 && (err == nil) != json.Valid(data) {
			t.Fatalf("unexpected decoding result: %v", err)
		}
	})
}
