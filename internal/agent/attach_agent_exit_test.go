package agent

import (
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"
)

// Regression coverage for the detach half of mg-9b5b.
//
// Reported symptom: "whenever I'm attached to an agent for a long time, when I
// come back I'm detached." The trigger is not a timeout and not a PTY error —
// it is the agent going away underneath a live attach. Cleanup closes the PTY
// master and retires the listener but used to leave established attach
// connections open, so:
//
//  1. the client kept a socket to a dead agent, showing a frozen screen, with
//     no indication anything had happened; and
//  2. the *next* byte the client sent — a keystroke, or the `\x1b[I` a terminal
//     emits on window refocus when the agent armed focus reporting — hit
//     master.Write on a closed fd, failed, and dropped the connection right
//     then. Come back, touch the terminal, get detached.
//
// Both were observed directly on the pre-fix code. Crew agents respawn and
// polecats are stopped when their merge lands, so the longer an attach runs the
// likelier the agent under it has already been replaced.

// TestAttachConnClosesWhenAgentExits pins the fix: the attach connection must
// close when the agent process exits, without the client having to send
// anything. Before the fix this test hangs until the read deadline and fails.
func TestAttachConnClosesWhenAgentExits(t *testing.T) {
	a := spawnAgent(t, "attach-exit-closes", "cat")
	conn := dialFramed(t, a, 80, 24)
	defer conn.Close()

	// Drain the scrollback replay so the read below sees the close, not data.
	drained := make(chan struct{})
	go func() {
		defer close(drained)
		buf := make([]byte, 4096)
		deadline := time.Now().Add(500 * time.Millisecond)
		for time.Now().Before(deadline) {
			conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()
	<-drained

	if err := a.cmd.Process.Kill(); err != nil {
		t.Fatalf("kill agent: %v", err)
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("agent never exited")
	}

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	buf := make([]byte, 64)
	for {
		n, err := conn.Read(buf)
		if err != nil {
			if os.IsTimeout(err) {
				t.Fatal("attach connection still open 3s after the agent exited — " +
					"the user stays 'attached' to a dead agent until their next keystroke (mg-9b5b)")
			}
			if !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) {
				t.Logf("attach connection closed with %v (acceptable — the point is that it closed)", err)
			}
			return // closed: the client unwinds and restores the terminal
		}
		if n == 0 {
			t.Fatal("read returned 0 bytes with no error")
		}
		// Late PTY output can still be draining; keep reading until close.
	}
}

// TestAttachConnStaysOpenWhileAgentLives is the counterweight: the close must be
// tied to the agent's exit, not fire on its own. A regression that closed attach
// connections eagerly would detach every user immediately.
func TestAttachConnStaysOpenWhileAgentLives(t *testing.T) {
	a := spawnAgent(t, "attach-exit-stays", "cat")
	conn := dialFramed(t, a, 80, 24)
	defer conn.Close()

	if _, err := conn.Write(dataFrame([]byte("still here\n"))); err != nil {
		t.Fatalf("write: %v", err)
	}

	// `cat` echoes through the PTY; seeing it back proves the conn is live.
	conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	buf := make([]byte, 4096)
	var seen []byte
	for !containsBytes(seen, []byte("still here")) {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("attach connection dropped while the agent was alive: %v", err)
		}
		seen = append(seen, buf[:n]...)
	}
}

func containsBytes(haystack, needle []byte) bool {
	if len(needle) == 0 {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if string(haystack[i:i+len(needle)]) == string(needle) {
			return true
		}
	}
	return false
}
