package client

import (
	"bytes"
	"strings"
	"testing"
)

// claudeCodeStartupModes is the exact set of DEC private-mode sequences
// observed as literals in the Claude Code binary pogo agents run
// (~/.local/share/claude/versions/2.1.220, 2026-07-28): alternate screen,
// mouse tracking + SGR mouse encoding, focus reporting, synchronized output,
// hide cursor. Every one of them used to survive a detach and land on the
// user's shell prompt.
const claudeCodeStartupModes = "\x1b[?1049h\x1b[?1000h\x1b[?1006h\x1b[?1004h\x1b[?2026h\x1b[?25l"

// TestRestoreSequenceResetsModesTheAgentEnabled is the positive control for the
// mg-9b5b corruption half. Feed the tracker what a real Claude Code TUI writes
// at startup and the detach-time restore must turn every one of those modes
// back off — including focus reporting, whose leftover `\x1b[I` on window
// refocus is the "control characters in my terminal prompt" Daniel reported.
func TestRestoreSequenceResetsModesTheAgentEnabled(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte(claudeCodeStartupModes))
	ts.Write([]byte("some ordinary TUI output\r\n"))

	got := string(ts.restoreSequence())
	for _, want := range []string{
		"\x1b[?1049l", // leave the alternate screen
		"\x1b[?1000l", // mouse tracking off
		"\x1b[?1006l", // SGR mouse encoding off
		"\x1b[?1004l", // focus reporting off
		"\x1b[?2026l", // synchronized output off
		"\x1b[?25h",   // cursor visible again
	} {
		if !strings.Contains(got, want) {
			t.Errorf("restoreSequence() = %q, missing %q", got, want)
		}
	}
	if !strings.HasSuffix(got, "\r\n") {
		t.Errorf("restoreSequence() = %q, want a trailing CRLF so the shell prompt starts on a fresh line", got)
	}
}

// TestRestoreLeavesAltScreenExitLast pins the ordering: the resets have to be
// written while still on the alternate screen, then the buffer switch, so the
// user lands back on the scrollback they left rather than on a screen the
// resets scribbled over.
func TestRestoreLeavesAltScreenExitLast(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b[?1049h\x1b[?1000h\x1b[?1004h"))

	got := string(ts.restoreSequence())
	alt := strings.Index(got, "\x1b[?1049l")
	if alt < 0 {
		t.Fatalf("restoreSequence() = %q, missing alt-screen exit", got)
	}
	for _, earlier := range []string{"\x1b[?1000l", "\x1b[?1004l"} {
		if i := strings.Index(got, earlier); i < 0 || i > alt {
			t.Errorf("restoreSequence() = %q: %q must precede the alt-screen exit", got, earlier)
		}
	}
}

// TestRestoreSkipsModesTheAgentTurnedBackOff keeps the restore minimal: a TUI
// that cleaned up after itself needs nothing undone.
func TestRestoreSkipsModesTheAgentTurnedBackOff(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b[?1049h\x1b[?1000h\x1b[?25l"))
	ts.Write([]byte("\x1b[?25h\x1b[?1000l\x1b[?1049l"))

	if got := string(ts.restoreSequence()); got != "\r\n" {
		t.Errorf("restoreSequence() = %q, want only the CRLF — the agent already restored every mode", got)
	}
}

// TestRestoreDoesNotClobberModesTheAgentNeverTouched is the guard against the
// tempting-but-wrong fix. Blanket-resetting a fixed mode list on detach would
// send `\x1b[?2004l` and turn off the bracketed paste the user's *shell* owns,
// breaking pasting in the terminal the detach just handed back.
func TestRestoreDoesNotClobberModesTheAgentNeverTouched(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b[?1049h plain output \x1b[?1049l"))

	got := string(ts.restoreSequence())
	for _, unwanted := range []string{"2004", "1004", "1000", "1006", "?25", "?7"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("restoreSequence() = %q, must not mention mode %s — the agent never set it", got, unwanted)
		}
	}
}

// TestModeSequenceSplitAcrossWrites is the streaming-parser guard. A PTY read
// boundary lands wherever the kernel says, including in the middle of
// `\x1b[?1049h`. A tracker that scanned each chunk independently would miss
// exactly the mode it most needs to reset.
func TestModeSequenceSplitAcrossWrites(t *testing.T) {
	seq := []byte("\x1b[?1049h\x1b[?1004h")
	for split := 1; split < len(seq); split++ {
		ts := newTermState()
		ts.Write(seq[:split])
		ts.Write(seq[split:])
		got := string(ts.restoreSequence())
		if !strings.Contains(got, "\x1b[?1049l") || !strings.Contains(got, "\x1b[?1004l") {
			t.Errorf("split at %d: restoreSequence() = %q, want both modes reset", split, got)
		}
	}

	// One byte at a time — the pathological case.
	ts := newTermState()
	for _, b := range seq {
		ts.Write([]byte{b})
	}
	if got := string(ts.restoreSequence()); !strings.Contains(got, "\x1b[?1049l") {
		t.Errorf("byte-at-a-time: restoreSequence() = %q, want the alt screen reset", got)
	}
}

// TestMultiParamModeSequence covers the compact form TUIs actually emit —
// several modes in one sequence.
func TestMultiParamModeSequence(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b[?1000;1002;1006;1004h"))

	got := string(ts.restoreSequence())
	for _, want := range []string{"\x1b[?1000l", "\x1b[?1002l", "\x1b[?1004l", "\x1b[?1006l"} {
		if !strings.Contains(got, want) {
			t.Errorf("restoreSequence() = %q, missing %q", got, want)
		}
	}
}

// TestDefaultOnModesAreTurnedBackOn covers the other direction: a mode whose
// default is *set* and which the agent cleared must be restored with `h`, not
// `l`. Autowrap off leaves a long shell prompt overprinting its own line.
func TestDefaultOnModesAreTurnedBackOn(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b[?7l\x1b[?25l"))

	got := string(ts.restoreSequence())
	for _, want := range []string{"\x1b[?7h", "\x1b[?25h"} {
		if !strings.Contains(got, want) {
			t.Errorf("restoreSequence() = %q, missing %q", got, want)
		}
	}
}

// TestOSCAndDCSStringsDoNotConfuseTheTracker: OSC payloads (window titles,
// hyperlinks) are arbitrary text. The parser must consume them as strings, not
// look for mode sequences inside them, and must resync afterwards.
func TestOSCAndDCSStringsDoNotConfuseTheTracker(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b]0;a title with [?1049h in it\x07"))
	ts.Write([]byte("\x1b]8;;https://example.com/?x=1\x1b\\link\x1b]8;;\x1b\\"))
	ts.Write([]byte("\x1bP+q544e\x1b\\"))
	if got := string(ts.restoreSequence()); got != "\r\n" {
		t.Errorf("restoreSequence() = %q, want nothing to restore — every mode-looking byte was inside a string", got)
	}

	// …and a real mode after the strings is still tracked.
	ts.Write([]byte("\x1b[?1004h"))
	if got := string(ts.restoreSequence()); !strings.Contains(got, "\x1b[?1004l") {
		t.Errorf("restoreSequence() = %q, want focus reporting reset after the strings", got)
	}
}

// TestSGRAndScrollRegionAreReset: a detach mid-render otherwise hands the shell
// prompt the agent's colours and a narrowed scroll region.
func TestSGRAndScrollRegionAreReset(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b[1;5r\x1b[38;5;204mstill coloured"))

	got := string(ts.restoreSequence())
	if !strings.Contains(got, "\x1b[r") {
		t.Errorf("restoreSequence() = %q, want the scroll region reset", got)
	}
	if !strings.Contains(got, "\x1b[0m") {
		t.Errorf("restoreSequence() = %q, want the SGR reset", got)
	}

	// A stream that reset its own attributes needs neither.
	ts2 := newTermState()
	ts2.Write([]byte("\x1b[31mred\x1b[0m\x1b[r"))
	if got := string(ts2.restoreSequence()); got != "\r\n" {
		t.Errorf("restoreSequence() = %q, want nothing to restore", got)
	}
}

// TestKeypadApplicationModeIsReset — ESC = leaves the numeric keypad emitting
// `\x1bOp`-style sequences at the shell prompt.
func TestKeypadApplicationModeIsReset(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b=stuff"))
	if got := string(ts.restoreSequence()); !strings.Contains(got, "\x1b>") {
		t.Errorf("restoreSequence() = %q, want DECKPNM", got)
	}

	ts2 := newTermState()
	ts2.Write([]byte("\x1b=stuff\x1b>"))
	if got := string(ts2.restoreSequence()); strings.Contains(got, "\x1b>") {
		t.Errorf("restoreSequence() = %q, keypad mode was already restored by the agent", got)
	}
}

// TestFullResetClearsTrackedState — after RIS the terminal is at power-on
// defaults, so there is nothing left for the detach to undo.
func TestFullResetClearsTrackedState(t *testing.T) {
	ts := newTermState()
	ts.Write([]byte("\x1b[?1049h\x1b[?1004h\x1b[31m"))
	ts.Write([]byte("\x1bc"))
	if got := string(ts.restoreSequence()); got != "\r\n" {
		t.Errorf("restoreSequence() = %q, want nothing to restore after RIS", got)
	}
}

// TestWriteIsTransparent — termState sits in the conn→stdout copy. It must
// consume every byte it is handed and never report a short write, or io.Copy
// aborts and the user's attach dies mid-session.
func TestWriteIsTransparent(t *testing.T) {
	ts := newTermState()
	payload := bytes.Repeat([]byte("\x1b[?1049h\x1b]0;t\x07abc\x1b[0m"), 64)
	n, err := ts.Write(payload)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(payload) {
		t.Errorf("Write returned %d, want %d", n, len(payload))
	}
}

// TestParseParams covers the parameter forms terminals accept.
func TestParseParams(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want []int
	}{
		{"", nil},
		{"0", []int{0}},
		{"1049", []int{1049}},
		{"1000;1006", []int{1000, 1006}},
		{"38;5;204", []int{38, 5, 204}},
		{"4:3", []int{4}},
	} {
		got := parseParams([]byte(tc.in))
		if len(got) != len(tc.want) {
			t.Errorf("parseParams(%q) = %v, want %v", tc.in, got, tc.want)
			continue
		}
		for i := range got {
			if got[i] != tc.want[i] {
				t.Errorf("parseParams(%q) = %v, want %v", tc.in, got, tc.want)
				break
			}
		}
	}
}
