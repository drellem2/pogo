package agent

import (
	"log"
	"os"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

// Writing a nudge body so the harness reads it as typing (mg-8a70).
//
// One write() to the PTY master does not arrive as one read() on the other
// side. The tty input queue on darwin holds 1022 bytes (TTYHOG-2): a longer
// write fills it, blocks until the reader drains it, and the reader sees
// [1022, rest]. Measured with a raw-mode reader: 1143 bytes arrived as reads of
// [1022, 121]; 3000 as [1022, 1022, 956]; 1022 as [1022].
//
// Claude Code 2.1.283 decides "typed or pasted" per read. A read of ~900 bytes
// or more becomes a paste; a paste followed at once by more input loses the
// paste at submit. So pogo's 1143-byte scheduler fires were submitted as their
// last 121 characters — reproduced 3 of 3 against the real binary, matching
// the 121-character remnants in 91 polecat transcripts.
//
// Timed pauses between pieces are not enough: pieces written faster than the
// harness reads them coalesce in the same queue (measured: 256-byte pieces
// written back to back were truncated or wrapped as a paste). So each piece
// waits until the queue is EMPTY — the harness has read everything before it —
// observed with FIONREAD on the slave fd pogod already holds (see startPTY).
// That held 8 of 8 against the real binary, including with the harness
// SIGSTOPped for a second mid-message.

// inputDrainBudget bounds the total time one nudge body may spend waiting for
// the harness to read what was written. A harness that stops reading its
// input for longer than this gets the rest written without waiting, which is
// the behaviour before mg-8a70 — delivery is not withheld on a guess.
const inputDrainBudget = 10 * time.Second

// inputDrainPoll is how often the drain wait re-reads the queue length.
const inputDrainPoll = 2 * time.Millisecond

// ttyInputPending returns how many bytes sit in the tty's input queue, written
// by pogod and not yet read by the harness.
func ttyInputPending(slave *os.File) (int, error) {
	rc, err := slave.SyscallConn()
	if err != nil {
		return 0, err
	}
	var n int
	var ierr error
	if err := rc.Control(func(fd uintptr) {
		n, ierr = unix.IoctlGetInt(int(fd), ioctlInputPending)
	}); err != nil {
		return 0, err
	}
	return n, ierr
}

// waitInputDrained blocks until the harness has read everything written to its
// tty, the deadline passes, or the queue cannot be read. It reports whether the
// queue was observed empty. Caller holds a.mu (so a.slave cannot be closed
// underneath it).
func (a *Agent) waitInputDrained(deadline time.Time) bool {
	for {
		n, err := ttyInputPending(a.slave)
		if err != nil {
			return false
		}
		if n == 0 {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		select {
		case <-a.done:
			return false
		case <-time.After(inputDrainPoll):
		}
	}
}

// splitInputChunks cuts s into pieces of at most max bytes, never inside a
// UTF-8 sequence. A piece is never empty: a rune longer than max (impossible
// for max >= 4) is emitted whole.
func splitInputChunks(s string, max int) []string {
	if max <= 0 || len(s) <= max {
		return []string{s}
	}
	var out []string
	for len(s) > max {
		cut := max
		for cut > 0 && !utf8.RuneStart(s[cut]) {
			cut--
		}
		if cut == 0 {
			_, size := utf8.DecodeRuneInString(s)
			cut = size
		}
		out = append(out, s[:cut])
		s = s[cut:]
	}
	if s != "" {
		out = append(out, s)
	}
	return out
}

// writeNudgeBody writes a nudge body to the PTY master. Caller holds a.mu.
//
// A body within the provider's InputChunkBytes (or any body, when the provider
// sets none, or when there is no slave fd to observe) is one write, exactly as
// before. A longer one is written in pieces, each after the queue has drained,
// and the call returns only once the last piece has been read too — so the
// submit terminator that follows cannot share a read with the body.
func (a *Agent) writeNudgeBody(message string) error {
	chunks := splitInputChunks(message, a.nudge.InputChunkBytes)
	if len(chunks) == 1 || a.slave == nil {
		_, err := a.master.WriteString(message)
		return err
	}
	deadline := time.Now().Add(inputDrainBudget)
	warned := false
	for i, c := range chunks {
		if i > 0 && !a.waitInputDrained(deadline) && !warned {
			warned = true
			log.Printf("agent %s: harness did not read its input within %s; writing the rest "+
				"of a %d-byte nudge without waiting (it may arrive as a paste)",
				a.Name, inputDrainBudget, len(message))
		}
		if _, err := a.master.WriteString(c); err != nil {
			return err
		}
	}
	a.waitInputDrained(deadline)
	return nil
}
