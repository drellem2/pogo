package client

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/term"

	"github.com/drellem2/pogo/internal/agent"
)

// syncBuf is a concurrency-safe stdout stand-in: the attach output pump writes
// to it from its own goroutine while the test reads.
type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// attachHarness wires attachAgent to a fake terminal (an os.Pipe for stdin, a
// syncBuf for stdout, stubbed termios calls) and a unix listener standing in
// for the agent's attach socket. server is invoked once with the accepted
// connection, after the framed-mode handshake has been read off it.
type attachHarness struct {
	stdinW   *os.File
	out      *syncBuf
	restored chan struct{} // closed when term.Restore was called
	errCh    chan error    // attachAgent's return value
}

func newAttachHarness(t *testing.T, server func(conn net.Conn)) *attachHarness {
	t.Helper()

	// AF_UNIX sun_path is 104 bytes on darwin; t.TempDir() alone is too deep.
	dir, err := os.MkdirTemp("/tmp", "pogo-attach-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "a.sock")

	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	handshake := make(chan []byte, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		hs := make([]byte, 5)
		if _, err := io.ReadFull(conn, hs); err != nil {
			return
		}
		handshake <- hs
		server(conn)
	}()

	stdinR, stdinW, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	t.Cleanup(func() { stdinR.Close(); stdinW.Close() })

	h := &attachHarness{
		stdinW:   stdinW,
		out:      &syncBuf{},
		restored: make(chan struct{}),
		errCh:    make(chan error, 1),
	}

	var restoreOnce sync.Once
	tio := attachIO{
		in:      stdinR,
		out:     h.out,
		makeRaw: func(int) (*term.State, error) { return &term.State{}, nil },
		restore: func(int, *term.State) error {
			restoreOnce.Do(func() { close(h.restored) })
			return nil
		},
		// Report "size unknown" — the same thing a non-tty stdin does in
		// production, so the handshake goes out as 0×0.
		getSize: func(int) (int, int, error) { return 0, 0, os.ErrInvalid },
	}

	go func() { h.errCh <- attachAgent(sock, tio) }()

	select {
	case hs := <-handshake:
		if hs[0] != agent.FrameTypeResize {
			t.Fatalf("handshake frame type = 0x%02x, want 0x%02x", hs[0], agent.FrameTypeResize)
		}
		if c := binary.LittleEndian.Uint16(hs[1:3]); c != 0 {
			t.Fatalf("handshake cols = %d, want 0 (size unknown)", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the attach handshake")
	}
	return h
}

// waitForOutput polls the captured stdout until it contains want.
func (h *attachHarness) waitForOutput(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(h.out.String(), want) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in output; got %q", want, h.out.String())
}

func (h *attachHarness) waitDone(t *testing.T) {
	t.Helper()
	select {
	case err := <-h.errCh:
		if err != nil {
			t.Fatalf("attachAgent: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("attachAgent did not return")
	}
	select {
	case <-h.restored:
	case <-time.After(2 * time.Second):
		t.Fatal("term.Restore was never called")
	}
}

// TestAttachRestoresTerminalModesOnDetach is the end-to-end positive control
// for mg-9b5b. It drives the *real* attach path — attachAgent, its output pump,
// its detach-byte scanner — with the byte sequence a Claude Code TUI actually
// writes at startup, then detaches with Ctrl-\ and asserts the terminal the
// user gets back is clean.
//
// Before the fix this test fails: the captured stdout ends with the agent's
// output and nothing else, because term.Restore only ever put termios back and
// the alternate screen, mouse reporting, focus reporting and hidden cursor all
// stayed latched on the user's terminal.
func TestAttachRestoresTerminalModesOnDetach(t *testing.T) {
	h := newAttachHarness(t, func(conn net.Conn) {
		conn.Write([]byte(claudeCodeStartupModes))
		conn.Write([]byte("agent TUI output\r\n"))
		// Hold the connection open so the detach comes from the user, not EOF.
		io.Copy(io.Discard, conn)
	})

	h.waitForOutput(t, "agent TUI output")

	// The user presses Ctrl-\.
	if _, err := h.stdinW.Write([]byte{detachByte}); err != nil {
		t.Fatalf("write detach byte: %v", err)
	}
	h.waitDone(t)

	got := h.out.String()
	tail := got[strings.Index(got, "agent TUI output"):]
	for _, want := range []string{
		"\x1b[?1049l", // back out of the alternate screen
		"\x1b[?1000l", // mouse tracking off
		"\x1b[?1006l", // SGR mouse encoding off
		"\x1b[?1004l", // focus reporting off — the `\x1b[I` source
		"\x1b[?2026l", // synchronized output off
		"\x1b[?25h",   // cursor visible
	} {
		if !strings.Contains(tail, want) {
			t.Errorf("terminal not restored on detach: missing %q\nafter-detach output: %q", want, tail)
		}
	}
	if !strings.HasSuffix(got, "\r\n") {
		t.Errorf("output does not end with CRLF; shell prompt would land on the TUI's last line: %q", got)
	}
}

// TestAttachRestoresTerminalModesWhenAgentGoesAway covers the way mg-9b5b was
// actually reported: the user did not press anything. The agent underneath the
// attach went away (respawn, or the mayor stopping a finished polecat), the
// server closed the connection, and the attach unwound on its own. That path
// must restore the terminal too — it is the one the user is *not* watching.
func TestAttachRestoresTerminalModesWhenAgentGoesAway(t *testing.T) {
	h := newAttachHarness(t, func(conn net.Conn) {
		conn.Write([]byte(claudeCodeStartupModes))
		conn.Write([]byte("agent TUI output\r\n"))
		time.Sleep(50 * time.Millisecond)
		conn.Close() // the agent exited; pogod closes the attach conn
	})

	h.waitDone(t)

	got := h.out.String()
	for _, want := range []string{"\x1b[?1049l", "\x1b[?1004l", "\x1b[?25h"} {
		if !strings.Contains(got, want) {
			t.Errorf("terminal not restored after the agent went away: missing %q\noutput: %q", want, got)
		}
	}
}

// TestAttachDoesNotResetModesTheAgentNeverSet — end-to-end guard for the same
// property TestRestoreDoesNotClobberModesTheAgentNeverTouched pins on the
// tracker: a detach must not disable the shell's own bracketed paste.
func TestAttachDoesNotResetModesTheAgentNeverSet(t *testing.T) {
	h := newAttachHarness(t, func(conn net.Conn) {
		conn.Write([]byte("plain output, no mode changes\r\n"))
		io.Copy(io.Discard, conn)
	})
	h.waitForOutput(t, "plain output")

	h.stdinW.Write([]byte{detachByte})
	h.waitDone(t)

	if got := h.out.String(); strings.Contains(got, "2004") {
		t.Errorf("detach sent a bracketed-paste reset the agent never asked for: %q", got)
	}
}

// TestAttachForwardsInputAndSwallowsTheDetachByte re-pins the input half
// through the same harness: keystrokes before the escape reach the agent as
// data frames, and the escape itself is never forwarded.
func TestAttachForwardsInputAndSwallowsTheDetachByte(t *testing.T) {
	got := make(chan []byte, 1)
	h := newAttachHarness(t, func(conn net.Conn) {
		var seen bytes.Buffer
		buf := make([]byte, 256)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				seen.Write(buf[:n])
				if bytes.Contains(seen.Bytes(), []byte("hi")) {
					got <- append([]byte(nil), seen.Bytes()...)
					seen.Reset()
				}
			}
			if err != nil {
				return
			}
		}
	})

	h.stdinW.Write([]byte{'h', 'i', detachByte, 'z'})

	select {
	case frames := <-got:
		want := []byte{agent.FrameTypeData, 0x02, 0x00, 'h', 'i'}
		if !bytes.Equal(frames, want) {
			t.Errorf("server saw %q, want a single data frame %q (escape and trailing byte dropped)", frames, want)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server never saw the forwarded input")
	}
	h.waitDone(t)
}
