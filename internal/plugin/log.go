package plugin

import "sync"

// DefaultLogRingSize is the default capacity of each plugin's stderr
// ring buffer (in lines). Chosen to comfortably hold a typical stack
// trace plus surrounding context without bounding plugin authors on
// what they can debug-log per run.
const DefaultLogRingSize = 200

// logBuffer is a fixed-capacity ring of tagged stderr lines. Newer
// lines overwrite older ones when the ring is full. It is safe for
// concurrent use: one writer goroutine (the stderr pump) plus any
// number of readers (Manager.Logs, error wrapping).
type logBuffer struct {
	mu   sync.Mutex
	ring []string
	head int // index of next write slot
	full bool
}

// newLogBuffer returns a ring with the given capacity. A capacity <= 0
// yields DefaultLogRingSize.
func newLogBuffer(capacity int) *logBuffer {
	if capacity <= 0 {
		capacity = DefaultLogRingSize
	}
	return &logBuffer{ring: make([]string, capacity)}
}

// Append stores a line. When the ring is full, the oldest line is
// overwritten.
func (b *logBuffer) Append(line string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.ring[b.head] = line
	b.head = (b.head + 1) % len(b.ring)
	if b.head == 0 {
		b.full = true
	}
}

// Snapshot returns a fresh slice containing the buffered lines in order
// (oldest first). Returns an empty slice if nothing has been written.
func (b *logBuffer) Snapshot() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	if !b.full {
		out := make([]string, b.head)
		copy(out, b.ring[:b.head])
		return out
	}
	out := make([]string, 0, len(b.ring))
	out = append(out, b.ring[b.head:]...)
	out = append(out, b.ring[:b.head]...)
	return out
}

// Len returns the number of lines currently held.
func (b *logBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.full {
		return len(b.ring)
	}
	return b.head
}
