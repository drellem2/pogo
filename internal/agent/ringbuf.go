package agent

import "sync"

// RingBuffer is a fixed-size circular buffer for agent output.
// Thread-safe. When full, old data is overwritten.
type RingBuffer struct {
	mu   sync.Mutex
	buf  []byte
	pos  int
	full bool
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
