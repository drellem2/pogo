package agent

import (
	"testing"
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
