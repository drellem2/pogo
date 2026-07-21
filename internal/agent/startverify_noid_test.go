package agent

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

// Fallback-signal control suite for mg-c33e.
//
// mg-2437 made the "I am not watching this polecat" decline LOUD. It did not
// make the polecat watched. mg-560d then established that the hole is
// load-bearing for drellem2/macguffin#25: a `--no-worktree` polecat's cwd is a
// brand-new `~/.pogo/agents/<name>/`, which is untrusted, so Claude Code raises
// the workspace-trust dialog on EVERY spawn. That dialog renders no composer,
// so the prompt-ready sentinel never matches and the kickoff nudge is never
// accepted — and 560d measured that a bare CR (exactly what `a.Nudge("")`
// sends) DISMISSES it: dialog → composer at t=0.7s, nudge accepted. The
// recovery net was fully capable of rescuing those polecats and declined to
// watch them because they carried no `--id`.
//
// This suite drives the fallback started-signal at the code level with
// WorkItemID == "". It was written against the CURRENT behaviour first and
// confirmed RED (no keystroke delivered, decline reported) before the fix
// inverted it — a control only ever seen passing proves nothing.
//
// HONESTY BOUND: these are code-level controls. They exercise the decision the
// watcher makes when the ready sentinel is absent from the PTY buffer; they do
// NOT reproduce the trust dialog itself. Per this ticket's reproduction hazard,
// the dialog is masked machine-wide by a blanket `/` trust entry in
// ~/.claude.json, and `--env HOME=<scratch>` detaches credentials and stalls at
// "Not logged in · Run /login" instead — a false refutation. The end-to-end
// path through pogod's real spawn machinery has NOT been reproduced here.

// TestVerifyStartAndRenudge_NoWorkItemRecoversViaReadyComposer is the core
// control this ticket exists for.
//
// A polecat spawned `--no-worktree` with no `--id`, whose PTY never shows a
// ready composer (the trust dialog's signature), must now be RECOVERED — one
// bare CR per attempt, bounded — not merely reported as unwatched.
func TestVerifyStartAndRenudge_NoWorkItemRecoversViaReadyComposer(t *testing.T) {
	useTempEventLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "")
	// Screen shows the workspace-trust dialog: no composer, so no sentinel.
	a.outputBuf.Write([]byte("Quick safety check: Is this a project you created or one you trust?\n1. Yes, I trust it\n"))

	// A verifier is wired (this is a real daemon, not a bare test registry),
	// but there is no work item for it to answer about.
	verifier, count := countingVerifier([]verifyCall{{started: false}})
	reg := fastRenudgeRegistry(verifier, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "\r\r\r" {
		t.Errorf("a no---id polecat stuck without a ready composer must draw one bare CR per attempt; got %q", got)
	}
	// The claim verifier has nothing to answer about and must not be consulted.
	if c := count(); c != 0 {
		t.Errorf("expected the claim verifier never consulted with no work item, got %d", c)
	}
}

// TestVerifyStartAndRenudge_NoWorkItemReadyComposerNoRenudge is the other half
// of the gate: a no---id polecat that DID reach a ready composer is started by
// the fallback signal, so the watcher must never touch its PTY. Without this,
// "recover the no---id case" would degrade into spraying stray keystrokes at
// every healthy in-place dispatch.
func TestVerifyStartAndRenudge_NoWorkItemReadyComposerNoRenudge(t *testing.T) {
	useTempEventLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "")
	a.outputBuf.Write([]byte("\x1b[2m? for shortcuts\x1b[0m"))

	reg := fastRenudgeRegistry(func(string) (bool, error) { return false, nil }, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("a no---id polecat sitting at a ready composer must not be renudged, got %q", got)
	}
}

// TestVerifyStartAndRenudge_NoWorkItemReadyIsSticky: readiness observed once
// must stay observed. The ring buffer is bounded, so a busy agent can scroll
// its composer marker out of the retained window; re-reading the buffer alone
// would then flip a working agent back to "unstarted" and inject stray CRs.
// The initial-nudge path records the sighting (markPromptReady), and that
// record must outlive buffer eviction.
func TestVerifyStartAndRenudge_NoWorkItemReadyIsSticky(t *testing.T) {
	useTempEventLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "")
	a.markPromptReady()
	// Buffer now holds only post-ready chatter — the marker itself is gone.
	a.outputBuf.Write([]byte("working on it, no marker in sight"))

	reg := fastRenudgeRegistry(func(string) (bool, error) { return false, nil }, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("a once-ready agent must stay started after its marker scrolls away, got %q", got)
	}
}

// TestVerifyStartAndRenudge_NoWorkItemRecoveryEmitsEvent: the recovery must be
// visible in the event log with a reason that distinguishes it from the
// claim-signal path, so an operator can tell "this polecat was rescued off the
// weaker fallback signal" from "this one was rescued off its claim signal".
// The work_item_id field stays present-but-empty, since the whole point of
// this path is that there is no work item.
func TestVerifyStartAndRenudge_NoWorkItemRecoveryEmitsEvent(t *testing.T) {
	eventLog := useTempEventLog(t)

	a, _, _ := newRenudgeTestAgent(t, "")
	a.outputBuf.Write([]byte("Quick safety check: Is this a project you created or one you trust?"))
	reg := fastRenudgeRegistry(func(string) (bool, error) { return false, nil }, 1)

	reg.verifyStartAndRenudge(a)

	ev := findEvent(readEventLines(t, eventLog), "auto_renudge", "pogod")
	if ev == nil {
		t.Fatalf("expected an auto_renudge event for the fallback recovery; got %v",
			readEventLinesIfAny(t, eventLog))
	}
	details, _ := ev["details"].(map[string]any)
	if details["reason"] != "no_ready_composer" {
		t.Errorf("fallback recovery needs its own reason, got details=%v", details)
	}
	if details["work_item_id"] != "" {
		t.Errorf("fallback recovery has no work item, got details=%v", details)
	}
	if details["to"] != a.eventAgent() {
		t.Errorf("event must name the recovered agent, got details=%v", details)
	}
}

// TestVerifyStartAndRenudge_NoWorkItemRecoveryIsAnnounced: engaging the weaker
// signal is an operator-relevant fact — the watcher is running on a proxy
// ("the screen never rendered a composer"), not on the hard claim signal. It
// must say so by name, and it must still point at the `--id` remedy that would
// give it the strong signal.
func TestVerifyStartAndRenudge_NoWorkItemRecoveryIsAnnounced(t *testing.T) {
	useTempEventLog(t)
	logged := captureLog(t)

	a, _, _ := newRenudgeTestAgent(t, "")
	a.outputBuf.Write([]byte("Quick safety check"))
	reg := fastRenudgeRegistry(func(string) (bool, error) { return false, nil }, 1)

	reg.verifyStartAndRenudge(a)

	out := logged()
	if !strings.Contains(out, a.Name) {
		t.Errorf("fallback watch must name the agent %q; log was:\n%s", a.Name, out)
	}
	if !strings.Contains(out, "--id") {
		t.Errorf("fallback watch must still point at the --id remedy; log was:\n%s", out)
	}
	// It is watched now, so the old flat "UNWATCHED" alarm must NOT fire.
	if strings.Contains(out, "UNWATCHED") {
		t.Errorf("a polecat recovered by the fallback signal is watched, not UNWATCHED; log was:\n%s", out)
	}
}

// TestVerifyStartAndRenudge_NoSentinelStillDeclinesLoudly keeps the honest
// decline for the case that genuinely has no signal at all. A provider with no
// prompt-ready sentinel (e.g. Codex, whose ratatui composer has no stable
// marker) gives the fallback nothing to observe, so the decline — and mg-2437's
// loud report — must survive, with a reason naming the real gap.
func TestVerifyStartAndRenudge_NoSentinelStillDeclinesLoudly(t *testing.T) {
	eventLog := useTempEventLog(t)
	logged := captureLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "")
	a.nudge.PromptReadySentinel = ""
	a.nudge.PromptReadyAlternates = nil

	reg := fastRenudgeRegistry(func(string) (bool, error) { return false, nil }, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("no signal at all means no renudge, got %q", got)
	}
	if out := logged(); !strings.Contains(out, "UNWATCHED") || !strings.Contains(out, a.Name) {
		t.Errorf("a genuinely signal-less polecat must still be reported loudly; log was:\n%s", out)
	}
	ev := findEvent(readEventLines(t, eventLog), "agent_unwatched", "pogod")
	if ev == nil {
		t.Fatalf("expected agent_unwatched when no fallback signal exists")
	}
	details, _ := ev["details"].(map[string]any)
	if details["reason"] != "no_ready_signal" {
		t.Errorf("decline reason must name the real gap (no observable signal), got details=%v", details)
	}
}

// TestVerifyStartAndRenudge_BareRegistryStillDeclines guards the boundary the
// ticket called out explicitly: the fix must not start renudging the cases the
// early return legitimately covered. A registry with no start verifier is a
// bare/unit-test registry (pogod wires one at startup), and the fallback must
// not fire there — no verifier means nothing on this daemon is watching
// anything, which is a different fault with its own reason.
func TestVerifyStartAndRenudge_BareRegistryStillDeclines(t *testing.T) {
	eventLog := useTempEventLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "")
	a.outputBuf.Write([]byte("Quick safety check"))
	reg := fastRenudgeRegistry(nil, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("a bare registry must not renudge, got %q", got)
	}
	ev := findEvent(readEventLines(t, eventLog), "agent_unwatched", "pogod")
	if ev == nil {
		t.Fatalf("expected agent_unwatched for a bare registry")
	}
	details, _ := ev["details"].(map[string]any)
	if details["reason"] != "no_start_verifier" {
		t.Errorf("bare-registry decline keeps its own reason, got details=%v", details)
	}
}

// TestVerifyStartAndRenudge_CrewNoWorkItemNotWatched keeps the crew exemption.
// Crew agents never carry a work item by design, are long-lived, and are
// respawned and nudged on their own cycle. The polecat-vs-crew seam
// reportUnwatched already draws is the right one for the fallback too:
// renudging crew would inject stray keystrokes into every crew spawn forever.
func TestVerifyStartAndRenudge_CrewNoWorkItemNotWatched(t *testing.T) {
	useTempEventLog(t)

	a, readAll, _ := newRenudgeTestAgent(t, "")
	a.Type = TypeCrew
	a.outputBuf.Write([]byte("Quick safety check"))
	reg := fastRenudgeRegistry(func(string) (bool, error) { return false, nil }, 3)

	reg.verifyStartAndRenudge(a)

	if got := readAll(); got != "" {
		t.Errorf("crew must never be renudged by the fallback, got %q", got)
	}
}

// TestVerifyStartAndRenudge_NoWorkItemStopsOnceComposerAppears is the intended
// recovery shape, and the one 560d measured against the live dialog: a CR
// dismisses the trust dialog, the composer renders, and the watcher stops
// rather than spending its remaining attempts on a recovered agent.
func TestVerifyStartAndRenudge_NoWorkItemStopsOnceComposerAppears(t *testing.T) {
	useTempEventLog(t)

	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	a := &Agent{
		Name:      "renudge-recover-test",
		Type:      TypePolecat,
		master:    pw,
		outputBuf: NewRingBuffer(1024),
		done:      make(chan struct{}),
		nudge:     DefaultNudgeProfile,
	}
	readAll := func() string {
		_ = pw.Close()
		b, _ := io.ReadAll(pr)
		_ = pr.Close()
		return string(b)
	}
	a.outputBuf.Write([]byte("Quick safety check: Is this a project you created or one you trust?"))

	// The composer renders as soon as the first recovery CR lands. Wired off
	// the watcher's own keystroke — the screen changes BECAUSE of the recovery,
	// which is what 560d observed (dialog → composer at t=0.7s).
	reg := fastRenudgeRegistry(func(string) (bool, error) { return false, nil }, 3)

	// Block on the PTY until the first CR actually arrives, then render the
	// composer. Reacting to the keystroke rather than to a timer keeps this
	// deterministic — no sleep, no race with the verify delay.
	rendered := make(chan struct{})
	go func() {
		defer close(rendered)
		buf := make([]byte, 1)
		if _, err := pr.Read(buf); err != nil {
			return
		}
		a.outputBuf.Write([]byte("? for shortcuts"))
	}()

	reg.verifyStartAndRenudge(a)

	// Bounded wait: if the watcher declined entirely (the pre-fix behaviour),
	// no CR ever arrives and the reader is still blocked. Fail loudly rather
	// than hanging the suite.
	select {
	case <-rendered:
	case <-time.After(2 * time.Second):
		_ = pw.Close()
		t.Fatalf("no recovery CR was ever delivered — the dialog was never dismissed")
	}

	if got := readAll(); got != "" {
		t.Errorf("expected no CR beyond the one that dismissed the dialog, got %q extra", got)
	}
}
