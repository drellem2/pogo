package agent

import (
	"sync"
	"time"
)

// OutputRingBytes is the capacity of the PTY-output ring every spawned Agent
// carries — the buffer Agent.RecentOutput reads from. Both Spawn and Respawn
// size their ring from it, so it is the honest answer to "how much output can I
// still see?".
//
// It is exported so a scanner that wants ALL the output still retained can ask
// for exactly that instead of naming a smaller number and hoping no burst hides
// what it is looking for. That hope is what mg-9270 was: the trust-dialog hooks
// read 8KB out of this 64KB ring while scanning for a marker that is only on
// screen for a window, so a burst between two polls could hide the marker from
// every tick. Prefer this constant over a literal at any call site whose
// correctness depends on not missing something.
const OutputRingBytes = 64 * 1024

// RingBuffer is a fixed-size circular buffer for agent output.
// Thread-safe. When full, old data is overwritten.
type RingBuffer struct {
	mu        sync.Mutex
	buf       []byte
	pos       int
	full      bool
	lastWrite time.Time
}

// NewRingBuffer creates a ring buffer of the given size.
func NewRingBuffer(size int) *RingBuffer {
	return &RingBuffer{buf: make([]byte, size)}
}

// Write appends data to the buffer, overwriting old data if full.
func (r *RingBuffer) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	n := len(p)
	size := len(r.buf)

	r.lastWrite = time.Now()

	if n >= size {
		// Data larger than buffer — keep only the tail
		copy(r.buf, p[n-size:])
		r.pos = 0
		r.full = true
		return n, nil
	}

	// How much fits before wrap?
	space := size - r.pos
	if n <= space {
		copy(r.buf[r.pos:], p)
	} else {
		copy(r.buf[r.pos:], p[:space])
		copy(r.buf, p[space:])
	}
	r.pos = (r.pos + n) % size
	if !r.full && r.pos < n {
		// We wrapped around
		r.full = true
	}
	return n, nil
}

// LastWriteTime returns the time of the most recent write.
func (r *RingBuffer) LastWriteTime() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lastWrite
}

// Last returns the most recent n bytes of output.
// Returns fewer bytes if less than n have been written.
func (r *RingBuffer) Last(n int) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()

	var total int
	if r.full {
		total = len(r.buf)
	} else {
		total = r.pos
	}

	if n > total {
		n = total
	}
	if n == 0 {
		return nil
	}

	result := make([]byte, n)
	// Start position of the last n bytes
	start := r.pos - n
	if start < 0 {
		start += len(r.buf)
	}

	if start+n <= len(r.buf) {
		copy(result, r.buf[start:start+n])
	} else {
		firstPart := len(r.buf) - start
		copy(result, r.buf[start:])
		copy(result[firstPart:], r.buf[:n-firstPart])
	}
	return result
}

// Len returns the total number of bytes currently stored.
func (r *RingBuffer) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.full {
		return len(r.buf)
	}
	return r.pos
}
