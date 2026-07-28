package client

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"

	"github.com/drellem2/pogo/internal/agent"
)

// detachByte is the escape keystroke that ends an attach session: Ctrl-\
// (ASCII FS, 0x1c). Because AttachAgent puts the terminal in raw mode, the
// kernel's ISIG processing is disabled, so Ctrl-\ no longer raises SIGQUIT —
// the byte is delivered to us like any other keystroke. We therefore detect it
// in the stdin stream and unwind the attach, rather than relying on a signal
// (which never fires) or forwarding the byte to the agent (which would inject
// a stray Ctrl-\ into the agent's TUI and leave the user stuck attached).
const detachByte = 0x1c

// attachIO is the terminal AttachAgent drives. Production wiring is the
// process's real stdin/stdout plus golang.org/x/term; tests substitute pipes
// and stubs so the attach path — including the detach-time terminal restore —
// can be exercised end to end without a controlling tty.
type attachIO struct {
	in      *os.File
	out     io.Writer
	makeRaw func(fd int) (*term.State, error)
	restore func(fd int, st *term.State) error
	getSize func(fd int) (cols, rows int, err error)
}

func stdAttachIO() attachIO {
	return attachIO{
		in:      os.Stdin,
		out:     os.Stdout,
		makeRaw: term.MakeRaw,
		restore: term.Restore,
		getSize: term.GetSize,
	}
}

// AttachAgent connects the current terminal to a running agent's PTY
// via its unix domain socket. Returns when the connection closes or
// the user presses the detach key (Ctrl-\); see detachByte.
//
// Wire protocol (client → server) is documented in
// internal/agent/attach_proto.go. Briefly: a mandatory leading 5-byte resize
// frame negotiates the initial PTY size *and* puts the server into framed
// mode, after which input bytes are wrapped in data frames and
// SIGWINCH-triggered resizes ride the same channel.
func AttachAgent(socketPath string) error {
	return attachAgent(socketPath, stdAttachIO())
}

func attachAgent(socketPath string, tio attachIO) error {
	conn, err := net.Dial("unix", socketPath)
	if err != nil {
		return fmt.Errorf("connect to agent socket: %w", err)
	}
	defer conn.Close()

	stdinFd := int(tio.in.Fd())
	oldState, err := tio.makeRaw(stdinFd)
	if err != nil {
		return fmt.Errorf("set raw mode: %w", err)
	}
	defer tio.restore(stdinFd, oldState)

	// Serialize writes to conn — stdin forwarding and SIGWINCH propagation
	// run in separate goroutines and must not interleave bytes inside a frame.
	var writeMu sync.Mutex

	// Always send the handshake resize frame first. Its leading
	// FrameTypeResize byte is what puts the server into framed mode; without
	// it the server inspects the first byte it sees, finds a data frame's
	// 0x02 type byte instead, concludes "legacy raw client" and io.Copy's the
	// entire framed stream — every 3-byte data-frame header, every length
	// field, every mid-session resize frame — straight into the agent's PTY
	// as if the user had typed it. Injecting control bytes as keystrokes
	// corrupts the target's TUI.
	//
	// When the terminal size can't be determined (e.g. stdin not a TTY) we
	// still send the frame, with 0×0 dimensions: the server treats 0×0 as
	// "size unknown — keep the current winsize", so framed mode is
	// established unambiguously and the agent keeps its spawn-time default.
	cols, rows, gerr := tio.getSize(stdinFd)
	if gerr != nil {
		cols, rows = 0, 0
	}
	if err := sendHandshakeFrame(conn, &writeMu, cols, rows); err != nil {
		return fmt.Errorf("send attach handshake: %w", err)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGWINCH)
	defer signal.Stop(sigCh)

	done := make(chan struct{}, 3)

	// SIGWINCH → resize frame. Reads current terminal size on each fire and
	// forwards it to the server so the agent's PTY mirrors the user's window.
	go func() {
		defer func() { done <- struct{}{} }()
		for range sigCh {
			cols, rows, err := tio.getSize(stdinFd)
			if err != nil {
				continue
			}
			if err := sendResizeFrame(conn, &writeMu, cols, rows); err != nil {
				return
			}
		}
	}()

	// stdin → data frames → conn. Each chunk is scanned for the detach byte
	// (Ctrl-\): bytes before it are forwarded, then the goroutine returns so
	// AttachAgent unwinds and the deferred term.Restore leaves the terminal
	// sane. The detach byte itself is consumed, never forwarded to the agent.
	go func() {
		defer func() { done <- struct{}{} }()
		buf := make([]byte, 4096)
		for {
			n, err := tio.in.Read(buf)
			if n > 0 {
				forward, detach := splitDetach(buf[:n])
				if len(forward) > 0 {
					if werr := sendDataFrame(conn, &writeMu, forward); werr != nil {
						return
					}
				}
				if detach {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()

	// conn → stdout (raw PTY output, no framing on this direction).
	//
	// The same bytes are teed into termState, which watches for the DEC private
	// modes the agent's TUI turns on (alt screen, mouse, focus reporting, …) so
	// the detach below can turn them back off. Nothing is filtered on the way
	// through: the agent's output must reach the terminal byte-for-byte or its
	// TUI renders wrong.
	tstate := newTermState()
	outDone := make(chan struct{})
	go func() {
		defer close(outDone)
		defer func() { done <- struct{}{} }()
		io.Copy(io.MultiWriter(tio.out, tstate), conn)
	}()

	<-done

	// Stop the output pump before restoring. Detach and PTY output race: bytes
	// that land after the restore sequence would re-arm the very modes we just
	// cleared, and a half-written escape sequence flushed after it would be the
	// stray control characters this fix exists to prevent. Closing the conn
	// unblocks the copy; the deferred Close is left in place for the early
	// error returns above.
	conn.Close()
	<-outDone

	// Hand the terminal back the way the agent's TUI would have on a clean
	// exit. term.Restore (deferred above) only puts termios back — it cannot
	// touch emulator-side state, which is what leaves control characters in the
	// shell prompt after a detach. See termstate.go.
	tio.out.Write(tstate.restoreSequence())
	return nil
}

// splitDetach scans a chunk of raw terminal input for the detach byte (Ctrl-\).
// It returns the bytes preceding the first detach byte — which should still be
// forwarded to the agent — and whether a detach byte was found. The detach byte
// and anything after it in the same chunk are dropped: once the user asks to
// detach, remaining keystrokes in that read are not meant for the agent.
func splitDetach(chunk []byte) (forward []byte, detach bool) {
	if i := bytes.IndexByte(chunk, detachByte); i >= 0 {
		return chunk[:i], true
	}
	return chunk, false
}

// sendHandshakeFrame writes the mandatory connect-time resize frame that puts
// the server into framed mode. Unlike sendResizeFrame it always writes a
// frame: out-of-range or unknown dimensions are clamped to 0, which the
// server reads as "keep the current winsize". Establishing framed mode is the
// point of this frame — skipping it would let the server fall back to legacy
// raw streaming and dump subsequent frame headers into the agent's PTY.
func sendHandshakeFrame(conn net.Conn, mu *sync.Mutex, cols, rows int) error {
	if cols < 0 || cols > 65535 {
		cols = 0
	}
	if rows < 0 || rows > 65535 {
		rows = 0
	}
	var frame [5]byte
	frame[0] = agent.FrameTypeResize
	binary.LittleEndian.PutUint16(frame[1:3], uint16(cols))
	binary.LittleEndian.PutUint16(frame[3:5], uint16(rows))
	mu.Lock()
	defer mu.Unlock()
	_, err := conn.Write(frame[:])
	return err
}

// sendResizeFrame writes a 5-byte resize frame (FrameTypeResize + cols u16 LE
// + rows u16 LE) to conn under mu. Cols/rows ≤ 0 or > 65535 are silently
// dropped — there is nothing useful to forward mid-session, and unlike the
// connect-time handshake there is no framed-mode state left to establish.
func sendResizeFrame(conn net.Conn, mu *sync.Mutex, cols, rows int) error {
	if cols <= 0 || rows <= 0 || cols > 65535 || rows > 65535 {
		return nil
	}
	var frame [5]byte
	frame[0] = agent.FrameTypeResize
	binary.LittleEndian.PutUint16(frame[1:3], uint16(cols))
	binary.LittleEndian.PutUint16(frame[3:5], uint16(rows))
	mu.Lock()
	defer mu.Unlock()
	_, err := conn.Write(frame[:])
	return err
}

// sendDataFrame writes one or more data frames (FrameTypeData + len u16 LE +
// payload) to conn under mu. Payloads larger than 65535 bytes are split
// across multiple frames; in practice a single keystroke is a few bytes, so
// this is defense in depth rather than a hot path.
func sendDataFrame(conn net.Conn, mu *sync.Mutex, data []byte) error {
	const maxChunk = 65535
	for len(data) > 0 {
		n := len(data)
		if n > maxChunk {
			n = maxChunk
		}
		var hdr [3]byte
		hdr[0] = agent.FrameTypeData
		binary.LittleEndian.PutUint16(hdr[1:3], uint16(n))
		mu.Lock()
		_, err := conn.Write(hdr[:])
		if err == nil {
			_, err = conn.Write(data[:n])
		}
		mu.Unlock()
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}
