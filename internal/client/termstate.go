package client

import (
	"bytes"
	"sort"
	"sync"
)

// termState tracks the terminal-emulator modes an attached agent's PTY output
// turns on, so a detach can turn them back off.
//
// Why this exists: `term.MakeRaw`/`term.Restore` in AttachAgent only save and
// restore the *tty driver's* termios (ECHO, ICANON, ISIG, …). They do not — and
// cannot — touch the state that lives in the terminal *emulator*: the alternate
// screen buffer, mouse reporting, focus reporting, bracketed paste, cursor
// visibility, the scroll region, SGR attributes. A full-screen TUI on the far
// end of the attach turns those on by writing DEC private-mode sequences that
// pogo forwards verbatim to the user's terminal. On detach pogo used to restore
// termios and return, leaving every one of those modes latched on the user's
// terminal — so the shell prompt came back with e.g. focus reporting still
// armed, and every window focus change typed a literal `\x1b[I` / `\x1b[O` at
// it. That is the "control characters in my terminal prompt" report (mg-9b5b).
//
// Claude Code 2.1.220 — the harness pogo agents actually run — emits
// `\x1b[?1049h` (alt screen), `\x1b[?1000h` + `\x1b[?1006h` (mouse + SGR mouse
// encoding), `\x1b[?1004h` (focus reporting), `\x1b[?2026h` (synchronized
// output) and `\x1b[?25l` (hide cursor). All were observed as literals in the
// shipped binary; `?25l`/`?25h` are observable live in any running agent's
// output buffer (`pogo agent output <name>`).
//
// The tracker deliberately resets **only** what it saw the agent turn on.
// Blanket-resetting a fixed list would clobber modes the user's own shell owns:
// zsh enables bracketed paste (`\x1b[?2004h`) for itself, and an attach that
// unconditionally sent `\x1b[?2004l` on the way out would break pasting in the
// shell it just returned to.
//
// termState is an io.Writer so it can sit in the conn→stdout copy, and it is
// safe for concurrent use: the output pump writes while the detach path reads.
type termState struct {
	mu sync.Mutex

	// priv holds every DEC private mode the stream has touched, mapped to its
	// current value (true = set/`h`). Modes never mentioned are absent.
	priv map[int]bool

	// sgrDirty is true when the last SGR sequence left non-default attributes
	// (colour, bold, reverse). A detach mid-render otherwise hands the shell
	// prompt the agent's colours.
	sgrDirty bool

	// scrollRegion is true when DECSTBM narrowed the scrolling region and it
	// has not been reset. A prompt inside a leftover region scrolls wrongly.
	scrollRegion bool

	// keypadApp is true after ESC = (DECKPAM) with no matching ESC > (DECKPNM).
	// Application keypad makes the numeric keypad emit `\x1bOp`-style sequences
	// at the shell prompt.
	keypadApp bool

	// parser state
	st        parseState
	params    []byte // CSI parameter + intermediate bytes, final byte excluded
	stringEsc bool   // saw ESC inside an OSC/DCS string; a following \ ends it
}

type parseState int

const (
	stGround parseState = iota
	stEsc
	stEscInter // ESC + intermediate (charset designator); swallow one more byte
	stCSI
	stString // OSC / DCS / APC / PM / SOS body, terminated by BEL or ST
)

// privModeDefaultOn lists DEC private modes whose *default* is set, so a stream
// that turned them off must have them turned back on at detach. Everything else
// defaults to reset, and is restored with `l`.
//
// Kept deliberately short. `?1` (cursor keys), `?12` (cursor blink) and friends
// are left alone: their default is terminal- and user-configured, and forcing a
// value would be pogo overriding a preference it never saw.
var privModeDefaultOn = map[int]bool{
	7:  true, // DECAWM — autowrap. Off leaves long prompt lines overprinting.
	25: true, // DECTCEM — cursor visible. Off leaves an invisible shell cursor.
}

// altScreenMode is the xterm alternate-screen-buffer mode. It is reset last so
// the resets that precede it are not painted onto a buffer that is about to be
// discarded, and so the user lands back on the scrollback they left.
const altScreenMode = 1049

func newTermState() *termState {
	return &termState{priv: make(map[int]bool)}
}

// Write feeds PTY output bytes through the parser. It never errors and never
// modifies p, so it is safe as one arm of an io.MultiWriter alongside stdout.
//
// The parser is streaming: escape sequences split across reads (a 4 KiB PTY
// chunk boundary can land anywhere, including between the `?` and the `1049h`)
// are tracked across calls. That matters — a mode whose sequence straddles a
// chunk boundary is exactly the one a naive bytes.Contains scan would miss and
// then fail to reset.
func (t *termState) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, b := range p {
		t.step(b)
	}
	return len(p), nil
}

func (t *termState) step(b byte) {
	// CAN and SUB abort any sequence in progress, from any state.
	if b == 0x18 || b == 0x1a {
		t.st = stGround
		t.params = t.params[:0]
		return
	}

	switch t.st {
	case stGround:
		if b == 0x1b {
			t.st = stEsc
		}

	case stEsc:
		switch {
		case b == '[':
			t.st = stCSI
			t.params = t.params[:0]
		case b == ']' || b == 'P' || b == '_' || b == '^' || b == 'X':
			t.st = stString
			t.stringEsc = false
		case b == '=':
			t.keypadApp = true
			t.st = stGround
		case b == '>':
			t.keypadApp = false
			t.st = stGround
		case b == 'c':
			// RIS — the terminal is back at power-on defaults, so there is
			// nothing left for us to restore.
			t.reset()
			t.st = stGround
		case b >= 0x20 && b <= 0x2f:
			t.st = stEscInter
		case b == 0x1b:
			// ESC ESC — stay in escape, the second one starts the sequence.
		default:
			t.st = stGround
		}

	case stEscInter:
		t.st = stGround

	case stCSI:
		switch {
		case b >= 0x30 && b <= 0x3f, b >= 0x20 && b <= 0x2f:
			// Parameter bytes (digits, ';', and the private markers ? < = >)
			// and intermediates. Bound the buffer so a malformed stream cannot
			// grow it without limit.
			if len(t.params) < 256 {
				t.params = append(t.params, b)
			}
		case b >= 0x40 && b <= 0x7e:
			t.finishCSI(b)
			t.st = stGround
			t.params = t.params[:0]
		default:
			// A C0 control inside a CSI is executed by the terminal and the
			// sequence continues; nothing here depends on it.
		}

	case stString:
		switch {
		case b == 0x07: // BEL
			t.st = stGround
		case t.stringEsc && b == '\\': // ST
			t.st = stGround
			t.stringEsc = false
		case b == 0x1b:
			t.stringEsc = true
		default:
			t.stringEsc = false
		}
	}
}

func (t *termState) finishCSI(final byte) {
	params := t.params
	private := len(params) > 0 && params[0] == '?'
	if private {
		params = params[1:]
	}

	switch {
	case private && (final == 'h' || final == 'l'):
		for _, n := range parseParams(params) {
			t.priv[n] = final == 'h'
		}
	case !private && final == 'm':
		// SGR. Empty or all-zero params is a full attribute reset.
		nums := parseParams(params)
		allZero := true
		for _, n := range nums {
			if n != 0 {
				allZero = false
				break
			}
		}
		t.sgrDirty = !(len(nums) == 0 || allZero)
	case !private && final == 'r':
		// DECSTBM. A bare `\x1b[r` restores the full-height region.
		t.scrollRegion = len(parseParams(params)) > 0
	}
}

// parseParams splits a CSI parameter string ("1000;1006", "", "0") into its
// numeric values. Empty parameters are reported as 0, matching how terminals
// treat an omitted parameter, and sub-parameters (`:`) are ignored.
func parseParams(p []byte) []int {
	if len(p) == 0 {
		return nil
	}
	var out []int
	for _, field := range bytes.Split(p, []byte{';'}) {
		if i := bytes.IndexByte(field, ':'); i >= 0 {
			field = field[:i]
		}
		n := 0
		for _, c := range field {
			if c < '0' || c > '9' {
				n = 0
				break
			}
			n = n*10 + int(c-'0')
			if n > 1<<20 {
				break
			}
		}
		out = append(out, n)
	}
	return out
}

func (t *termState) reset() {
	t.priv = make(map[int]bool)
	t.sgrDirty = false
	t.scrollRegion = false
	t.keypadApp = false
}

// restoreSequence returns the bytes that put the terminal back to the state it
// would have been in had the agent's TUI exited cleanly: every mode the stream
// turned on (and left on) turned back off, every default-on mode it turned off
// turned back on, attributes and scroll region reset, and a CR/LF so the shell
// prompt does not land on top of the last line the TUI drew.
//
// It is safe to call while the output pump is still running, but the caller
// should stop the pump first — otherwise a few more PTY bytes can arrive after
// the restore and re-dirty the terminal.
func (t *termState) restoreSequence() []byte {
	t.mu.Lock()
	defer t.mu.Unlock()

	var buf bytes.Buffer

	// Deterministic order — a map range would make the output untestable.
	modes := make([]int, 0, len(t.priv))
	for n := range t.priv {
		modes = append(modes, n)
	}
	sort.Ints(modes)

	emit := func(n int) {
		on := t.priv[n]
		def := privModeDefaultOn[n]
		if on == def {
			return // already at its default — nothing to undo
		}
		buf.WriteString("\x1b[?")
		buf.WriteString(itoa(n))
		if def {
			buf.WriteByte('h')
		} else {
			buf.WriteByte('l')
		}
	}

	for _, n := range modes {
		// The cursor and the alternate screen are handled after the buffer
		// switch, so the visible screen ends up correct.
		if n == altScreenMode || n == 25 {
			continue
		}
		emit(n)
	}
	if t.scrollRegion {
		buf.WriteString("\x1b[r")
	}
	if t.keypadApp {
		buf.WriteString("\x1b>")
	}
	if _, seen := t.priv[altScreenMode]; seen {
		emit(altScreenMode)
	}
	if _, seen := t.priv[25]; seen {
		emit(25)
	}
	if t.sgrDirty {
		buf.WriteString("\x1b[0m")
	}
	buf.WriteString("\r\n")
	return buf.Bytes()
}

// itoa avoids pulling strconv in for a handful of small non-negative ints.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var d [8]byte
	i := len(d)
	for n > 0 && i > 0 {
		i--
		d[i] = byte('0' + n%10)
		n /= 10
	}
	return string(d[i:])
}
