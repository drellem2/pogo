package claude

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
)

// These tests are drellem2/pogo#177's remedy (mg-c1e2 / mg-f394). The hook
// pressed a bare Enter on whatever row Claude Code highlighted, and 2.1.270+
// draws "No, exit" first and highlights it, so every spawn into an untrusted
// directory exited 1. The fixture below is a two-row dialog that reads single
// keys, tracks its highlight, and prints ACCEPTED or REFUSED. It redraws a
// highlight move the way Claude Code 2.1.283 does — the glyphs only, over
// relative cursor moves, with no label — and it can draw its rows in EITHER
// order, so it covers the dialog that broke the old code and the one that did
// not.

const (
	fixtureAccepted = "POGO-FIXTURE-ACCEPTED"
	fixtureRefused  = "POGO-FIXTURE-REFUSED"
	fixtureGotKey   = "POGO-FIXTURE-GOT-KEY"

	labelYes = "Yes, I trust this folder"
	labelNo  = "No, exit"
)

// liveDialog is Claude Code 2.1.283's trust dialog as its PTY emitted it,
// captured in a fresh `git init` directory (mg-f394), from the first line of the
// body to the end of the first render. The workspace path above it is omitted.
const liveDialog = "\x1b[2GQuick\x1b[8Gsafety\x1b[15Gcheck:\x1b[22GIs\x1b[25Gthis\x1b[30Ga\x1b[32Gproject\x1b[40Gyou\x1b[44Gcreated\x1b[52Gor\x1b[55Gone\x1b[59Gyou\x1b[63Gtrust?\x1b[70G(Like\x1b[76Gyour\x0d\x0d\x0a\x1b[2Gown\x1b[6Gcode,\x1b[12Ga\x1b[14Gwell-known\x1b[25Gopen\x1b[30Gsource\x1b[37Gproject,\x1b[46Gor\x1b[49Gwork\x1b[54Gfrom\x1b[59Gyour\x1b[64Gteam).\x1b[71GIf\x1b[74Gnot,\x0d\x0d\x0a\x1b[2Gtake\x1b[7Ga\x1b[9Gmoment\x1b[16Gto\x1b[19Greview\x1b[26Gwhat's\x1b[33Gin\x1b[36Gthis\x1b[41Gfolder\x1b[48Gfirst.\x0d\x0d\x0a\x0d\x0d\x0a\x1b[2GClaude\x1b[9GCode'll\x1b[17Gbe\x1b[20Gable\x1b[25Gto\x1b[28Gread,\x1b[34Gedit,\x1b[40Gand\x1b[44Gexecute\x1b[52Gfiles\x1b[58Ghere.\x0d\x0d\x0a\x0d\x0d\x0a\x1b[2GSecurity\x1b[11Gguide\x0d\x0d\x0a\x0d\x0d\x0a\x1b[2G\xe2\x9d\xaf\x1b[4GNo,\x1b[8Gexit\x0d\x0d\x0a\x1b[4GYes,\x1b[9GI\x1b[11Gtrust\x1b[17Gthis\x1b[22Gfolder\x0d\x0d\x0a\x0d\x0d\x0a\x1b[2GEnter\x1b[8Gto\x1b[11Gconfirm\x1b[19G\xc2\xb7\x1b[21GEsc\x1b[25Gto\x1b[28Gcancel\x0d\x0d\x0a\x1b[1C\x1b[4A\x1b[>0q\x1b[?u\x1b[c"

// liveMoveDown is what the same session emitted after one Down arrow: a space
// over the old highlight and a highlight on the next row, by relative cursor
// moves alone. No label is redrawn — which is why the hook replays the screen
// rather than reading the stripped stream.
const liveMoveDown = "\x1b[1D\x1b[4B\x0d\x1b[1C\x1b[4A \x0d\x1b[1C\x1b[1B\xe2\x9d\xaf\x0d\x0d\x0a\x0d\x0a\x0d\x0a\x1b[1C\x1b[3A"

// twoRowDialogScript is a PTY program shaped like Claude Code's trust dialog.
// rows are the two labels top to bottom, accept is the 1-based row that trusts,
// and start is the row highlighted at first. It draws the body, both rows (the
// highlight at column 1, labels at column 3, as 2.1.283 does), a blank line and
// the footer, then reads raw single keys: Up/Down move the highlight with a
// glyph-only redraw, Enter on the accept row prints fixtureAccepted and the
// composer marker, and Enter on the other row prints fixtureRefused and EXITS 1,
// as Claude Code does. Every key read also prints fixtureGotKey, so a test can
// count exactly what the hook sent.
//
// exitOnAccept makes the accept row exit too, standing in for a harness that
// dies on the "right" answer.
func twoRowDialogScript(row1, row2 string, accept, start int, exitOnAccept bool) string {
	onAccept := "printf '" + fixtureAccepted + "\\r\\n? for shortcuts\\r\\n'; sleep 30; exit 0"
	if exitOnAccept {
		onAccept = "printf '" + fixtureAccepted + "\\r\\n'; exit 0"
	}
	glyph := func(row int) string {
		return "$( [ \"$h\" = " + string(rune('0'+row)) + " ] && printf '" + trustHighlight + "' || printf ' ')"
	}
	return "stty raw -echo\n" +
		"h=" + string(rune('0'+start)) + "; n=0\n" +
		// Cursor sits on the line below the footer. Row 1 is 4 lines up, row 2
		// is 3, plus one line per fixtureGotKey printed since.
		"mark() { up=$(($1 + n)); printf '\\033[%dA\\r\\033[1C%s\\r\\033[%dB' \"$up\" \"$2\" \"$up\"; }\n" +
		"printf '" + dialogLine + "\\r\\n'\n" +
		"printf ' %s " + row1 + "\\r\\n' \"" + glyph(1) + "\"\n" +
		"printf ' %s " + row2 + "\\r\\n' \"" + glyph(2) + "\"\n" +
		"printf '\\r\\n Enter to confirm · Esc to cancel\\r\\n'\n" +
		"esc=$(printf '\\033'); cr=$(printf '\\r')\n" +
		"while :; do\n" +
		"  c=$(dd bs=1 count=1 2>/dev/null)\n" +
		"  printf '" + fixtureGotKey + "\\r\\n'; n=$((n+1))\n" +
		"  if [ \"$c\" = \"$esc\" ]; then\n" +
		"    s=$(dd bs=1 count=2 2>/dev/null); old=$h\n" +
		"    case \"$s\" in '[A') h=1;; '[B') h=2;; esac\n" +
		"    if [ \"$old\" != \"$h\" ]; then mark $((5 - old)) ' '; mark $((5 - h)) '" + trustHighlight + "'; fi\n" +
		"  elif [ \"$c\" = \"$cr\" ]; then\n" +
		"    if [ \"$h\" = " + string(rune('0'+accept)) + " ]; then " + onAccept + "; else printf '" + fixtureRefused + "\\r\\n'; exit 1; fi\n" +
		"  fi\n" +
		"done\n"
}

// noFirst and yesFirst are the two row orders. noFirst is Claude Code
// 2.1.270+, with the refusing row on top and highlighted — #177.
func noFirst(start int, exitOnAccept bool) string {
	return twoRowDialogScript(labelNo, labelYes, 2, start, exitOnAccept)
}

func yesFirst(start int, exitOnAccept bool) string {
	return twoRowDialogScript(labelYes, labelNo, 1, start, exitOnAccept)
}

// waitForDialogRows blocks until the fixture has drawn its footer, so a test's
// premise is about a rendered dialog rather than an empty buffer.
func waitForDialogRows(t *testing.T, a *agent.Agent) {
	t.Helper()
	if !sawWithin(a, "Enter to confirm", 10*time.Second) {
		t.Fatalf("fixture never drew its rows; PTY:\n%s", agent.StripANSI(a.RecentOutput(4096)))
	}
}

// runWatch runs the real loop and returns its outcome, failing if it outlives
// a generous bound.
func runWatch(t *testing.T, a *agent.Agent, budget time.Duration) trustWatchOutcome {
	t.Helper()
	got := make(chan trustWatchOutcome, 1)
	go func() { got <- trustDialogWatch(a, budget, 50*time.Millisecond, time.Now) }()
	select {
	case o := <-got:
		return o
	case <-time.After(budget + 15*time.Second):
		t.Fatalf("watch did not return within its %v budget", budget)
	}
	return trustWatchInconclusive
}

func ptyText(a *agent.Agent) string {
	return string(agent.StripANSI(a.RecentOutput(composerScanBytes)))
}

// TestBareEnterOnTheNoFirstDialogRefuses is the POSITIVE CONTROL: it gives the
// fixture the OLD hook's keystroke. A bare "\r" on the No-first dialog must
// reach fixtureRefused and exit. If it does not, every test below asserting
// "not refused" is passing on a fixture that cannot refuse — the shape of
// trust_hook_race_test.go before mg-f394, whose dialog accepted any line.
func TestBareEnterOnTheNoFirstDialogRefuses(t *testing.T) {
	a := spawnScripted(t, "bare-enter-ctl", noFirst(1, false))
	waitForDialogRows(t, a)

	if err := a.SendRaw("\r"); err != nil {
		t.Fatal(err)
	}
	if !sawWithin(a, fixtureRefused, 5*time.Second) {
		t.Fatalf("a bare Enter on the No-first dialog did not refuse: the fixture "+
			"cannot tell a right answer from a wrong one. PTY:\n%s", ptyText(a))
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Error("fixture did not exit after refusing, as Claude Code does")
	}
}

// TestNoFirstDialogIsMovedDownThenAccepted is #177 itself, against the fixture
// the control just proved can refuse: the hook must move the highlight DOWN to
// the trusting row and only then press Enter — two keys, no more.
func TestNoFirstDialogIsMovedDownThenAccepted(t *testing.T) {
	a := spawnScripted(t, "no-first", noFirst(1, false))
	waitForDialogRows(t, a)

	outcome := runWatch(t, a, 15*time.Second)

	out := ptyText(a)
	if strings.Contains(out, fixtureRefused) {
		t.Fatalf("hook answered the refusing row; PTY:\n%s", out)
	}
	if !strings.Contains(out, fixtureAccepted) {
		t.Fatalf("hook never accepted; PTY:\n%s", out)
	}
	if n := strings.Count(out, fixtureGotKey); n != 2 {
		t.Errorf("fixture read %d keys, want 2 (one Down, one Enter)", n)
	}
	if outcome != trustWatchConfirmed {
		t.Errorf("outcome = %v, want %v", outcome, trustWatchConfirmed)
	}
}

// TestYesFirstDialogIsAcceptedWithoutMoving is the other order, highlighted on
// the trusting row: one Enter, no arrow. A fixed "\x1b[B\r" — the alternative
// mg-c1e2 rejected — moves OFF the right row here and refuses.
func TestYesFirstDialogIsAcceptedWithoutMoving(t *testing.T) {
	a := spawnScripted(t, "yes-first", yesFirst(1, false))
	waitForDialogRows(t, a)

	outcome := runWatch(t, a, 15*time.Second)

	out := ptyText(a)
	if !strings.Contains(out, fixtureAccepted) || strings.Contains(out, fixtureRefused) {
		t.Fatalf("want accepted and not refused; PTY:\n%s", out)
	}
	if n := strings.Count(out, fixtureGotKey); n != 1 {
		t.Errorf("fixture read %d keys, want exactly 1 (the Enter): the hook "+
			"should not move a highlight that is already on the trusting row", n)
	}
	if outcome != trustWatchConfirmed {
		t.Errorf("outcome = %v, want %v", outcome, trustWatchConfirmed)
	}
}

// TestYesFirstDialogHighlightingNoIsMovedUp pins the DIRECTION: with the
// trusting row on top and the refusing row highlighted, the move is Up. A hook
// that only ever pressed Down would sit on "No, exit" here.
func TestYesFirstDialogHighlightingNoIsMovedUp(t *testing.T) {
	a := spawnScripted(t, "yes-first-up", yesFirst(2, false))
	waitForDialogRows(t, a)

	outcome := runWatch(t, a, 15*time.Second)

	out := ptyText(a)
	if !strings.Contains(out, fixtureAccepted) || strings.Contains(out, fixtureRefused) {
		t.Fatalf("want accepted and not refused; PTY:\n%s", out)
	}
	if outcome != trustWatchConfirmed {
		t.Errorf("outcome = %v, want %v", outcome, trustWatchConfirmed)
	}
}

// TestUnrecognisedDialogFailsClosed: a dialog whose rows are neither known
// label gets NO keystroke, and the spent budget is drift.
func TestUnrecognisedDialogFailsClosed(t *testing.T) {
	a := spawnScripted(t, "unknown-label",
		twoRowDialogScript("Open read-only", "Go back", 1, 1, false))
	waitForDialogRows(t, a)

	outcome := runWatch(t, a, 2*time.Second)

	if sawWithin(a, fixtureGotKey, 500*time.Millisecond) {
		t.Errorf("hook sent a key to a dialog whose rows it could not identify; "+
			"it must fail closed. PTY:\n%s", ptyText(a))
	}
	if outcome != trustWatchDrift {
		t.Errorf("outcome = %v, want %v", outcome, trustWatchDrift)
	}
}

// TestExitAfterAnswerIsRefused: the hook presses Enter on the trusting row and
// the harness exits instead of drawing the composer. That must be the distinct
// refused outcome — not confirmed (the old code returned confirmed the instant
// it pressed Enter) and not inconclusive — and it must emit
// trust_dialog_refused.
func TestExitAfterAnswerIsRefused(t *testing.T) {
	var got []events.Event
	prev := emitEvent
	emitEvent = func(ev events.Event) { got = append(got, ev) }
	t.Cleanup(func() { emitEvent = prev })

	a := spawnScripted(t, "exit-after-answer", noFirst(1, true))
	waitForDialogRows(t, a)

	done := make(chan struct{})
	go func() {
		defer close(done)
		watchForTrustDialog(a, 15*time.Second, 50*time.Millisecond)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("watch did not return after the harness exited")
	}

	if !strings.Contains(ptyText(a), fixtureAccepted) {
		t.Fatal("premise broken: the hook never answered, so the exit says nothing about the answer")
	}
	if len(got) != 1 || got[0].EventType != "trust_dialog_refused" {
		t.Fatalf("events = %+v, want exactly one trust_dialog_refused", got)
	}
	if got[0].Agent != "cat-exit-after-answer" {
		t.Errorf("event agent = %q, want the cat-<name> identity", got[0].Agent)
	}
}

// TestSpentBudgetAfterAnswerIsRefused covers the same decision on the deadline
// arm: an agent already exited when a spent budget is noticed, after the hook
// answered, is refused rather than inconclusive.
func TestSpentBudgetAfterAnswerIsRefused(t *testing.T) {
	exited := make(chan struct{})
	close(exited)
	if got := spentBudgetOutcome(exited, true); got != trustWatchRefused {
		t.Errorf("spentBudgetOutcome(closed, answered) = %v, want %v", got, trustWatchRefused)
	}
	live := make(chan struct{})
	if got := spentBudgetOutcome(live, true); got != trustWatchDrift {
		t.Errorf("spentBudgetOutcome(open, answered) = %v, want %v: answered but "+
			"no composer within the budget is the drift signature", got, trustWatchDrift)
	}
}

func TestHighlightedTrustRow(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  trustRow
		label string
		move  int
	}{
		{"2.1.283 live render: No highlighted, Yes below", liveDialog, trustRowRefuse, "no,exit", 1},
		{"2.1.283 live render after one Down", liveDialog + liveMoveDown, trustRowAccept, "yes,itrustthisfolder", 0},
		{"stripped stream alone cannot say this", "❯ No, exit\n  Yes, I trust this folder\n", trustRowRefuse, "no,exit", 1},
		{"yes first, highlighted", "❯ Yes, I trust this folder\n  No, exit\n", trustRowAccept, "yes,itrustthisfolder", 0},
		{"yes first, no highlighted", "  Yes, I trust this folder\n❯ No, exit\n", trustRowRefuse, "no,exit", -1},
		{"numbered rows of earlier releases", "❯ 1. Yes, proceed\n  2. No, exit\n", trustRowAccept, "yes,proceed", 0},
		{"unknown wording", "❯ Open read-only\n  Go back\n", trustRowUnknown, "openread-only", 0},
		{"highlight on an unknown row beside the accept row", "  Yes, I trust this folder\n❯ Something new\n", trustRowUnknown, "somethingnew", 0},
		{"refusing row drawn, partner not yet", "❯ No, exit\n", trustRowNone, "no,exit", 0},
		{"body only", "Quick safety check: Is this a project you created or one you trust?", trustRowNone, "", 0},
		{"ansi and column moves", "\x1b[2G\x1b[36m❯\x1b[4GNo,\x1b[8Gexit\x1b[0m\r\n\x1b[4GYes,\x1b[9GI\x1b[11Gtrust\x1b[17Gthis\x1b[22Gfolder\r\n", trustRowRefuse, "no,exit", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, label, move := highlightedTrustRow([]byte(tt.input))
			if got != tt.want || label != tt.label || move != tt.move {
				t.Errorf("highlightedTrustRow = (%d, %q, %d), want (%d, %q, %d)",
					got, label, move, tt.want, tt.label, tt.move)
			}
		})
	}
}

// TestLiveMoveIsInvisibleToTheStrippedStream is the premise screenLines rests
// on: after a highlight move, the stripped stream's LAST "❯" is followed by no
// label at all. If Claude Code ever starts redrawing labels on a move, this
// fails, and the replay may no longer be needed.
func TestLiveMoveIsInvisibleToTheStrippedStream(t *testing.T) {
	c := collapse(string(agent.StripANSI([]byte(liveDialog + liveMoveDown))))
	i := strings.LastIndex(c, trustHighlight)
	if i < 0 {
		t.Fatal("no highlight in the stripped stream")
	}
	if rest := c[i+len(trustHighlight):]; strings.Contains(strings.ToLower(rest), "yes") {
		t.Errorf("the moved highlight is followed by %q: the stream now carries "+
			"the label, so the premise for replaying the screen has changed", rest)
	}
}
