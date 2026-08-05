package wedgewatch

import (
	"fmt"
	"strings"
)

// PTY fixtures reconstructed from the 2026-08-04 and 2026-08-05 terminals.
//
// They are built as RAW bytes with real ANSI in them, not as clean strings,
// because the ANSI is where the last detector of this family went wrong.
// mg-f36b: Claude Code renders modal footers as TUI columns placed with
// cursor-forward escapes (ESC[<n>C) rather than literal spaces, and
// agent.StripANSI deletes those escapes instead of substituting a space — so a
// literal compare against the spaced marker never matched in production and the
// rating-dialog watcher logged zero dismissals for two months while looking
// installed. A fixture written as a clean string would have passed that
// watcher's tests too.

const (
	esc = "\x1b"
	// clr is the screen-clear + home the TUI emits before a repaint.
	clr = esc + "[2J" + esc + "[H"
	// dim/reset are the SGR pairs the status line is wrapped in.
	dim   = esc + "[2m"
	reset = esc + "[0m"
)

// col renders n columns of spacing the way the TUI actually does it: a
// cursor-forward escape, NOT n spaces. StripANSI deletes it entirely.
func col(n int) string { return fmt.Sprintf("%s[%dC", esc, n) }

// wedgedLoginPTY is the 2026-08-04 screen: the two 401 lines, the login prompt,
// and the frozen counter, repainted several times as the spinner animates.
//
// The repaints are the point. This buffer is what "last-activity: just now"
// was measuring for thirteen hours.
func wedgedLoginPTY(declared string) []byte {
	var b strings.Builder
	for i := 0; i < 4; i++ {
		b.WriteString(clr)
		b.WriteString("API Error: 401 OAuth access token has been revoked.\r\n")
		b.WriteString("API Error: 401 OAuth access token has expired. Re-authenticate to continue.\r\n")
		b.WriteString("Please run /login\r\n")
		b.WriteString(dim + "✻" + col(1) + "Baked for " + declared + reset + "\r\n")
	}
	return []byte(b.String())
}

// outagePTY is the connectivity half of the same event, as it appeared before
// the 401s: name resolution failing against the API host.
func outagePTY(declared string) []byte {
	var b strings.Builder
	b.WriteString(clr)
	b.WriteString("Unable to connect to API (ENOTFOUND api.anthropic.com)\r\n")
	b.WriteString(dim + "✻" + col(1) + "Baked for " + declared + reset + "\r\n")
	return []byte(b.String())
}

// workingPTY is a healthy agent mid-turn: no dead-end marker, and a counter
// that reads whatever the turn has been running for.
func workingPTY(declared string) []byte {
	var b strings.Builder
	b.WriteString(clr)
	b.WriteString("● Read(internal/wedgewatch/counter.go)\r\n")
	b.WriteString("  ⎿  Read 214 lines\r\n")
	b.WriteString(dim + "✻ Baking…" + col(2) + "(" + declared + col(1) + "· esc to interrupt)" + reset)
	return []byte(b.String())
}

// ratingDialogPTY reproduces mg-f36b's trap exactly: the option row spaced with
// cursor-forward escapes, so the on-screen "1:Bad 2:Fine 3:Good 0:Dismiss"
// reaches any scanner as the run-together "1:Bad2:Fine3:Good0:Dismiss".
func ratingDialogPTY(declared string) []byte {
	var b strings.Builder
	b.WriteString(clr)
	b.WriteString("How is Claude doing this session?\r\n")
	b.WriteString("1:Bad" + col(4) + "2:Fine" + col(4) + "3:Good" + col(4) + "0:Dismiss\r\n")
	b.WriteString(dim + "Baked for " + declared + reset + "\r\n")
	return []byte(b.String())
}

// rateLimitModalPTY is the rate-limit-options modal, likewise column-spaced.
func rateLimitModalPTY(declared string) []byte {
	var b strings.Builder
	b.WriteString(clr)
	b.WriteString("What do you want to do?\r\n")
	b.WriteString("❯ 1." + col(1) + "Stop and wait for limit to reset\r\n")
	b.WriteString("  2." + col(1) + "Continue on a different model\r\n")
	b.WriteString(dim + "Baked for " + declared + reset + "\r\n")
	return []byte(b.String())
}

// quotingPTY is the NEGATIVE control that matters most, and the reason
// MarkerHoldDown is not zero: an agent doing perfectly good work ON THIS VERY
// FEATURE puts every enumerated marker string into its own PTY. A detector that
// fired on marker text alone would have reported the polecat that wrote it.
func quotingPTY(declared string) []byte {
	var b strings.Builder
	b.WriteString(clr)
	b.WriteString("● Write(internal/wedgewatch/markers.go)\r\n")
	b.WriteString("  ⎿  LoginPromptText = \"Please run /login\"\r\n")
	b.WriteString("     API401Text      = \"API Error: 401\"\r\n")
	b.WriteString("     ENOTFOUNDText   = \"ENOTFOUND\"\r\n")
	b.WriteString("     RatingDialogText = \"1:Bad 2:Fine 3:Good 0:Dismiss\"\r\n")
	b.WriteString("     RateLimitText   = \"Stop and wait for limit to reset\"\r\n")
	b.WriteString(dim + "✻ Baking…" + col(2) + "(" + declared + col(1) + "· esc to interrupt)" + reset)
	return []byte(b.String())
}

// noCounterPTY has output but no parseable work counter — a harness whose
// status line has been renamed. It must produce "could not judge", never
// "healthy".
func noCounterPTY() []byte {
	return []byte(clr + "● Bash(go test ./...)\r\n  ⎿  ok  github.com/drellem2/pogo/internal/wedgewatch\r\n")
}
