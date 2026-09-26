package claude

import (
	"context"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
)

// trustDialogMarker matches the Claude Code workspace trust dialog.
// Claude Code shows "Quick safety check: Is this a project you created or
// one you trust?" when launched in a directory it hasn't seen before.
// The --dangerously-skip-permissions flag does NOT suppress this dialog.
//
// The marker is written whitespace-free and matched against collapsed text —
// see matchesTrustDialog. Claude Code's Ink TUI positions footer text with
// per-word cursor-column moves (ESC[<n>G), so once ANSI escapes are stripped
// the inter-word spaces can vanish ("safety check" -> "safetycheck"). The
// previous spaced-tolerant pattern (`safety.check`) required exactly one
// character between the words and would silently stop matching if the dialog
// were drawn that way — the same space-collapse trap that broke the
// prompt-ready sentinel in gh#76 / mg-d06a. codex and cursor both already
// collapse before matching; this brings Claude in line.
var trustDialogMarker = regexp.MustCompile(`(?i)safetycheck`)

// collapse removes ALL whitespace from s. Both predicates below match against
// collapsed text — see trustDialogMarker for why the whitespace must go, and
// composerReady for why the sentinels are collapsed too rather than only the
// output.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// matchesTrustDialog reports whether PTY output contains Claude's trust dialog.
// It strips ANSI escapes and then all whitespace before matching — see
// trustDialogMarker for why the whitespace must go.
func matchesTrustDialog(output []byte) bool {
	return trustDialogMarker.MatchString(collapse(string(agent.StripANSI(output))))
}

// trustRow is what the highlighted row of the dialog would do if Enter were
// pressed on it.
type trustRow int

const (
	// trustRowNone: the rows are not on screen yet, or no row carries the
	// highlight. Nothing to act on this tick.
	trustRowNone trustRow = iota
	// trustRowAccept: the highlight is on the row that trusts the folder.
	trustRowAccept
	// trustRowRefuse: the highlight is on the row that exits Claude Code.
	// Enter here exits the harness with status 1 (2.1.283, live, mg-c1e2).
	trustRowRefuse
	// trustRowUnknown: a highlighted row whose label is neither. The hook must
	// not guess what Enter does on it.
	trustRowUnknown
)

// trustAcceptLabels and trustRefuseLabels are the dialog's row labels,
// whitespace-collapsed and lower-cased, with the highlight glyph and any "N."
// ordinal removed. "yes,itrustthisfolder"/"no,exit" are Claude Code 2.1.283,
// captured from a live PTY in a fresh `git init` dir (mg-f394), where the rows
// carry no ordinal and "No, exit" is drawn FIRST and highlighted — which is
// drellem2/pogo#177. "yes,proceed" is the accept row of the numbered dialog
// earlier releases drew ("❯ 1. Yes, proceed / 2. No, exit"); it is not
// re-measured here, and it is safe to carry only because an unrecognised label
// fails closed.
var (
	trustAcceptLabels = []string{"yes,itrustthisfolder", "yes,proceed"}
	trustRefuseLabels = []string{"no,exit"}
)

// trustHighlight is the glyph Claude Code draws at the start of the highlighted
// row.
const trustHighlight = "❯"

// trustRowOrdinal strips an optional "1." from a collapsed row.
var trustRowOrdinal = regexp.MustCompile(`^\d+\.`)

// maxTrustRowSpan bounds how far from the accept row the highlight may sit and
// still be read as belonging to the same dialog. The dialog has two adjacent
// rows; the slack covers a blank separator line and nothing else.
const maxTrustRowSpan = 3

// trustRowText returns a screen row's collapsed, lower-cased label and whether
// the row carries the highlight.
func trustRowText(line string) (label string, highlighted bool) {
	c := strings.ToLower(collapse(line))
	if strings.HasPrefix(c, trustHighlight) {
		highlighted = true
		c = strings.TrimPrefix(c, trustHighlight)
	}
	return trustRowOrdinal.ReplaceAllString(c, ""), highlighted
}

func classifyTrustLabel(label string) trustRow {
	for _, l := range trustAcceptLabels {
		if label == l {
			return trustRowAccept
		}
	}
	for _, l := range trustRefuseLabels {
		if label == l {
			return trustRowRefuse
		}
	}
	return trustRowUnknown
}

// highlightedTrustRow classifies the row Claude Code currently highlights, and
// says how far the highlight is from the trusting row: move > 0 means that many
// rows DOWN, move < 0 that many UP, 0 that it is already there (or that there
// is nothing to move to). label is the highlighted row's collapsed label, for
// logging.
//
// This is the label-select half of the #177 remedy (mg-c1e2): Enter is pressed
// only on a row identified by its LABEL, and the direction to move is read from
// where the two labels were DRAWN, never assumed. A fixed "\x1b[B\r" was the
// rejected alternative — it is positional, silently re-breaks the day the rows
// are reordered back, and moves off the right row when the highlight already
// sits on it.
//
// It reads the replayed screen (screenLines), not the stripped stream, because
// a highlight move redraws the glyph and not the label — see screenLines. The
// LAST accept row on the screen is the current dialog; a highlight is read only
// from rows within maxTrustRowSpan of it.
func highlightedTrustRow(output []byte) (row trustRow, label string, move int) {
	lines := screenLines(output)
	accept := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if l, _ := trustRowText(lines[i]); classifyTrustLabel(l) == trustRowAccept {
			accept = i
			break
		}
	}
	if accept < 0 {
		// No trusting row drawn. If a highlighted row IS drawn, the dialog is
		// up and its wording is not one this hook knows — fail closed rather
		// than wait politely for a label that will never come.
		for i := len(lines) - 1; i >= 0; i-- {
			if l, h := trustRowText(lines[i]); h && l != "" {
				if r := classifyTrustLabel(l); r == trustRowRefuse {
					// The refusing row alone, its partner not drawn yet.
					return trustRowNone, l, 0
				}
				return trustRowUnknown, l, 0
			}
		}
		return trustRowNone, "", 0
	}
	// Nearest highlight to the accept row, the accept row itself first.
	for d := 0; d <= maxTrustRowSpan; d++ {
		for _, i := range []int{accept - d, accept + d} {
			if i < 0 || i >= len(lines) {
				continue
			}
			l, h := trustRowText(lines[i])
			if !h {
				continue
			}
			r := classifyTrustLabel(l)
			if r == trustRowRefuse {
				return r, l, accept - i
			}
			return r, l, 0
		}
	}
	return trustRowNone, "", 0
}

// composerReady reports whether Claude's composer has rendered, which proves
// the trust dialog is not on screen: the dialog blocks the TUI, and the
// ready-markers are absent for as long as it is up (that is precisely why
// DefaultNudgeProfile uses them to gate the initial nudge).
//
// It reuses the nudge profile's ready-sentinel set rather than hardcoding a
// second copy, so there is ONE definition of "Claude's composer is up" and a
// harness reword only has to be tracked in one place. The alternates are
// deliberately spaceless — see DefaultNudgeProfile.PromptReadyAlternates.
//
// Both the output and each sentinel are collapsed before matching, so a marker
// matches whichever way the TUI spaced it. That is not cosmetic: the set mixes
// spellings on purpose — the primary sentinel is spaced ("? for shortcuts")
// because that is how older Claude Code drew it, while the alternates are
// spaceless because v2.1.x positions the footer with per-word cursor-column
// moves. Matching raw, each spelling only ever hit its own era's rendering.
// Collapsing both sides makes every sentinel cover both. Same trap as
// gh#76 / mg-d06a, and the same fix.
//
// The set already spans the composer->turn transition — "shift+tabtocycle" and
// "?forshortcuts" are mode-bar markers, not properties of an EMPTY composer — so
// claude's half of mg-9270 is composerScanBytes, not a new sentinel. cursor,
// whose gate rested on a single placeholder that a turn REPLACES, needed both.
//
// This is the hook's false-positive guard, and extending the watch window
// (below) makes it load-bearing. trustDialogMarker matches on PTY *text*, and
// Claude echoes its kickoff prompt into the TUI — so a work item whose body
// merely mentions a "safety check" matches the marker. While the hook only
// watched 8s it almost always expired before the prompt was echoed; watching
// for the full initial-nudge budget means it is now live at echo time. Without
// this guard, a spawn into an already-trusted worktree (Registry.Respawn
// re-enters the same Dir, and Claude persists trust per path in ~/.claude.json)
// would see no dialog, match the echoed prompt instead, and press Enter into
// the live composer — submitting a half-typed nudge. This is the same failure
// cursor.composerReady was added for; Claude is exposed to it the moment the
// window grows.
func composerReady(output []byte) bool {
	collapsed := collapse(string(agent.StripANSI(output)))
	for _, s := range readySentinels() {
		if s != "" && strings.Contains(collapsed, collapse(s)) {
			return true
		}
	}
	return false
}

// readySentinels is the composer-ready marker set: the nudge profile's primary
// sentinel followed by its alternates.
func readySentinels() []string {
	p := agent.DefaultNudgeProfile
	return append([]string{p.PromptReadySentinel}, p.PromptReadyAlternates...)
}

// composerScanBytes is how much PTY output each poll scans.
//
// It is the WHOLE ring, deliberately, and not a smaller slice of it. This gate
// used to read 8KB out of a 64KB ring, and a marker it misses is not a marker it
// gets to see again: a burst larger than the read hides the composer from every
// single tick, so the gate never closes and the hook polls out its full 60s
// budget — which is precisely the window in which an echoed kickoff prompt
// mentioning a "safety check" can match trustDialogMarker. Reading everything
// still retained removes the burst's ability to hide the marker rather than
// betting on a bigger guess (mg-9270).
//
// Widening is safe because BOTH predicates read this same buffer and
// composerReady is checked first: any composer evidence in the window beats any
// dialog evidence in the same window, which is the ordering the gate already
// relies on. Cost is a 64KB copy plus one StripANSI pass per 250ms poll, for at
// most one spawn's cold-start budget — and a healthy spawn exits on its first or
// second tick. Claude's Ink TUI repaints continuously, so it is the provider
// most able to produce the burst in the first place.
const composerScanBytes = agent.OutputRingBytes

// TrustDialogPollInterval is how often to check PTY output for the trust dialog.
// 250ms matches codex and cursor: the dialog is dismissed promptly rather than
// sitting up for as much as a half-second of the nudge's idle budget.
const TrustDialogPollInterval = 250 * time.Millisecond

// TrustDialogTimeout bounds how long after spawn the hook watches for the
// trust dialog before giving up.
//
// It is the initial nudge's own budget, not an independent guess. The previous
// value was a fixed 8s, and that was the defect: the hook started at spawn and
// gave up 8 seconds later, so on a CPU-starved host under concurrent spawns the
// dialog could render AFTER the hook had returned. Nothing then dismissed it —
// the composer never appeared, the ready sentinel never matched, the kickoff
// prompt was never delivered, and the polecat hung until a human typed 1
// (drellem2/macguffin#25; CloverRoss reproduced it 3/3, and their `nudge 1`
// rescue was literally answering this dialog).
//
// Sourcing the bound from DefaultNudgeProfile.InitialNudgeTimeout means there
// is ONE cold-start budget rather than two that disagree: the spawn path is
// already willing to wait this long for the composer, so the hook that unblocks
// the composer must not stop watching first. Watching longer is close to free
// because composerReady returns the hook early on every healthy spawn — the
// full budget is only ever spent when neither marker appears, which is the
// drift signature recorded below.
var TrustDialogTimeout = agent.DefaultNudgeProfile.InitialNudgeTimeout

// TrustDialogHook is the Claude provider's PostSpawnHook. It answers Claude
// Code's workspace-trust dialog by scanning PTY output and pressing Enter on the
// row it has identified BY LABEL as the trusting one.
//
// It used to press a bare Enter on whatever row was highlighted. Claude Code
// 2.1.270+ draws "No, exit" first and highlights it, so that Enter exited every
// spawn into an untrusted directory with status 1 — and the hook returned
// confirmed the instant it pressed the key, so nothing noticed
// (drellem2/pogo#177, reproduced on 2.1.283). Per mg-c1e2 it now:
//
//   - label-selects: Enter is sent only while highlightedTrustRow reports the
//     accept label; a highlight on the refusing row is moved one row toward the
//     trusting row (whichever direction the labels were drawn in) and the
//     screen is re-scanned before anything else is sent;
//   - fails closed: a highlight on a label that is neither gets no keystroke at
//     all, and the spent budget records drift;
//   - waits for the composer: after Enter the watch continues until the
//     composer is up. An exit in that window is trustWatchRefused — distinct
//     and loud, never folded into confirmed or drift.
func TrustDialogHook(a *agent.Agent) {
	watchForTrustDialog(a, TrustDialogTimeout, TrustDialogPollInterval)
}

// trustWatchOutcome is what one watch turned out to be. The loop decides it and
// its caller records it, so there is exactly ONE place that maps an outcome to a
// drift sample — and a test can assert the decision without having to observe the
// process-global drift detector.
type trustWatchOutcome int

const (
	// trustWatchInconclusive: the agent exited mid-watch. Not a ready-gate
	// result either way, so nothing is recorded.
	trustWatchInconclusive trustWatchOutcome = iota
	// trustWatchDrift: the budget was spent having matched NEITHER sentinel.
	trustWatchDrift
	// trustWatchConfirmed: the composer was seen — either after the dialog was
	// answered, or with no dialog at all (already-trusted worktree). Either way
	// the sentinel is live. Answering the dialog is NOT enough on its own: that
	// is what the old hook returned, over a harness its own Enter had just
	// killed (#177).
	trustWatchConfirmed
	// trustWatchRefused: the hook answered the dialog and the harness then
	// EXITED instead of showing the composer. The keystroke did the opposite of
	// what it was meant to. This is not a sentinel result — the marker matched —
	// so it never feeds the drift detector, and it must never be read as
	// confirmed: "spawn ok, dialog answered" over a dead harness is how #177
	// stayed invisible.
	trustWatchRefused
)

func (o trustWatchOutcome) String() string {
	switch o {
	case trustWatchInconclusive:
		return "inconclusive"
	case trustWatchDrift:
		return "drift"
	case trustWatchConfirmed:
		return "confirmed"
	case trustWatchRefused:
		return "refused"
	}
	return "unknown"
}

// watchForTrustDialog is TrustDialogHook's body with the timing injected, so
// tests can drive the real loop against a real PTY on a millisecond budget
// instead of waiting out the production one. It runs the watch and records what
// it concluded.
//
// On a drift outcome: the hook watched the whole window and matched NEITHER
// sentinel — neither the trust-dialog marker nor a composer-ready marker. On a
// healthy spawn it resolves well inside the window (dialog dismissed, or
// composer seen on an already-trusted worktree), so a spent budget is the drift
// signature: a hardcoded UI string has probably changed, leaving trust-dialog
// dismissal unguarded. Record it so a fleet-wide run of these goes loud
// (mg-ce4c / mg-ff2c).
func watchForTrustDialog(a *agent.Agent, budget, poll time.Duration) {
	switch trustDialogWatch(a, budget, poll, time.Now) {
	case trustWatchConfirmed:
		agent.RecordTrustDialogReady(a.ProviderID(), agent.DefaultNudgeProfile.PromptReadySentinel, true)
	case trustWatchDrift:
		agent.RecordTrustDialogReady(a.ProviderID(), agent.DefaultNudgeProfile.PromptReadySentinel, false)
	case trustWatchRefused:
		reportTrustRefused(a)
	}
}

// emitEvent is the event sink for trust_dialog_refused. A package var so the
// test binary can take it off the production events.log (see TestMain).
var emitEvent = func(ev events.Event) { events.Emit(context.Background(), ev) }

// reportTrustRefused makes a refused trust answer loud: a log line AND a durable
// trust_dialog_refused event. The log alone is not a signal on this host
// (pogod's stderr may not even reach pogod.log), and the spawn itself has
// already been reported ok by the time this fires.
func reportTrustRefused(a *agent.Agent) {
	log.Printf("agent %s: Claude Code EXITED after its workspace-trust dialog was answered — "+
		"the keystroke refused trust instead of accepting it; the spawn is dead", a.Name)
	emitEvent(events.Event{
		EventType:  "trust_dialog_refused",
		Agent:      a.EventAgent(),
		WorkItemID: a.WorkItemID,
		Details: map[string]any{
			"provider": a.ProviderID(),
		},
	})
}

// spentBudgetOutcome decides what a spent budget means. It is the one
// spent-budget path, reached from trustDialogWatch's wakeup arm and from any tick
// that finds the deadline already passed.
//
// It prefers an exited agent over the drift record for the same reason the
// deadline check exists: at the instant both are ready select tosses a coin, and
// routing more wakeups through one path would otherwise turn a mid-watch exit
// into a false drift sample about half the time. A watch that ends because its
// agent died says nothing about whether the sentinel is still the right string,
// so it must not be counted as evidence that it is not.
//
// Taking done as a parameter rather than reading a.Done() inline is what makes
// the preference testable at all: end-to-end, a closed done channel wins
// trustDialogWatch's outer select before the first tick even fires, so a
// scenario test never reaches this decision.
//
// answered is whether the hook already pressed Enter on the dialog: an exit
// after that is the refusal signature, not an inconclusive watch.
func spentBudgetOutcome(done <-chan struct{}, answered bool) trustWatchOutcome {
	select {
	case <-done:
		return exitOutcome(answered)
	default:
		return trustWatchDrift
	}
}

// exitOutcome is what an agent exit during the watch means. Before the dialog
// is answered it says nothing about the sentinels. After it, the harness quit
// in response to the hook's own keystroke — trustWatchRefused.
func exitOutcome(answered bool) trustWatchOutcome {
	if answered {
		return trustWatchRefused
	}
	return trustWatchInconclusive
}

// maxTrustRowMoves bounds how many arrow keys the hook sends to move the
// highlight off a refusing row. The dialog has two rows, so one move ought to
// be the whole job — and against Claude Code 2.1.283 it is not: in 10 of 10
// live runs in a fresh directory (mg-f394) the dialog moved the highlight to
// "Yes" on the first Down and then, unprompted, BACK to "No" a few hundred
// milliseconds later, before the hook's pre-Enter re-scan. The re-scan saw it,
// the next tick moved again, and every run was accepted on the second Down. A
// hook that trusted its own keystroke instead of re-reading the screen would
// have pressed Enter on "No, exit" in all ten — so the budget of moves and the
// re-scan are both load-bearing, not belt and braces.
// A highlight that still refuses after this many is something the hook does not
// understand, and it stops pressing keys.
const maxTrustRowMoves = 3

// Arrow keys, sent as their own writes, never glued to the Enter.
const (
	trustRowUp   = "\x1b[A"
	trustRowDown = "\x1b[B"
)

// trustDialogWatch is the poll loop, with the clock injected alongside the
// timing so a test can put it in the state a starved goroutine wakes into.
//
// The budget is held as an INSTANT (deadlineAt) and the real timer below is only
// a wakeup hint — the same shape dispatchScannerIdle uses for its idle window
// (mg-872b), and for the same reason. The previous loop selected over the
// deadline timer and ticker.C as EQUAL candidates, and Go picks uniformly at
// random among ready cases: a goroutine starved past its budget woke with both
// channels long ready and took the scan branch about half the time, answering a
// dialog the budget had already given up on. Because time.After delivers once
// while the ticker keeps firing, each iteration was a fresh coin flip, so under
// sustained starvation the wrong branch won nearly always — which is how
// TestLateRenderingDialogIsNeverDismissed failed a merge gate at 12/12 rather
// than at 50% (mg-effc). Measuring the deadline on a clock reading, in the
// branch that would otherwise act, makes the outcome independent of which of two
// ready channels the scheduler happened to pick.
//
// The deadline instant is immune to a wall-clock step: time.Now carries a
// monotonic reading, and Add and Before both use it, so an NTP correction during
// the watch can neither expire the budget early nor extend it. That matters
// because this replaces a time.After that had the same property — the fix must
// not trade a scheduling dependency for a clock-setting one.
//
// The narrowing this buys is deliberate: a tick that arrives after the deadline
// instant no longer dismisses a dialog it can see. That is what "the budget is
// spent" has to mean for the bound to be a bound at all — and in production the
// budget is the whole initial-nudge cold-start window, past which nothing is
// waiting for the composer anyway.
func trustDialogWatch(a *agent.Agent, budget, poll time.Duration, now func() time.Time) trustWatchOutcome {
	deadlineAt := now().Add(budget)
	wakeup := time.After(budget)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	// answered: Enter has been sent on the accept row. From here only the
	// composer (confirmed), an exit (refused) or the deadline (drift) ends the
	// watch.
	answered := false
	moves := 0
	// failClosedLogged keeps a dialog the hook will not answer to one log line
	// per spawn rather than one per tick.
	failClosedLogged := false

	for {
		select {
		case <-wakeup:
			return spentBudgetOutcome(a.Done(), answered)
		case <-a.Done():
			return exitOutcome(answered)
		case <-ticker.C:
			// The tick is only a wakeup hint; the budget decides.
			if !now().Before(deadlineAt) {
				return spentBudgetOutcome(a.Done(), answered)
			}
			output := a.RecentOutput(composerScanBytes)
			if len(output) == 0 {
				continue
			}
			// The composer is up, so no dialog is blocking. Stop scanning
			// before the echoed kickoff prompt can be mistaken for the dialog,
			// and return early on an already-trusted worktree instead of
			// polling out the full budget. After an answer, this is the
			// confirmation that the answer was accepted. See composerReady.
			if composerReady(output) {
				return trustWatchConfirmed
			}
			if answered || !matchesTrustDialog(output) {
				continue
			}
			row, label, move := highlightedTrustRow(output)
			switch row {
			case trustRowNone:
				// Body drawn, rows not yet: wait for the next tick.
			case trustRowRefuse:
				if moves < maxTrustRowMoves && move != 0 {
					moves++
					key := trustRowDown
					if move < 0 {
						key = trustRowUp
					}
					log.Printf("agent %s: trust dialog highlights refusing row %q, moving to the trusting row", a.Name, label)
					if err := a.SendRaw(key); err != nil {
						log.Printf("agent %s: failed to move trust-dialog highlight: %v", a.Name, err)
					}
				} else if !failClosedLogged {
					failClosedLogged = true
					log.Printf("agent %s: trust dialog still highlights refusing row %q after %d moves — sending nothing (fail closed)", a.Name, label, moves)
				}
			case trustRowUnknown:
				if !failClosedLogged {
					failClosedLogged = true
					log.Printf("agent %s: trust dialog highlights unrecognised row %q — sending nothing (fail closed); the dialog wording has probably changed", a.Name, label)
				}
			case trustRowAccept:
				log.Printf("agent %s: detected workspace trust dialog, accepting row %q", a.Name, label)
				// Let the TUI finish rendering before answering, then re-scan:
				// Enter goes only to a highlight that is STILL the accept row.
				time.Sleep(300 * time.Millisecond)
				if r, _, _ := highlightedTrustRow(a.RecentOutput(composerScanBytes)); r != trustRowAccept {
					continue
				}
				if err := a.SendRaw("\r"); err != nil {
					log.Printf("agent %s: failed to dismiss trust dialog: %v", a.Name, err)
					continue
				}
				answered = true
			}
		}
	}
}
