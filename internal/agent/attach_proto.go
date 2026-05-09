package agent

// Wire protocol for the unix-socket attach path.
//
// On connect, a new client immediately sends a 5-byte resize frame:
//
//	+----------+----------+----------+
//	| 0x01 (1) | cols (2) | rows (2) |
//	+----------+----------+----------+
//
// The server peeks the first byte. If it is FrameTypeResize (0x01) the
// server enters framed mode and reads the remaining 4 bytes as cols/rows
// (little-endian u16), applies the resize via pty.Setsize, and continues
// reading framed messages from the client. In framed mode every byte the
// client sends is wrapped in a typed message:
//
//	resize: 0x01 + cols(u16 LE) + rows(u16 LE)              (5 bytes)
//	data:   0x02 + len(u16 LE)  + N raw bytes               (3+N bytes)
//
// If the first byte the server receives is anything else, the server falls
// back to **legacy raw mode** (`io.Copy(master, conn)`) and writes that
// already-read byte to the PTY first so input is not lost. This preserves
// compatibility with attach clients built before this change — the server
// just keeps running them at the default winsize set on Spawn/Respawn. See
// docs/pty-investigation-2026-05-09.md for the rationale.
//
// Server → client traffic is unchanged: raw PTY output bytes, no framing.
const (
	FrameTypeResize byte = 0x01
	FrameTypeData   byte = 0x02
)
