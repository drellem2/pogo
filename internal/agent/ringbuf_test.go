package agent

import (
	"testing"
	"time"
)

func TestRingBufferBasic(t *testing.T) {
	rb := NewRingBuffer(10)

	rb.Write([]byte("hello"))
	got := string(rb.Last(10))
	if got != "hello" {
		t.Errorf("Last(10) = %q, want %q", got, "hello")
	}

	if rb.Len() != 5 {
		t.Errorf("Len() = %d, want 5", rb.Len())
	}
}

func TestRingBufferWrap(t *testing.T) {
	rb := NewRingBuffer(8)

	rb.Write([]byte("abcde")) // pos=5, not full
	rb.Write([]byte("fghij")) // wraps: pos=2, full=true
	got := string(rb.Last(8))
	if got != "cdefghij" {
		t.Errorf("Last(8) = %q, want %q", got, "cdefghij")
	}
}

func TestRingBufferOverflow(t *testing.T) {
	rb := NewRingBuffer(4)

	rb.Write([]byte("abcdefgh")) // data larger than buffer
	got := string(rb.Last(4))
	if got != "efgh" {
		t.Errorf("Last(4) = %q, want %q", got, "efgh")
	}
}

func TestRingBufferLastPartial(t *testing.T) {
	rb := NewRingBuffer(10)

	rb.Write([]byte("hello world!")) // wraps around
	got := string(rb.Last(5))
	if got != "orld!" {
		t.Errorf("Last(5) = %q, want %q", got, "orld!")
	}
}

func TestRingBufferEmpty(t *testing.T) {
	rb := NewRingBuffer(10)

	got := rb.Last(5)
	if got != nil {
		t.Errorf("Last(5) on empty = %q, want nil", got)
	}

	if rb.Len() != 0 {
		t.Errorf("Len() on empty = %d, want 0", rb.Len())
	}
}

// TestRingBufferLastWriteTimeInvariant guards the invariant that idle
// detection depends on (see Agent.IsIdle): only Write advances lastWrite —
// reads (Last, Len, LastWriteTime) must never mutate it. If a read bumped
// lastWrite, every poll of WaitIdle would reset the idle window and the
// agent would never be seen as idle. This is the ring-buffer half of the
// mg-8772 S1 (nudge-timeout) regression coverage.
func TestRingBufferLastWriteTimeInvariant(t *testing.T) {
	rb := NewRingBuffer(64)

	rb.Write([]byte("hello world"))
	afterWrite := rb.LastWriteTime()
	if afterWrite.IsZero() {
		t.Fatal("LastWriteTime zero after a write")
	}

	time.Sleep(10 * time.Millisecond)

	// Reads must not move lastWrite, no matter how many times they run.
	for i := 0; i < 5; i++ {
		rb.Last(11)
		rb.Last(4)
		rb.Len()
		rb.LastWriteTime()
	}
	if got := rb.LastWriteTime(); !got.Equal(afterWrite) {
		t.Errorf("reads mutated lastWrite: %v != %v", got, afterWrite)
	}

	// A subsequent write must advance it.
	time.Sleep(10 * time.Millisecond)
	rb.Write([]byte("more"))
	if got := rb.LastWriteTime(); !got.After(afterWrite) {
		t.Errorf("write did not advance lastWrite: %v not after %v", got, afterWrite)
	}
}
