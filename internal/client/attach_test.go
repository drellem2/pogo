package client

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// TestSplitDetach is the regression guard for the detach fix. Before it,
// AttachAgent put the terminal in raw mode (clearing ISIG) but never scanned
// stdin for the documented Ctrl-\ escape, so the 0x1c byte was wrapped in a
// data frame and injected into the agent's PTY — there was no way to detach.
// splitDetach must forward the bytes before the escape, report detach, and
// drop the escape byte (and anything after it in the same read).
func TestSplitDetach(t *testing.T) {
	for _, tc := range []struct {
		name        string
		chunk       []byte
		wantForward []byte
		wantDetach  bool
	}{
		{"no escape", []byte("ls -la\r"), []byte("ls -la\r"), false},
		{"escape only", []byte{detachByte}, []byte{}, true},
		{"bytes then escape", []byte{'a', 'b', detachByte}, []byte{'a', 'b'}, true},
		{"escape then bytes dropped", []byte{detachByte, 'x', 'y'}, []byte{}, true},
		{"bytes escape bytes", []byte{'h', 'i', detachByte, 'z'}, []byte{'h', 'i'}, true},
		{"empty", []byte{}, []byte{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			forward, detach := splitDetach(tc.chunk)
			if detach != tc.wantDetach {
				t.Errorf("detach = %v, want %v", detach, tc.wantDetach)
			}
			if !bytes.Equal(forward, tc.wantForward) {
				t.Errorf("forward = %q, want %q", forward, tc.wantForward)
			}
		})
	}
}

// TestDetachByteIsCtrlBackslash pins the escape keystroke to Ctrl-\ (0x1c),
// matching the value the attach command's help text advertises. A drift here
// would silently make the documented detach key stop working.
func TestDetachByteIsCtrlBackslash(t *testing.T) {
	if detachByte != 0x1c {
		t.Errorf("detachByte = 0x%02x, want 0x1c (Ctrl-\\)", detachByte)
	}
}

// readN reads exactly n bytes from conn with a deadline, failing the test on
// error. net.Pipe writes block until read, so the caller drives reads while
// the frame writer runs in a goroutine.
func readN(t *testing.T, conn net.Conn, n int) []byte {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, n)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("readN(%d): %v", n, err)
	}
	return buf
}

// TestSendHandshakeFrameAlwaysFramed is the client half of the mg-8772 S2
// regression guard. The connect-time handshake frame is what selects framed
// mode on the server; sendHandshakeFrame must therefore *always* emit a
// 5-byte FrameTypeResize frame — including when the terminal size is unknown
// (0×0) or out of range. The previous code skipped the frame on a bad size,
// which let the server fall back to legacy raw mode and stream data-frame
// headers into the agent's PTY as keystrokes.
func TestSendHandshakeFrameAlwaysFramed(t *testing.T) {
	cases := []struct {
		name               string
		cols, rows         int
		wantCols, wantRows uint16
	}{
		{"normal", 120, 40, 120, 40},
		{"size unknown (0×0)", 0, 0, 0, 0},
		{"negative clamps to 0", -1, -5, 0, 0},
		{"over-range clamps to 0", 70000, 80000, 0, 0},
		{"one bad dimension", 100, 99999, 100, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			var mu sync.Mutex
			errCh := make(chan error, 1)
			go func() { errCh <- sendHandshakeFrame(client, &mu, tc.cols, tc.rows) }()

			frame := readN(t, server, 5)
			if err := <-errCh; err != nil {
				t.Fatalf("sendHandshakeFrame: %v", err)
			}

			if frame[0] != agent.FrameTypeResize {
				t.Errorf("handshake frame type = 0x%02x, want FrameTypeResize 0x%02x", frame[0], agent.FrameTypeResize)
			}
			if gotCols := binary.LittleEndian.Uint16(frame[1:3]); gotCols != tc.wantCols {
				t.Errorf("handshake cols = %d, want %d", gotCols, tc.wantCols)
			}
			if gotRows := binary.LittleEndian.Uint16(frame[3:5]); gotRows != tc.wantRows {
				t.Errorf("handshake rows = %d, want %d", gotRows, tc.wantRows)
			}
		})
	}
}

// TestSendResizeFrameDropsInvalid verifies the mid-session SIGWINCH path still
// drops sizes there is nothing useful to forward for — unlike the handshake,
// it has no framed-mode state left to establish, so a no-op is correct.
func TestSendResizeFrameDropsInvalid(t *testing.T) {
	for _, tc := range []struct {
		name       string
		cols, rows int
	}{
		{"zero", 0, 0},
		{"negative", -1, 40},
		{"over-range", 70000, 40},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			defer client.Close()
			defer server.Close()

			var mu sync.Mutex
			errCh := make(chan error, 1)
			go func() {
				errCh <- sendResizeFrame(client, &mu, tc.cols, tc.rows)
				client.Close() // unblock any reader: nothing should have been sent
			}()

			server.SetReadDeadline(time.Now().Add(time.Second))
			n, _ := server.Read(make([]byte, 8))
			if n != 0 {
				t.Errorf("sendResizeFrame wrote %d bytes for invalid size %dx%d; want 0", n, tc.cols, tc.rows)
			}
			if err := <-errCh; err != nil {
				t.Errorf("sendResizeFrame returned error: %v", err)
			}
		})
	}
}

// TestSendResizeFrameValid verifies a valid mid-session resize is forwarded as
// a 5-byte FrameTypeResize frame.
func TestSendResizeFrameValid(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	var mu sync.Mutex
	errCh := make(chan error, 1)
	go func() { errCh <- sendResizeFrame(client, &mu, 90, 25) }()

	frame := readN(t, server, 5)
	if err := <-errCh; err != nil {
		t.Fatalf("sendResizeFrame: %v", err)
	}
	if frame[0] != agent.FrameTypeResize {
		t.Errorf("frame type = 0x%02x, want 0x%02x", frame[0], agent.FrameTypeResize)
	}
	if c := binary.LittleEndian.Uint16(frame[1:3]); c != 90 {
		t.Errorf("cols = %d, want 90", c)
	}
	if r := binary.LittleEndian.Uint16(frame[3:5]); r != 25 {
		t.Errorf("rows = %d, want 25", r)
	}
}

// TestSendDataFrameWrapsPayload verifies stdin bytes are wrapped in a
// FrameTypeData frame (type + u16 LE length + payload) — the framing the
// server unwraps so payload bytes, and only payload bytes, reach the PTY.
func TestSendDataFrameWrapsPayload(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	payload := []byte("hello\r")
	var mu sync.Mutex
	errCh := make(chan error, 1)
	go func() { errCh <- sendDataFrame(client, &mu, payload) }()

	hdr := readN(t, server, 3)
	if hdr[0] != agent.FrameTypeData {
		t.Errorf("frame type = 0x%02x, want FrameTypeData 0x%02x", hdr[0], agent.FrameTypeData)
	}
	gotLen := binary.LittleEndian.Uint16(hdr[1:3])
	if int(gotLen) != len(payload) {
		t.Fatalf("frame length = %d, want %d", gotLen, len(payload))
	}
	body := readN(t, server, int(gotLen))
	if err := <-errCh; err != nil {
		t.Fatalf("sendDataFrame: %v", err)
	}
	if string(body) != string(payload) {
		t.Errorf("frame payload = %q, want %q", body, payload)
	}
}
