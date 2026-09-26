package codex

import (
	"context"
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
)

// trustDialogMarker matches Codex's directory-trust dialog. Codex shows it the
// first time it is launched in a directory that is not in its trusted-projects
// list (~/.codex/config.toml [projects."<path>"]). Every polecat runs in a
// freshly-created worktree, so the dialog appears on every spawn.
//
// --dangerously-bypass-approvals-and-sandbox does NOT suppress this dialog —
// it governs command approvals and the sandbox, not project trust. Verified
// empirically against Codex 0.132.0; see docs/investigations/codex-nudge-calibration.md.
//
// The dialog body reads (Codex 0.132.0):
//
//	"Working with untrusted contents comes with higher risk of prompt
//	 injection. Trusting the directory allows project-local config, ..."
//
// Codex draws that body glyph-by-glyph with cursor positioning, so once ANSI
// escapes are stripped the inter-word spaces are gone ("untrusted contents" ->
// "untrustedcontents"). matchesTrustDialog collapses ALL whitespace before
// matching, so the marker is written whitespace-free and works regardless of
// how Codex positioned the glyphs.
//
// Upstream Codex reworded the dialog after 0.132.0 (openai/codex#44732,
// 2026-09-11) to "Trust this folder? Codex can read, edit, and run files here,
// ...", which contains neither phrase above; `trustthisfolder\?codexcan` is that
// body. It is carried from the upstream source and render snapshot, NOT from a
// live PTY — the host runs 0.132.0 — which is acceptable only because the hook
// fails closed: a wrongly-matched body with no recognised highlighted row sends
// nothing (see highlightedTrustRow).
var trustDialogMarker = regexp.MustCompile(`(?i)untrustedcontents|trustingthe(directory|folder)|trustthisfolder\?codexcan`)

// trustRowStart finds the start of a highlighted dialog row in
// whitespace-collapsed text: Codex draws the selected row as
// "› 1. Yes, continue", which collapses to "›1.Yes,continue". The composer
// placeholder also starts with "›" but carries no "N." ordinal, so it cannot
// match.
var trustRowStart = regexp.MustCompile(`›\d+\.`)

// trustRowLabel reads the label that follows a trustRowStart match, up to the
// next numbered row, the next highlight, the footer, an escape byte, or the end
// of the text.
//
// The escape byte is load-bearing: agent.StripANSI leaves private-mode sequences
// in place, and Codex follows a highlight move with "\x1b[?25l\x1b[?2026l"
// (hide cursor, end synchronized update). When the moved row is the last thing
// drawn, those bytes would otherwise become part of its label — "no,quit\x1b[?25l…"
// — and the hook would fail closed on a row it should have moved. Caught live
// against Codex 0.132.0, not by the scripted fixture (mg-7511).
var trustRowLabel = regexp.MustCompile(`^›\d+\.(.*?)(?:\d+\.|›|press|enter|esc|\x1b|$)`)

// trustRow is what the highlighted row of the dialog would do if Enter were
// pressed on it.
type trustRow int

const (
	// trustRowNone: no highlighted numbered row is on screen yet.
	trustRowNone trustRow = iota
	// trustRowAccept: the highlight is on a row that trusts the directory.
	trustRowAccept
	// trustRowRefuse: the highlight is on a row that quits Codex. Enter here
	// exits the harness with status 0 (measured on 0.132.0).
	trustRowRefuse
	// trustRowUnknown: a highlighted row whose label is neither. The hook must
	// not guess what Enter does on it.
	trustRowUnknown
)

// trustAcceptLabels and trustRefuseLabels are the dialog's row labels,
// whitespace-collapsed and lower-cased. "yes,continue"/"no,quit" are Codex
// 0.132.0 (live PTY, mg-7511); "trustandcontinue"/"quit" are upstream after
// openai/codex#44732 (source + render snapshot only).
//
// Upstream's restricted variant ("Open restricted", shown only for a directory
// the user has explicitly marked untrusted) is deliberately in NEITHER set: it
// opens Codex without trusting the directory, which is not what a polecat's
// worktree needs, so it falls to trustRowUnknown and fails closed.
var (
	trustAcceptLabels = []string{"yes,continue", "trustandcontinue"}
	trustRefuseLabels = []string{"no,quit", "quit"}
)

// highlightedTrustRow classifies the MOST RECENT highlighted row in the PTY
// output, together with its collapsed label for logging. The ring buffer is
// chronological and Codex re-emits both rows whenever the highlight moves
// (measured on 0.132.0: after an arrow key the stream carries
// "1.Yes,continue›2.No,quit"), so the last match is the current state.
//
// This is the label-select half of the #177 remedy (mg-c1e2 / mg-f394) applied
// to Codex: Enter is pressed only on a row this function has identified by its
// LABEL, never on whatever row happens to be highlighted.
func highlightedTrustRow(output []byte) (trustRow, string) {
	collapsed := strings.ToLower(collapseWhitespace(agent.StripANSI(output)))
	starts := trustRowStart.FindAllStringIndex(collapsed, -1)
	if len(starts) == 0 {
		return trustRowNone, ""
	}
	// Locate the last highlight first, then read its label: a single
	// FindAll over label-and-terminator would consume the NEXT row's "›" as
	// a terminator and skip the highlight a move just drew.
	label := trustRowLabel.FindStringSubmatch(collapsed[starts[len(starts)-1][0]:])[1]
	for _, l := range trustAcceptLabels {
		if label == l {
			return trustRowAccept, label
		}
	}
	for _, l := range trustRefuseLabels {
		if label == l {
			return trustRowRefuse, label
		}
	}
	return trustRowUnknown, label
}

// matchesTrustDialog reports whether PTY output contains Codex's trust dialog.
// It strips ANSI escapes and then all whitespace before matching — see
// trustDialogMarker for why the whitespace must go.
func matchesTrustDialog(output []byte) bool {
	clean := agent.StripANSI(output)
	collapsed := strings.Join(strings.Fields(string(clean)), "")
	return trustDialogMarker.MatchString(collapsed)
}

// composerReady reports whether Codex's status box has rendered, which proves
// the trust dialog is not on screen: the dialog blocks the TUI and paints
// nothing else, so the box is absent for as long as it is up (verified 5/5
// against a live, never-dismissed dialog; present 8/8 once the composer is up).
//
// This is the hook's false-positive guard, and widening the watch window (below)
// makes it load-bearing. trustDialogMarker matches on PTY *text*, and the
// harness echoes the nudged task into the TUI — so a work item whose body merely
// quotes the dialog ("Working with untrusted contents") matches the marker. While
// the window was a fixed 12s the hook had almost always expired before the echo
// arrived; at the cold-start budget it is still watching. Without this guard, a
// spawn into an already-trusted directory (Registry.Respawn re-enters the same
// Dir, and Codex persists trust in ~/.codex/config.toml [projects."<path>"])
// would see no dialog, match the echoed task instead, and press Enter into a
// live composer — submitting a half-typed nudge. This is the same failure
// claude.composerReady and cursor.composerReady were added for; Codex is exposed
// to it the moment the window grows.
//
// mg-86e7's own ticket body would have tripped this.
//
// It reuses the provider's ready-sentinel set rather than hardcoding a second
// copy, so there is ONE definition of "Codex's composer is up". Both sides are
// whitespace-collapsed — including the sentinels — so the gate holds whichever
// render path Codex uses for the box; see promptReadyAlternates on the
// space-collapse trap.
func composerReady(output []byte) bool {
	collapsed := collapseWhitespace(agent.StripANSI(output))
	for _, s := range readySentinels() {
		if c := collapseWhitespace([]byte(s)); c != "" && strings.Contains(collapsed, c) {
			return true
		}
	}
	return false
}

// readySentinels is the composer-ready marker set: the provider's primary ready
// sentinel followed by its alternates. It reads the same consts
// Provider.Nudge is built from, so a Codex UI reword is tracked in one place.
func readySentinels() []string {
	return append([]string{promptReadySentinel}, promptReadyAlternates...)
}

// collapseWhitespace removes every run of whitespace, so a marker matches the
// text however the TUI positioned the glyphs.
func collapseWhitespace(b []byte) string {
	return strings.Join(strings.Fields(string(b)), "")
}

// TrustDialogPollInterval is how often to scan PTY output for the trust dialog.
const TrustDialogPollInterval = 250 * time.Millisecond

// TrustDialogTimeout bounds how long after spawn the hook watches for the
// dialog before giving up.
//
// It is the provider's own cold-start budget, not an independent guess. The
// previous value was a fixed 12 seconds, and that bound was the defect: the hook
// starts at spawn and gave up 12s later, so on a CPU-starved host under
// concurrent spawns the dialog could render AFTER the hook had returned. Nothing
// then dismissed it — the composer never appeared, the ready sentinel never
// matched, the initial nudge was never delivered, and the polecat hung until a
// human answered the dialog by hand (drellem2/pogo#91). A fixed wall-clock bound
// is the same SHAPE of defect as the one it is trying to catch, so a bigger
// number would move the failure rather than remove it.
//
// Sourcing the bound from initialNudgeTimeout means there is ONE cold-start
// budget rather than two that disagree. Watching longer is close to free because
// composerReady returns the hook early on every healthy spawn — the full budget
// is only ever spent when neither marker appears, which is the drift signature
// recorded at the deadline below.
//
// codex was the last of the three providers to get this, deliberately: the bound
// was never the hard part, the GUARD was. claude and cursor already had a
// measured composer-ready marker to gate the widened watch on, and codex's
// PromptReadySentinel was empty — so widening the window first would have left
// dismissal firing Enter at echoed task text. The marker was measured against a
// live Codex CLI under mg-86e7 before this bound moved; see composerReady and
// docs/investigations/codex-nudge-calibration.md.
const TrustDialogTimeout = initialNudgeTimeout

// TrustDialogHook is the Codex provider's PostSpawnHook. It auto-accepts
// Codex's directory-trust dialog by scanning PTY output and pressing Enter on
// the row it has identified BY LABEL as the trusting one, so a polecat is not
// blocked at startup in its fresh worktree.
//
// It used to press a bare Enter on whatever row was highlighted — the same
// keystroke drellem2/pogo#177 found selecting "No, exit" on Claude Code. Codex
// highlights the trusting row by default (0.132.0 live, git and non-git dirs;
// upstream main initialises `highlighted: TrustDirectorySelection::Trust`
// unconditionally), so the bare Enter was correct — but only by that default, and
// Enter on Codex's "No, quit" row exits the harness with status 0, which nothing
// here would have noticed (mg-7511). So, as mg-c1e2's remedy prescribes for
// Claude:
//
//   - label-select: Enter is sent only while highlightedTrustRow reports the
//     accept label; a highlight on the refusing row is moved with Up (which
//     highlights the trusting row in both 0.132.0 and upstream) and re-scanned;
//   - fail closed: a highlight that is neither label gets no keystroke at all,
//     and the spent budget records drift;
//   - composer-ready: after Enter the watch continues until the composer is
//     up, and an exit in that window is trustWatchRefused — distinct and loud,
//     never folded into confirmed or drift.
//
// It mirrors claude.TrustDialogHook and cursor.TrustDialogHook — the same
// spawn-scoped, poll-and-dismiss shape, bounded by the same provider's own
// cold-start budget — because Codex needs the same treatment for an analogous
// dialog. The poll is faster than Claude's (250ms) so the dialog is dismissed
// well before the initial nudge's wait-idle timer can elapse; see the
// IdleThreshold note in provider.go.
func TrustDialogHook(a *agent.Agent) {
	watchForTrustDialog(a, TrustDialogTimeout, TrustDialogPollInterval)
}

// trustWatchOutcome is what one watch turned out to be. The loop decides it and
// its caller records it, so there is exactly ONE place that maps an outcome to a
// drift sample — and a test can assert the decision without having to observe the
// process-global drift detector. Mirrors claude's split for the same reason.
type trustWatchOutcome int

const (
	// trustWatchInconclusive: the agent exited mid-watch. Not a ready-gate
	// result either way, so nothing is recorded.
	trustWatchInconclusive trustWatchOutcome = iota
	// trustWatchDrift: the budget was spent having matched NEITHER sentinel.
	trustWatchDrift
	// trustWatchConfirmed: the composer was seen — either after the dialog was
	// answered, or with no dialog at all (already-trusted directory). Either way
	// the sentinel is live.
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
// healthy spawn it resolves well inside the window (dialog dismissed ~0.3s, or
// composer seen ~190ms in on an already-trusted directory), so a spent budget is
// the drift signature: a hardcoded UI string has probably changed, leaving
// trust-dialog dismissal unguarded. Record it so a fleet-wide run of these goes
// loud (mg-ce4c / mg-ff2c).
func watchForTrustDialog(a *agent.Agent, budget, poll time.Duration) {
	switch trustDialogWatch(a, budget, poll, time.Now) {
	case trustWatchConfirmed:
		agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, true)
	case trustWatchDrift:
		agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, false)
	case trustWatchRefused:
		reportTrustRefused(a)
	}
}

// emitEvent is the event sink for trust_dialog_refused. A package var so the
// test binary can take it off the production events.log (see TestMain).
var emitEvent = func(ev events.Event) { events.Emit(context.Background(), ev) }

// reportTrustRefused makes a refused trust answer loud: a log line AND a durable
// trust_dialog_refused event. The log alone is not a signal on this host (pogod's
// stderr may not even reach pogod.log), and the spawn itself has already been
// reported ok by the time this fires.
func reportTrustRefused(a *agent.Agent) {
	log.Printf("agent %s: Codex EXITED after its directory-trust dialog was answered — "+
		"the keystroke refused trust instead of accepting it; the spawn is dead", a.Name)
	identity := "cat-" + a.Name
	if a.Type == agent.TypeCrew {
		identity = "crew-" + a.Name
	}
	emitEvent(events.Event{
		EventType:  "trust_dialog_refused",
		Agent:      identity,
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

// maxTrustRowMoves bounds how many times the hook moves the highlight off a
// refusing row. Up is idempotent on this dialog (it highlights the trusting
// row), so a repeat only covers a re-render that had not landed by the next
// tick; a highlight that still refuses after this many is something the hook
// does not understand, and it stops pressing keys.
const maxTrustRowMoves = 3

// trustRowUp is the Up-arrow sequence that moves the dialog's highlight onto
// the trusting row.
const trustRowUp = "\x1b[A"

// trustDialogWatch is the poll loop, with the clock injected alongside the
// timing so a test can put it in the state a starved goroutine wakes into.
//
// The budget is held as an INSTANT (deadlineAt) and the real timer below is only
// a wakeup hint — the same shape internal/claude's dispatchScannerIdle uses for
// its idle window (mg-872b), and for the same reason. The previous loop selected
// over the deadline timer and ticker.C as EQUAL candidates, and Go picks
// uniformly at random among ready cases: a goroutine starved past its budget woke
// with both channels long ready and took the scan branch about half the time,
// answering a dialog the budget had already given up on. Because time.After
// delivers once while the ticker keeps firing, each iteration was a fresh coin
// flip, so under sustained starvation the wrong branch won nearly always — which
// is how this package's TestLateRenderingDialogIsNeverDismissed failed a merge
// gate alongside claude's and cursor's, all three sharing this loop shape
// (mg-effc). Measuring the deadline on a clock reading, in the branch that would
// otherwise act, makes the outcome independent of which of two ready channels the
// scheduler happened to pick.
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
// budget is the whole cold-start window, past which nothing is waiting for the
// composer anyway.
func trustDialogWatch(a *agent.Agent, budget, poll time.Duration, now func() time.Time) trustWatchOutcome {
	deadlineAt := now().Add(budget)
	wakeup := time.After(budget)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	// answered: Enter has been sent on the accept row. From here only the
	// composer (confirmed), an exit (refused) or the deadline (drift) ends
	// the watch.
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
			output := a.RecentOutput(8192)
			if len(output) == 0 {
				continue
			}
			// The composer is up, so no dialog is blocking. Stop scanning
			// before the echoed task can be mistaken for the dialog, and
			// return early on an already-trusted directory instead of polling
			// out the full budget. After an answer, this is the confirmation
			// that the answer was accepted. See composerReady.
			if composerReady(output) {
				return trustWatchConfirmed
			}
			if answered || !matchesTrustDialog(output) {
				continue
			}
			row, label := highlightedTrustRow(output)
			switch row {
			case trustRowNone:
				// Body drawn, rows not yet: wait for the next tick.
			case trustRowRefuse:
				if moves < maxTrustRowMoves {
					moves++
					log.Printf("agent %s: Codex trust dialog highlights refusing row %q, moving to the trusting row", a.Name, label)
					if err := a.SendRaw(trustRowUp); err != nil {
						log.Printf("agent %s: failed to move Codex trust-dialog highlight: %v", a.Name, err)
					}
				} else if !failClosedLogged {
					failClosedLogged = true
					log.Printf("agent %s: Codex trust dialog still highlights refusing row %q after %d moves — sending nothing (fail closed)", a.Name, label, moves)
				}
			case trustRowUnknown:
				if !failClosedLogged {
					failClosedLogged = true
					log.Printf("agent %s: Codex trust dialog highlights unrecognised row %q — sending nothing (fail closed); the dialog wording has probably changed", a.Name, label)
				}
			case trustRowAccept:
				log.Printf("agent %s: detected Codex directory-trust dialog, accepting row %q", a.Name, label)
				// Let the TUI finish rendering the dialog before answering,
				// then re-scan: Enter goes only to a highlight that is STILL
				// the accept row.
				time.Sleep(300 * time.Millisecond)
				if r, _ := highlightedTrustRow(a.RecentOutput(8192)); r != trustRowAccept {
					continue
				}
				if err := a.SendRaw("\r"); err != nil {
					log.Printf("agent %s: failed to dismiss Codex trust dialog: %v", a.Name, err)
					continue
				}
				answered = true
			}
		}
	}
}
