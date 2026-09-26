package codex

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
)

// These tests are the codex half of drellem2/pogo#177's remedy (mg-7511). The
// Claude hook pressed a bare Enter on whatever row Claude Code highlighted, and
// 2.1.270+ highlights "No, exit". The Codex hook pressed the same bare Enter.
// Codex highlights its trusting row by default, so that Enter happened to be
// right — but Enter on Codex's "No, quit" row exits the harness with status 0
// (measured on 0.132.0), and the old hook would have recorded that spawn as
// confirmed. The fixture below is a two-row dialog that reads single keys,
// tracks its highlight, and prints ACCEPTED or REFUSED; it starts on either row,
// so it covers the default Codex ships AND the one that would break the old code.

const (
	fixtureAccepted = "POGO-FIXTURE-ACCEPTED"
	fixtureRefused  = "POGO-FIXTURE-REFUSED"
	fixtureGotKey   = "POGO-FIXTURE-GOT-KEY"
)

// twoRowDialogScript is a PTY program shaped like Codex's trust dialog. It
// draws the body and both rows with "›" on row start (1 or 2), then reads raw
// single keys: Up/Down move the highlight and redraw both rows (Codex re-emits
// both rows on a move — measured on 0.132.0), Enter on row 1 prints
// fixtureAccepted and draws the composer, and Enter on row 2 prints
// fixtureRefused and EXITS 0, as Codex does. Every key read also prints
// fixtureGotKey, so a test can prove the hook sent nothing.
//
// label1/label2 are the row labels, so the same fixture can play an unknown
// wording. exitOnAccept makes row 1 exit too, standing in for a harness that
// dies on the "right" answer.
func twoRowDialogScript(start int, label1, label2 string, exitOnAccept bool) string {
	accept := "printf '" + fixtureAccepted + "\\r\\n" + composerLine + "\\r\\n'; sleep 30; exit 0"
	if exitOnAccept {
		accept = "printf '" + fixtureAccepted + "\\r\\n'; exit 0"
	}
	return "stty raw -echo\n" +
		"h=" + string(rune('0'+start)) + "\n" +
		"render() { if [ \"$h\" = 1 ]; then printf '› 1. " + label1 + "\\r\\n  2. " + label2 + "\\r\\n'; " +
		"else printf '  1. " + label1 + "\\r\\n› 2. " + label2 + "\\r\\n'; fi; }\n" +
		"printf '" + dialogLine + "\\r\\n'\n" +
		"render\n" +
		"printf 'Press enter to continue\\r\\n'\n" +
		"esc=$(printf '\\033'); cr=$(printf '\\r')\n" +
		"while :; do\n" +
		"  c=$(dd bs=1 count=1 2>/dev/null)\n" +
		"  printf '" + fixtureGotKey + "\\r\\n'\n" +
		"  if [ \"$c\" = \"$esc\" ]; then\n" +
		"    s=$(dd bs=1 count=2 2>/dev/null)\n" +
		"    case \"$s\" in '[A') h=1;; '[B') h=2;; esac\n" +
		"    render\n" +
		"  elif [ \"$c\" = \"$cr\" ]; then\n" +
		"    if [ \"$h\" = 1 ]; then " + accept + "; else printf '" + fixtureRefused + "\\r\\n'; exit 0; fi\n" +
		"  fi\n" +
		"done\n"
}

// waitForDialogRows blocks until the fixture has drawn its rows, so a test's
// premise is about a rendered dialog rather than an empty buffer.
func waitForDialogRows(t *testing.T, a *agent.Agent) {
	t.Helper()
	if !sawWithin(a, "2. ", 10*time.Second) {
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

// TestBareEnterOnARefusingDefaultRefuses is the POSITIVE CONTROL: it proves the
// fixture discriminates, by giving it the old hook's keystroke. A bare "\r" on
// a dialog highlighting its refusing row must reach fixtureRefused. If it does
// not, every test below that asserts "not refused" is passing on a fixture that
// cannot refuse — the trust_hook_race_test.go shape mg-c1e2 warned about.
func TestBareEnterOnARefusingDefaultRefuses(t *testing.T) {
	a := spawnScripted(t, "bare-enter-ctl", twoRowDialogScript(2, "Yes, continue", "No, quit", false))
	waitForDialogRows(t, a)

	if err := a.SendRaw("\r"); err != nil {
		t.Fatal(err)
	}
	if !sawWithin(a, fixtureRefused, 5*time.Second) {
		t.Fatalf("a bare Enter on the refusing row did not refuse: the fixture "+
			"cannot tell a right answer from a wrong one. PTY:\n%s",
			agent.StripANSI(a.RecentOutput(4096)))
	}
	select {
	case <-a.Done():
	case <-time.After(5 * time.Second):
		t.Error("fixture did not exit after refusing, as Codex does")
	}
}

// TestRefusingDefaultIsMovedThenAccepted is the fix against the same fixture
// the control just proved can refuse: the hook must move the highlight to the
// trusting row by label and only then press Enter.
func TestRefusingDefaultIsMovedThenAccepted(t *testing.T) {
	a := spawnScripted(t, "refusing-default", twoRowDialogScript(2, "Yes, continue", "No, quit", false))
	waitForDialogRows(t, a)

	outcome := runWatch(t, a, 15*time.Second)

	out := string(agent.StripANSI(a.RecentOutput(8192)))
	if strings.Contains(out, fixtureRefused) {
		t.Fatalf("hook answered the refusing row; PTY:\n%s", out)
	}
	if !strings.Contains(out, fixtureAccepted) {
		t.Fatalf("hook never accepted; PTY:\n%s", out)
	}
	if outcome != trustWatchConfirmed {
		t.Errorf("outcome = %v, want %v", outcome, trustWatchConfirmed)
	}
}

// TestTrustingDefaultIsAccepted is the default Codex actually ships (0.132.0
// and upstream main): the highlight starts on the trusting row and one Enter
// accepts it, with no arrow key.
func TestTrustingDefaultIsAccepted(t *testing.T) {
	a := spawnScripted(t, "trusting-default", twoRowDialogScript(1, "Yes, continue", "No, quit", false))
	waitForDialogRows(t, a)

	outcome := runWatch(t, a, 15*time.Second)

	out := string(agent.StripANSI(a.RecentOutput(8192)))
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

// TestUpstreamWordingIsAccepted covers the labels upstream Codex uses after
// openai/codex#44732, from its source and render snapshot (no live PTY on this
// host, which runs 0.132.0).
func TestUpstreamWordingIsAccepted(t *testing.T) {
	a := spawnScripted(t, "upstream-labels", twoRowDialogScript(2, "Trust and continue", "Quit", false))
	waitForDialogRows(t, a)

	outcome := runWatch(t, a, 15*time.Second)

	out := string(agent.StripANSI(a.RecentOutput(8192)))
	if !strings.Contains(out, fixtureAccepted) || strings.Contains(out, fixtureRefused) {
		t.Fatalf("want accepted and not refused; PTY:\n%s", out)
	}
	if outcome != trustWatchConfirmed {
		t.Errorf("outcome = %v, want %v", outcome, trustWatchConfirmed)
	}
}

// TestUnrecognisedHighlightFailsClosed: a dialog whose highlighted row is
// neither known label gets NO keystroke, and the spent budget is drift. Upstream's
// "Open restricted" row is the realistic case — it opens Codex without trusting
// the directory.
func TestUnrecognisedHighlightFailsClosed(t *testing.T) {
	a := spawnScripted(t, "unknown-label", twoRowDialogScript(1, "Open restricted", "Back to Agent Command Center", false))
	waitForDialogRows(t, a)

	outcome := runWatch(t, a, 2*time.Second)

	if sawWithin(a, fixtureGotKey, 500*time.Millisecond) {
		t.Errorf("hook sent a key to a dialog whose highlighted row it could not "+
			"identify; it must fail closed. PTY:\n%s", agent.StripANSI(a.RecentOutput(4096)))
	}
	if outcome != trustWatchDrift {
		t.Errorf("outcome = %v, want %v", outcome, trustWatchDrift)
	}
}

// TestExitAfterAnswerIsRefused: the hook presses Enter on the accept row and
// the harness exits instead of drawing the composer. That must be the distinct
// refused outcome — not confirmed (the old code returned confirmed the instant
// it pressed Enter) and not inconclusive — and it must emit
// trust_dialog_refused.
func TestExitAfterAnswerIsRefused(t *testing.T) {
	var got []events.Event
	prev := emitEvent
	emitEvent = func(ev events.Event) { got = append(got, ev) }
	t.Cleanup(func() { emitEvent = prev })

	a := spawnScripted(t, "exit-after-answer", twoRowDialogScript(1, "Yes, continue", "No, quit", true))
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

	if !strings.Contains(string(agent.StripANSI(a.RecentOutput(8192))), fixtureAccepted) {
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
	}{
		{"codex 0.132.0 live render", realDialog, trustRowAccept, "yes,continue"},
		{
			// Captured live after a Down arrow on 0.132.0: both rows re-emitted,
			// marker moved. The LAST highlight is the current one.
			name:  "after a move, the last highlight wins",
			input: realDialog + "1.Yes,continue›2.No,quit",
			want:  trustRowRefuse, label: "no,quit",
		},
		{"moved back", realDialog + "1.Yes,continue›2.No,quit" + "›1.Yes,continue2.No,quit", trustRowAccept, "yes,continue"},
		{
			name: "upstream render snapshot",
			input: "Trust this folder? Codex can read, edit, and run files here,\n" +
				"› 1. Trust and continue\n  2. Quit\n\n  enter continue and create sandbox · esc quit",
			want: trustRowAccept, label: "trustandcontinue",
		},
		{"upstream refusing highlight", "  1. Trust and continue\n› 2. Quit\n\n  enter continue · esc quit", trustRowRefuse, "quit"},
		{"upstream restricted row", "› 1. Open restricted\n  2. Back to Agent Command Center", trustRowUnknown, "openrestricted"},
		{"body drawn, rows not yet", "Working with untrusted contents comes with", trustRowNone, ""},
		{"composer placeholder is not a row", realComposer + "› Explain this codebase", trustRowNone, ""},
		{
			// Live 0.132.0 PTY bytes after a Down arrow: StripANSI keeps the
			// private-mode tail, which must not become part of the label.
			name:  "private-mode escape after a moved row",
			input: realDialog + "1.Yes,continue›2.No,quit\x1b[?25l\x1b[?2026l",
			want:  trustRowRefuse, label: "no,quit",
		},
		{"with ANSI", "\x1b[36m› 1. \x1b[1mYes, continue\x1b[0m  2. No, quit", trustRowAccept, "yes,continue"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, label := highlightedTrustRow([]byte(tt.input))
			if got != tt.want || label != tt.label {
				t.Errorf("highlightedTrustRow = (%d, %q), want (%d, %q)", got, label, tt.want, tt.label)
			}
		})
	}
}

// TestMatchesUpstreamTrustDialog pins the post-#44732 body, which carries
// neither 0.132.0 marker phrase.
func TestMatchesUpstreamTrustDialog(t *testing.T) {
	body := "Trust this folder? Codex can read, edit, and run files here,\n" +
		"subject to your permission settings."
	if !matchesTrustDialog([]byte(body)) {
		t.Error("upstream trust dialog body not matched")
	}
	if !matchesTrustDialog([]byte(strings.Join(strings.Fields(body), ""))) {
		t.Error("upstream body drawn glyph-by-glyph not matched")
	}
}
