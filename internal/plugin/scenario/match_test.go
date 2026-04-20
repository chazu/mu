package scenario

import (
	"strings"
	"testing"
)

func TestMatchShape_LiteralsPass(t *testing.T) {
	s := &Scenario{Expect: Expect{Match: "shape", Shape: map[string]any{
		"name": "go",
		"protocol_version": 1,
	}}}
	err := s.Match(map[string]any{
		"name":             "go",
		"protocol_version": float64(1),
		"extra":            "ignored",
	})
	if err != nil {
		t.Fatalf("expected match, got %v", err)
	}
}

func TestMatchShape_LiteralMismatch(t *testing.T) {
	s := &Scenario{Expect: Expect{Match: "shape", Shape: map[string]any{"name": "go"}}}
	err := s.Match(map[string]any{"name": "rust"})
	if err == nil {
		t.Fatal("expected mismatch error")
	}
}

func TestMatchShape_SentinelStringNonEmpty(t *testing.T) {
	s := &Scenario{Expect: Expect{Match: "shape", Shape: map[string]any{"name": "<string,non-empty>"}}}
	if err := s.Match(map[string]any{"name": "x"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Match(map[string]any{"name": ""}); err == nil {
		t.Fatal("expected empty-string failure")
	}
	if err := s.Match(map[string]any{"name": float64(5)}); err == nil {
		t.Fatal("expected wrong-type failure")
	}
}

func TestMatchShape_SentinelInt(t *testing.T) {
	s := &Scenario{Expect: Expect{Match: "shape", Shape: map[string]any{"n": "<int>"}}}
	if err := s.Match(map[string]any{"n": float64(3)}); err != nil {
		t.Fatalf("expected int to pass, got %v", err)
	}
	if err := s.Match(map[string]any{"n": float64(3.5)}); err == nil {
		t.Fatal("expected non-integer to fail")
	}
}

func TestMatchShape_MissingKey(t *testing.T) {
	s := &Scenario{Expect: Expect{Match: "shape", Shape: map[string]any{"required": "<any>"}}}
	if err := s.Match(map[string]any{}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("expected missing-key error, got %v", err)
	}
}

func TestMatchShape_Regex(t *testing.T) {
	s := &Scenario{Expect: Expect{Match: "shape", Shape: map[string]any{"version": `<regex,^\d+\.\d+\.\d+$>`}}}
	if err := s.Match(map[string]any{"version": "1.2.3"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Match(map[string]any{"version": "garbage"}); err == nil {
		t.Fatal("expected regex mismatch")
	}
}

func TestMatchExact(t *testing.T) {
	s := &Scenario{Expect: Expect{Match: "exact", Exact: map[string]any{"a": float64(1)}}}
	if err := s.Match(map[string]any{"a": float64(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.Match(map[string]any{"a": float64(2)}); err == nil {
		t.Fatal("expected exact mismatch")
	}
}
