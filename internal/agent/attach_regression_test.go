package agent

import (
	"bytes"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// Regression coverage for mg-8772: the mg-5564 PTY/attach change could corrupt
// a live agent ("S2 — attach corrupts the target"). The bugs:
//
//   - A framed client that skipped the handshake (e.g. stdin not a TTY) had
//     the server fall back to legacy raw mode and stream every data-frame
//     header into the PTY as keystrokes.
//   - applyResize called pty.Setsize unconditionally; TIOCSWINSZ raises
//     SIGWINCH even for an unchanged size, so a connect-time handshake at the
//     agent's current size still triggered a redraw — redraw bytes bump the
//     ring buffer and can starve WaitIdle (the S1 nudge-timeout symptom).
//
// These tests use `cat` (echoes whatever reaches its PTY, so leaked framing is
// directly observable) and small shell agents whose SIGWINCH / output cadence
// behaviour can be asserted.

// spawnAgent is a small helper: spawns an agent, fails the test on error, and
// registers StopAll cleanup.
func spawnAgent(t *testing.T, name string, command ...string) *Agent {
	t.Helper()
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	t.Cleanup(func() { reg.StopAll(2 * time.Second) })
	a, err := reg.Spawn(SpawnRequest{
		Name:    name,
		Type:    TypePolecat,
		Command: command,
	})
	if err != nil {
		t.Fatalf("Spawn(%s): %v", name, err)
	}
	return a
}

// dialFramed dials the agent's attach socket and completes the framed-mode
// handshake with the given winsize. cols/rows of 0 exercise the "size unknown"
// handshake a non-TTY client sends.
func dialFramed(t *testing.T, a *Agent, cols, rows uint16) net.Conn {
	t.Helper()
	conn, err := net.Dial("unix", a.SocketPath())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if _, err := conn.Write(resizeFrame(cols, rows)); err != nil {
		conn.Close()
		t.Fatalf("write handshake frame: %v", err)
	}
	return conn
}

// waitForOutput polls RecentOutput until it contains want or the deadline
// elapses. Returns the final output snapshot.
func waitForOutput(a *Agent, want string, timeout time.Duration) string {
	deadline := time.Now().Add(timeout)
	for {
		got := string(a.RecentOutput(64 * 1024))
		if strings.Contains(got, want) {
			return got
		}
		if time.Now().After(deadline) {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestAttachFramedModeDoesNotInjectFrameHeaders is the core S2 regression
// guard. In framed mode the server must unwrap data frames and write *only*
// the payload to the PTY — never the frame-type byte, the length field, or
// the handshake bytes. `cat` echoes whatever reaches its PTY, so any leaked
// framing shows up directly in its output. The handshake (0x01 + size) and
// the data-frame header (0x02 + len) both contain NUL bytes, so a NUL in the
// echoed output is a precise "raw framing leaked into the agent" signal.
func TestAttachFramedModeDoesNotInjectFrameHeaders(t *testing.T) {
	a := spawnAgent(t, "framed-no-leak", "cat")

	conn := dialFramed(t, a, 120, 40)
	defer conn.Close()

	const payload = "PAYLOAD-clean-ascii-line\n"
	if _, err := conn.Write(dataFrame([]byte(payload))); err != nil {
		t.Fatalf("write data frame: %v", err)
	}

	got := waitForOutput(a, "PAYLOAD-clean-ascii-line", 2*time.Second)
	if !strings.Contains(got, "PAYLOAD-clean-ascii-line") {
		t.Fatalf("payload never reached PTY; output=%q", got)
	}
	if i := bytes.IndexByte([]byte(got), 0x00); i >= 0 {
		t.Errorf("NUL byte at offset %d in agent output — frame header/handshake leaked into PTY: %q", i, got)
	}
	if bytes.Contains([]byte(got), []byte{FrameTypeData}) {
		t.Errorf("FrameTypeData (0x02) byte leaked into agent output: %q", got)
	}
	if bytes.Contains([]byte(got), []byte{FrameTypeResize}) {
		t.Errorf("FrameTypeResize (0x01) byte leaked into agent output: %q", got)
	}
}

// TestAttachZeroSizeHandshake covers the non-TTY client path: when the client
// can't read its terminal size it still sends the handshake frame, with 0×0
// dimensions, so framed mode is established. Data frames after a 0×0 handshake
// must still be unwrapped, and the PTY must keep its spawn-time default size
// (0×0 means "keep current winsize"). Before the fix the client skipped the
// frame entirely and the server dumped data-frame headers into the PTY.
func TestAttachZeroSizeHandshake(t *testing.T) {
	a := spawnAgent(t, "zero-handshake", "cat")

	conn := dialFramed(t, a, 0, 0)
	defer conn.Close()

	if _, err := conn.Write(dataFrame([]byte("zerosize-ok\n"))); err != nil {
		t.Fatalf("write data frame: %v", err)
	}

	got := waitForOutput(a, "zerosize-ok", 2*time.Second)
	if !strings.Contains(got, "zerosize-ok") {
		t.Fatalf("payload never reached PTY after 0×0 handshake; output=%q", got)
	}
	if i := bytes.IndexByte([]byte(got), 0x00); i >= 0 {
		t.Errorf("NUL byte at offset %d — framing leaked after 0×0 handshake: %q", i, got)
	}

	// 0×0 means "keep current size": the PTY stays at the spawn default.
	if cols, rows := waitForPTYSize(t, a, defaultPTYCols, defaultPTYRows, 500*time.Millisecond); cols != defaultPTYCols || rows != defaultPTYRows {
		t.Errorf("0×0 handshake changed PTY to %dx%d; want spawn default %dx%d",
			cols, rows, defaultPTYCols, defaultPTYRows)
	}
}

// TestAttachDoesNotStallTarget is the cadence half of the S2 regression guard.
// The agent emits a counter token on a fixed cadence regardless of input; the
// counter must keep advancing across all three phases — before, during, and
// after a framed attach with a real resize — proving the attach/resize path
// neither freezes nor kills the target.
func TestAttachDoesNotStallTarget(t *testing.T) {
	a := spawnAgent(t, "cadence", "sh", "-c",
		"i=0; while true; do i=$((i+1)); echo \"tick $i\"; sleep 0.05; done")

	tickRe := regexp.MustCompile(`tick (\d+)`)
	lastTick := func() int {
		matches := tickRe.FindAllStringSubmatch(string(a.RecentOutput(64*1024)), -1)
		max := 0
		for _, m := range matches {
			if n, err := strconv.Atoi(m[1]); err == nil && n > max {
				max = n
			}
		}
		return max
	}

	// Wait for the ticker to get going.
	deadline := time.Now().Add(2 * time.Second)
	for lastTick() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	before := lastTick()
	if before < 2 {
		t.Fatalf("ticker never started: lastTick=%d", before)
	}

	// Attach with a real resize (200×50 spawn default → 120×40).
	conn := dialFramed(t, a, 120, 40)
	time.Sleep(400 * time.Millisecond)
	during := lastTick()

	// Detach.
	conn.Close()
	time.Sleep(400 * time.Millisecond)
	after := lastTick()

	if !(before < during && during < after) {
		t.Errorf("tick cadence stalled across attach/detach: before=%d during=%d after=%d",
			before, during, after)
	}
}

// TestApplyResizeIdempotent verifies applyResize only signals the agent when
// the winsize actually changes. TIOCSWINSZ raises SIGWINCH unconditionally, so
// a naive implementation pokes the target on every connect-time handshake; a
// Claude Code TUI answers each SIGWINCH with a redraw that bumps the ring
// buffer's LastWriteTime and can starve WaitIdle (S1). The agent here prints
// "WINCH" from a SIGWINCH trap so signal delivery is directly observable.
func TestApplyResizeIdempotent(t *testing.T) {
	a := spawnAgent(t, "winch-trap", "sh", "-c",
		"trap 'printf \"WINCH\\n\"' WINCH; while true; do sleep 0.1; done")

	// Let the shell install its trap.
	time.Sleep(400 * time.Millisecond)

	countWINCH := func() int {
		return strings.Count(string(a.RecentOutput(64*1024)), "WINCH")
	}
	if c := countWINCH(); c != 0 {
		t.Fatalf("unexpected WINCH before any resize: %d", c)
	}

	// Resize to the size the PTY already has (the spawn default) → applyResize
	// must skip pty.Setsize, so no SIGWINCH reaches the agent.
	a.applyResize(defaultPTYCols, defaultPTYRows)
	time.Sleep(500 * time.Millisecond)
	if c := countWINCH(); c != 0 {
		t.Errorf("no-op applyResize signaled SIGWINCH (%d WINCH); want 0", c)
	}

	// Resize to a genuinely new size → SIGWINCH must be delivered.
	a.applyResize(100, 30)
	deadline := time.Now().Add(2 * time.Second)
	for countWINCH() < 1 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if c := countWINCH(); c < 1 {
		t.Fatalf("real applyResize did not signal SIGWINCH; want >=1, got %d", c)
	}

	// Repeat the same new size → no additional SIGWINCH.
	before := countWINCH()
	a.applyResize(100, 30)
	time.Sleep(500 * time.Millisecond)
	if c := countWINCH(); c != before {
		t.Errorf("repeated same-size applyResize signaled again: %d -> %d", before, c)
	}

	// A zero dimension is "size unknown" and must never signal.
	a.applyResize(0, 0)
	a.applyResize(80, 0)
	a.applyResize(0, 24)
	time.Sleep(300 * time.Millisecond)
	if c := countWINCH(); c != before {
		t.Errorf("zero-dimension applyResize signaled SIGWINCH: %d -> %d", before, c)
	}
}

// TestApplyResizeChangesPTYSize is a direct check that a real resize still
// takes effect (the idempotence guard must not swallow genuine changes).
func TestApplyResizeChangesPTYSize(t *testing.T) {
	a := spawnAgent(t, "resize-effective", "cat")

	a.applyResize(133, 47)
	if cols, rows := waitForPTYSize(t, a, 133, 47, 1*time.Second); cols != 133 || rows != 47 {
		t.Errorf("applyResize did not take effect: PTY = %dx%d, want 133x47", cols, rows)
	}

	// Verify against the kernel directly too.
	a.mu.Lock()
	master := a.master
	a.mu.Unlock()
	ws, err := pty.GetsizeFull(master)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if ws.Cols != 133 || ws.Rows != 47 {
		t.Errorf("kernel winsize = %dx%d, want 133x47", ws.Cols, ws.Rows)
	}
}
