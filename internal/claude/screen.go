package claude

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// screenLines replays PTY output onto a grid and returns the text of each row,
// top to bottom. It exists because the stripped byte stream cannot say which
// dialog row Claude Code has highlighted once the highlight has MOVED.
//
// Claude Code 2.1.283 draws the trust dialog's rows once, in full:
//
//	❯ No, exit
//	  Yes, I trust this folder
//
// and an arrow key then redraws only the two glyphs that changed — a space over
// the old "❯" and a "❯" at the start of the other row — using relative cursor
// moves ("\x1b[4A \r\x1b[1C\x1b[1B❯", captured live, mg-f394). No label is
// re-emitted, so agent.StripANSI of the stream reads "…❯No,exitYes,Itrust…❯":
// the marker has moved and nothing next to it says where to. Replaying the
// cursor movement puts that "❯" back on the row it was drawn on.
//
// This is a model of the cursor, not a terminal emulator, and it only has to be
// good enough to answer "which row carries the ❯". It handles what Ink emits
// for this dialog — printable runes, CR, LF, BS, and the CSI cursor moves
// A/B/C/D/E/F/G plus K (erase in line) — and skips every other escape. It does
// not scroll: rows only accumulate downwards, which is what a relative-move
// renderer looks like once the scrolled-off top is kept instead of dropped. An
// absolute row move (CSI H) is taken relative to the first byte replayed, which
// is wrong for a full-screen renderer and irrelevant to Ink's inline one.
//
// Wide glyphs are counted as one column. That can misplace text WITHIN a row,
// never across rows, and rows are all the caller reads.
func screenLines(output []byte) []string {
	s := &screen{rows: map[int][]rune{}}
	s.replay(output)
	return s.lines()
}

type screen struct {
	rows           map[int][]rune
	row, col       int
	minRow, maxRow int
	savedRow       int
	savedCol       int
}

func (s *screen) put(r rune) {
	line := s.rows[s.row]
	for len(line) <= s.col {
		line = append(line, ' ')
	}
	line[s.col] = r
	s.rows[s.row] = line
	s.col++
	s.touch()
}

func (s *screen) touch() {
	if s.row < s.minRow {
		s.minRow = s.row
	}
	if s.row > s.maxRow {
		s.maxRow = s.row
	}
}

func (s *screen) eraseLine(mode int) {
	line := s.rows[s.row]
	switch mode {
	case 0: // cursor to end
		if s.col < len(line) {
			s.rows[s.row] = line[:s.col]
		}
	case 1: // start to cursor
		for i := 0; i <= s.col && i < len(line); i++ {
			line[i] = ' '
		}
	case 2:
		delete(s.rows, s.row)
	}
}

func (s *screen) replay(b []byte) {
	for i := 0; i < len(b); {
		c := b[i]
		switch {
		case c == '\r':
			s.col = 0
			i++
		case c == '\n':
			s.row++
			s.touch()
			i++
		case c == '\b':
			if s.col > 0 {
				s.col--
			}
			i++
		case c == 0x1b:
			i = s.escape(b, i)
		case c < 0x20 || c == 0x7f:
			i++ // other C0 controls: BEL, TAB and the rest move nothing we read
		default:
			r, n := utf8.DecodeRune(b[i:])
			if r != utf8.RuneError || n > 1 {
				s.put(r)
			}
			i += n
		}
	}
}

// escape consumes the escape sequence starting at b[i] and returns the index
// just past it.
func (s *screen) escape(b []byte, i int) int {
	if i+1 >= len(b) {
		return len(b)
	}
	switch b[i+1] {
	case '[':
		return s.csi(b, i+2)
	case ']', 'P', '_', '^':
		// OSC / DCS / APC / PM: skip to BEL or ST.
		for j := i + 2; j < len(b); j++ {
			if b[j] == 0x07 {
				return j + 1
			}
			if b[j] == 0x1b && j+1 < len(b) && b[j+1] == '\\' {
				return j + 2
			}
		}
		return len(b)
	case '7':
		s.savedRow, s.savedCol = s.row, s.col
		return i + 2
	case '8':
		s.row, s.col = s.savedRow, s.savedCol
		return i + 2
	case '(', ')', '*', '+', '#', '%':
		return i + 3 // charset designation: ESC ( B and friends
	}
	return i + 2
}

// csi consumes a CSI sequence whose parameters start at b[j] and applies the
// cursor moves this model understands.
func (s *screen) csi(b []byte, j int) int {
	start := j
	for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
		j++
	}
	if j >= len(b) {
		return len(b)
	}
	params := string(b[start:j])
	final := b[j]
	end := j + 1
	// A private or intermediate prefix (?, >, <, =, space, …) is not a
	// cursor move — "\x1b[?25l", "\x1b[>0q", "\x1b[?u".
	if params != "" && (params[0] < '0' || params[0] > ';') {
		return end
	}
	n := func(idx, def int) int {
		parts := strings.Split(params, ";")
		if idx >= len(parts) || parts[idx] == "" {
			return def
		}
		v, err := strconv.Atoi(parts[idx])
		if err != nil || v < 0 {
			return def
		}
		return v
	}
	count := n(0, 1)
	if count == 0 {
		count = 1
	}
	switch final {
	case 'A':
		s.row -= count
	case 'B':
		s.row += count
	case 'C':
		s.col += count
	case 'D':
		s.col -= count
		if s.col < 0 {
			s.col = 0
		}
	case 'E':
		s.row += count
		s.col = 0
	case 'F':
		s.row -= count
		s.col = 0
	case 'G':
		s.col = count - 1
	case 'H', 'f':
		s.row = n(0, 1) - 1
		s.col = n(1, 1) - 1
		if s.col < 0 {
			s.col = 0
		}
	case 'K':
		s.eraseLine(n(0, 0))
	}
	s.touch()
	return end
}

func (s *screen) lines() []string {
	out := make([]string, 0, s.maxRow-s.minRow+1)
	for r := s.minRow; r <= s.maxRow; r++ {
		out = append(out, string(s.rows[r]))
	}
	return out
}
