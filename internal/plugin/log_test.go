package plugin

import (
	"reflect"
	"sync"
	"testing"
)

func TestLogBuffer_EmptySnapshot(t *testing.T) {
	b := newLogBuffer(4)
	if snap := b.Snapshot(); len(snap) != 0 {
		t.Errorf("expected empty snapshot, got %v", snap)
	}
	if b.Len() != 0 {
		t.Errorf("Len() = %d, want 0", b.Len())
	}
}

func TestLogBuffer_UnderCapacity(t *testing.T) {
	b := newLogBuffer(4)
	b.Append("a")
	b.Append("b")
	b.Append("c")
	got := b.Snapshot()
	want := []string{"a", "b", "c"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestLogBuffer_WrapAround(t *testing.T) {
	b := newLogBuffer(3)
	b.Append("a")
	b.Append("b")
	b.Append("c")
	b.Append("d")
	b.Append("e")
	got := b.Snapshot()
	want := []string{"c", "d", "e"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if b.Len() != 3 {
		t.Errorf("Len() = %d, want 3", b.Len())
	}
}

func TestLogBuffer_DefaultCapacity(t *testing.T) {
	b := newLogBuffer(0)
	if cap(b.ring) != DefaultLogRingSize {
		t.Errorf("cap = %d, want %d", cap(b.ring), DefaultLogRingSize)
	}
}

func TestLogBuffer_ConcurrentAppend(t *testing.T) {
	b := newLogBuffer(1000)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				b.Append("x")
			}
		}()
	}
	wg.Wait()
	if got := b.Len(); got != 1000 {
		t.Errorf("expected 1000 entries (ring should wrap), got %d", got)
	}
}
