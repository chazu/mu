package dag

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/chazu/mu/internal/cas"
)

type restoreStore struct {
	cas.Store
	read func() io.ReadCloser
}

func (s restoreStore) Get(context.Context, cas.Digest) (io.ReadCloser, error) { return s.read(), nil }

type failingRestoreReader struct {
	io.Reader
	readErr, closeErr error
}

func (r failingRestoreReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if err == io.EOF && r.readErr != nil {
		return n, r.readErr
	}
	return n, err
}
func (r failingRestoreReader) Close() error { return r.closeErr }

func TestRestoreFailurePreservesExistingOutput(t *testing.T) {
	for _, failure := range []string{"read", "close", "digest", "legacy", "mode", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			dest := filepath.Join(root, "out")
			if err := os.WriteFile(dest, []byte("original"), 0o755); err != nil {
				t.Fatal(err)
			}
			digest, err := cas.ComputeDigest(strings.NewReader("replacement"))
			if err != nil {
				t.Fatal(err)
			}
			result := &cas.ActionResult{Version: cas.ActionResultVersion, Outputs: map[string]cas.Digest{"out": digest}, OutputModes: map[string]uint32{"out": 0o640}}
			store := restoreStore{read: func() io.ReadCloser {
				reader := failingRestoreReader{Reader: strings.NewReader("replacement")}
				switch failure {
				case "read":
					reader.readErr = errors.New("broken read")
				case "close":
					reader.closeErr = errors.New("broken close")
				case "digest":
					reader.Reader = strings.NewReader("corrupt")
				}
				return reader
			}}
			if failure == "legacy" {
				result.Version = 0
			}
			if failure == "mode" {
				result.OutputModes["out"] = 0o4755
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if failure == "cancel" {
				cancel()
			}
			if err := (&Executor{Store: store}).restoreOutputs(ctx, &Action{WorkDir: root, Outputs: []string{"out"}}, result); err == nil {
				t.Fatal("accepted invalid restore")
			}
			content, err := os.ReadFile(dest)
			if err != nil || string(content) != "original" {
				t.Fatalf("changed existing output: %q, %v", content, err)
			}
			info, err := os.Stat(dest)
			if err != nil || info.Mode().Perm() != 0o755 {
				t.Fatalf("changed existing permissions: %v, %v", info, err)
			}
			entries, _ := filepath.Glob(filepath.Join(root, ".mu-output-*"))
			if len(entries) > 0 {
				t.Fatalf("left staging files: %v", entries)
			}
		})
	}
}

func TestRestoreStagesAllOutputsBeforePublishing(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"first", "second"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("original"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	digest, err := cas.ComputeDigest(strings.NewReader("replacement"))
	if err != nil {
		t.Fatal(err)
	}
	result := &cas.ActionResult{Version: cas.ActionResultVersion, Outputs: map[string]cas.Digest{"first": digest, "second": digest}, OutputModes: map[string]uint32{"first": 0o644, "second": 0o644}}
	calls := 0
	store := restoreStore{read: func() io.ReadCloser {
		calls++
		if calls == 2 {
			return io.NopCloser(strings.NewReader("corrupt"))
		}
		return io.NopCloser(strings.NewReader("replacement"))
	}}
	if err := (&Executor{Store: store}).restoreOutputs(context.Background(), &Action{WorkDir: root, Outputs: []string{"first", "second"}}, result); err == nil {
		t.Fatal("accepted corrupt second output")
	}
	for _, name := range []string{"first", "second"} {
		content, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(content) != "original" {
			t.Fatalf("published %s before all reads succeeded", name)
		}
	}
}
