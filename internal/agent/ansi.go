package agent

import "regexp"

// ansiPattern matches ANSI/VT escape sequences: CSI sequences (ESC [ ... final byte),
// OSC sequences (ESC ] ... ST), and two-byte ESC+char sequences.
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]|\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)|\x1b[()][0-9A-B]|\x1b[^[\]()]`)

// StripANSI removes ANSI/VT escape sequences from the given byte slice,
// returning plain text suitable for human reading and machine parsing.
func StripANSI(b []byte) []byte {
	return ansiPattern.ReplaceAll(b, nil)
}
