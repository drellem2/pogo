package agent

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/creack/pty"
)

// shortSocketDir returns a tempdir with a path short enough to fit inside
// AF_UNIX's sun_path limit (≈104 bytes on macOS). t.TempDir() on darwin
// returns paths under /var/folders/... which routinely exceed that.
func shortSocketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pogo-attach-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

// waitForPTYSize polls pty.Getsize on master until cols/rows match the wanted
// values or the deadline elapses. Returns the last observed size on timeout.
// PTY resize is a kernel ioctl, so the change isn't always visible
// immediately after the server-side write returns.
func waitForPTYSize(t *testing.T, a *Agent, wantCols, wantRows uint16, timeout time.Duration) (uint16, uint16) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var cols, rows uint16
	for {
		a.mu.Lock()
		master := a.master
		a.mu.Unlock()
		if master != nil {
			ws, err := pty.GetsizeFull(master)
			if err == nil {
				cols, rows = ws.Cols, ws.Rows
				if cols == wantCols && rows == wantRows {
					return cols, rows
				}
			}
		}
		if time.Now().After(deadline) {
			return cols, rows
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestSpawnSetsDefaultWinsize verifies that a freshly spawned agent's PTY
// has a non-zero, non-degenerate winsize. Without the fix, pty.Start defaults
// to 0×0 and TUI clients (Ink, etc.) silently fall back to 80×24, which
// renders mis-aligned once a real terminal of any other size attaches.
func TestSpawnSetsDefaultWinsize(t *testing.T) {
	socketDir := shortSocketDir(t)
	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "winsize-default",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	a.mu.Lock()
	master := a.master
	a.mu.Unlock()
	if master == nil {
		t.Fatal("master is nil")
	}

	ws, err := pty.GetsizeFull(master)
	if err != nil {
		t.Fatalf("GetsizeFull: %v", err)
	}
	if ws.Cols == 0 || ws.Rows == 0 {
		t.Errorf("PTY winsize = %dx%d, want non-zero (default %dx%d)",
			ws.Cols, ws.Rows, defaultPTYCols, defaultPTYRows)
	}
	if ws.Cols != defaultPTYCols || ws.Rows != defaultPTYRows {
		t.Errorf("PTY winsize = %dx%d, want %dx%d",
			ws.Cols, ws.Rows, defaultPTYCols, defaultPTYRows)
	}
}

// TestAttachResizeFrameNegotiates verifies the leading 5-byte resize frame
// on a fresh attach updates the agent's PTY to the client-reported size,
// and a second resize frame mid-stream (the SIGWINCH path) propagates too.
func TestAttachResizeFrameNegotiates(t *testing.T) {
	socketDir := shortSocketDir(t)
	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "resize-attach",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	conn, err := net.Dial("unix", a.SocketPath())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// First resize frame — handshake on connect.
	if _, err := conn.Write(resizeFrame(120, 40)); err != nil {
		t.Fatalf("write resize frame: %v", err)
	}
	if cols, rows := waitForPTYSize(t, a, 120, 40, 1*time.Second); cols != 120 || rows != 40 {
		t.Errorf("after initial resize PTY = %dx%d, want 120x40", cols, rows)
	}

	// Second resize frame — SIGWINCH-style mid-session update.
	if _, err := conn.Write(resizeFrame(80, 24)); err != nil {
		t.Fatalf("write second resize: %v", err)
	}
	if cols, rows := waitForPTYSize(t, a, 80, 24, 1*time.Second); cols != 80 || rows != 24 {
		t.Errorf("after SIGWINCH-style resize PTY = %dx%d, want 80x24", cols, rows)
	}
}

// TestAttachDataFrameForwardsInput verifies that after entering framed mode
// (via the leading resize frame) input is wrapped in data frames and reaches
// the PTY — i.e. typing still works.
func TestAttachDataFrameForwardsInput(t *testing.T) {
	socketDir := shortSocketDir(t)
	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "framed-input",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	conn, err := net.Dial("unix", a.SocketPath())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// Enter framed mode and send a data frame containing "ping\n".
	if _, err := conn.Write(resizeFrame(100, 30)); err != nil {
		t.Fatalf("write resize: %v", err)
	}
	if _, err := conn.Write(dataFrame([]byte("ping\n"))); err != nil {
		t.Fatalf("write data frame: %v", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if got := string(a.RecentOutput(1024)); strings.Contains(got, "ping") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected 'ping' in agent output, got %q", string(a.RecentOutput(1024)))
}

// TestAttachLegacyRawModeStillWorks verifies that a client which never sends
// a resize frame (legacy pre-mg-5564 binary, or `nc /tmp/agent.sock`) still
// has its raw bytes forwarded to the PTY. Acceptance criterion #4 of mg-5564.
func TestAttachLegacyRawModeStillWorks(t *testing.T) {
	socketDir := shortSocketDir(t)
	reg, err := NewRegistry(socketDir)
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "legacy-attach",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	conn, err := net.Dial("unix", a.SocketPath())
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()

	// First byte is "h" (0x68), not FrameTypeResize — server must enter
	// legacy raw mode and write the byte to the PTY.
	if _, err := conn.Write([]byte("hello\n")); err != nil {
		t.Fatalf("write raw: %v", err)
	}

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if got := string(a.RecentOutput(1024)); strings.Contains(got, "hello") {
			// Legacy mode worked; PTY size stays at the spawn default.
			a.mu.Lock()
			master := a.master
			a.mu.Unlock()
			ws, err := pty.GetsizeFull(master)
			if err != nil {
				t.Fatalf("GetsizeFull: %v", err)
			}
			if ws.Cols != defaultPTYCols || ws.Rows != defaultPTYRows {
				t.Errorf("legacy attach changed PTY to %dx%d; expected default %dx%d",
					ws.Cols, ws.Rows, defaultPTYCols, defaultPTYRows)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Errorf("expected 'hello' in agent output, got %q", string(a.RecentOutput(1024)))
}

func resizeFrame(cols, rows uint16) []byte {
	frame := make([]byte, 5)
	frame[0] = FrameTypeResize
	binary.LittleEndian.PutUint16(frame[1:3], cols)
	binary.LittleEndian.PutUint16(frame[3:5], rows)
	return frame
}

func dataFrame(payload []byte) []byte {
	frame := make([]byte, 3+len(payload))
	frame[0] = FrameTypeData
	binary.LittleEndian.PutUint16(frame[1:3], uint16(len(payload)))
	copy(frame[3:], payload)
	return frame
}
