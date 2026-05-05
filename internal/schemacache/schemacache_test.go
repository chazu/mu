package schemacache_test

import (
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/chau/mu/internal/schemacache"
)

func newCache(t *testing.T) *schemacache.Cache {
	t.Helper()
	c, err := schemacache.New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return c
}

func TestInsertAndRead(t *testing.T) {
	c := newCache(t)
	files := []schemacache.File{
		{RelPath: "ec2.cue", Content: []byte("package aws\n#EC2Instance: {}\n")},
		{RelPath: "vpc/vpc.cue", Content: []byte("package vpc\n#VPC: {}\n")},
	}
	if err := c.Insert("mu/aws", "v1", files); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if !c.Has("mu/aws", "v1") {
		t.Fatal("expected Has to be true after Insert")
	}
	got, err := c.Read("mu/aws", "v1", "ec2.cue")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(got) != string(files[0].Content) {
		t.Errorf("Read content mismatch: got %q want %q", got, files[0].Content)
	}
}

func TestFilesListsCueOnly(t *testing.T) {
	c := newCache(t)
	if err := c.Insert("mu/aws", "v1", []schemacache.File{
		{RelPath: "b.cue", Content: []byte("package x\n")},
		{RelPath: "a.cue", Content: []byte("package y\n")},
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	got, err := c.Files("mu/aws", "v1")
	if err != nil {
		t.Fatalf("Files: %v", err)
	}
	want := []string{"a.cue", "b.cue"}
	if len(got) != len(want) {
		t.Fatalf("Files len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("Files[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestInsertIdempotent(t *testing.T) {
	c := newCache(t)
	files := []schemacache.File{{RelPath: "x.cue", Content: []byte("package x\n")}}
	if err := c.Insert("mu/aws", "v1", files); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if err := c.Insert("mu/aws", "v1", files); err != nil {
		t.Fatalf("re-Insert (matching): %v", err)
	}
}

func TestInsertVersionMismatchOnContent(t *testing.T) {
	c := newCache(t)
	if err := c.Insert("mu/aws", "v1", []schemacache.File{{RelPath: "x.cue", Content: []byte("a")}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	err := c.Insert("mu/aws", "v1", []schemacache.File{{RelPath: "x.cue", Content: []byte("b")}})
	if !errors.Is(err, schemacache.ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
}

func TestInsertVersionMismatchOnExtraFile(t *testing.T) {
	c := newCache(t)
	if err := c.Insert("mu/aws", "v1", []schemacache.File{{RelPath: "x.cue", Content: []byte("a")}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	err := c.Insert("mu/aws", "v1", []schemacache.File{
		{RelPath: "x.cue", Content: []byte("a")},
		{RelPath: "y.cue", Content: []byte("b")},
	})
	if !errors.Is(err, schemacache.ErrVersionMismatch) {
		t.Fatalf("expected ErrVersionMismatch, got %v", err)
	}
}

func TestMultipleVersionsCoexist(t *testing.T) {
	c := newCache(t)
	if err := c.Insert("mu/aws", "v1", []schemacache.File{{RelPath: "x.cue", Content: []byte("v1")}}); err != nil {
		t.Fatalf("Insert v1: %v", err)
	}
	if err := c.Insert("mu/aws", "v2", []schemacache.File{{RelPath: "x.cue", Content: []byte("v2")}}); err != nil {
		t.Fatalf("Insert v2: %v", err)
	}
	for _, v := range []string{"v1", "v2"} {
		got, err := c.Read("mu/aws", v, "x.cue")
		if err != nil {
			t.Fatalf("Read %s: %v", v, err)
		}
		if string(got) != v {
			t.Errorf("Read %s = %q, want %q", v, got, v)
		}
	}
}

func TestReadNotFound(t *testing.T) {
	c := newCache(t)
	if _, err := c.Read("mu/missing", "v1", "x.cue"); !errors.Is(err, schemacache.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing version, got %v", err)
	}
	if err := c.Insert("mu/aws", "v1", []schemacache.File{{RelPath: "x.cue", Content: []byte("a")}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := c.Read("mu/aws", "v1", "missing.cue"); !errors.Is(err, schemacache.ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing file, got %v", err)
	}
}

func TestRejectNonCueFile(t *testing.T) {
	c := newCache(t)
	err := c.Insert("mu/aws", "v1", []schemacache.File{{RelPath: "readme.md", Content: []byte("hi")}})
	if err == nil {
		t.Fatal("expected error for non-.cue file")
	}
}

func TestRejectPathEscape(t *testing.T) {
	c := newCache(t)
	cases := []string{"../escape.cue", "a/../../b.cue", "/abs.cue"}
	for _, p := range cases {
		err := c.Insert("mu/aws", "v1", []schemacache.File{{RelPath: p, Content: []byte("x")}})
		if err == nil {
			t.Errorf("expected error for path %q", p)
		}
	}
}

func TestConcurrentInsertSameVersion(t *testing.T) {
	c := newCache(t)
	files := []schemacache.File{{RelPath: "x.cue", Content: []byte("package x\n")}}
	const N = 8
	errs := make(chan error, N)
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- c.Insert("mu/aws", "v1", files)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("concurrent insert: %v", err)
		}
	}
	if !c.Has("mu/aws", "v1") {
		t.Fatal("expected Has after concurrent inserts")
	}
}

func TestHashStability(t *testing.T) {
	c1 := newCache(t)
	c2 := newCache(t)
	files := []schemacache.File{
		{RelPath: "a.cue", Content: []byte("package x\n")},
		{RelPath: "b.cue", Content: []byte("package y\n")},
	}
	if err := c1.Insert("mu/aws", "v1", files); err != nil {
		t.Fatalf("Insert c1: %v", err)
	}
	if err := c2.Insert("mu/aws", "v1", files); err != nil {
		t.Fatalf("Insert c2: %v", err)
	}
	h1, err := c1.Hash("mu/aws", "v1")
	if err != nil {
		t.Fatalf("Hash c1: %v", err)
	}
	h2, err := c2.Hash("mu/aws", "v1")
	if err != nil {
		t.Fatalf("Hash c2: %v", err)
	}
	if h1 != h2 {
		t.Errorf("hash mismatch across cache instances: %s vs %s", h1, h2)
	}
}

func TestModulePathOnDisk(t *testing.T) {
	c := newCache(t)
	if err := c.Insert("mu/aws", "v1", []schemacache.File{{RelPath: "x.cue", Content: []byte("a")}}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	expected := filepath.Join(c.Root(), "mu", "aws", "v1", "x.cue")
	if _, err := filepath.Abs(expected); err != nil {
		t.Fatalf("Abs: %v", err)
	}
}
