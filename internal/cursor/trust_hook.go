package cursor

import (
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// trustDialogMarker matches Cursor's workspace-trust dialog. Cursor shows it
// whenever it launches in a directory with no saved trust decision. Every
// polecat runs in a freshly-created worktree, so the dialog appears on every
// spawn.
//
// Neither non-interactive flag suppresses it: --force governs command approval
// ("Run Everything"), not workspace trust, and --trust is rejected outside
// --print/headless mode. Verified empirically against Cursor CLI
// 2026.07.09-a3815c0; see docs/investigations/cursor-nudge-calibration.md.
//
// The dialog body reads (Cursor 2026.07.09-a3815c0):
//
//	Workspace Trust Required
//	Cursor Agent can execute code and access files in this directory.
//	Do you trust the contents of this directory?
//	<path>
//	▶ [a] Trust this workspace
//	  [q] Quit
//	Use arrow keys to navigate, Enter to select, or press the key shown
//
// matchesTrustDialog strips the box border and collapses ALL whitespace before
// matching, so the marker is written whitespace-free. Cursor draws the dialog
// inside a box-drawing frame: at pogo's 200-column default each phrase fits on
// one line, but a narrower winsize (or a longer future body) wraps a phrase
// across two lines, interposing "│" and padding. Stripping the frame glyphs and
// the whitespace makes the pattern survive that. Two independent phrases are
// matched so a reword of either one alone still hits.
var trustDialogMarker = regexp.MustCompile(`(?i)workspacetrustrequired|trustthisworkspace`)

// boxDrawing removes the frame glyphs Cursor draws the dialog with, so a phrase
// wrapped across two boxed lines still collapses to a contiguous match.
var boxDrawing = strings.NewReplacer(
	"│", "", "─", "", "╭", "", "╮", "", "╰", "", "╯", "",
	"┌", "", "┐", "", "└", "", "┘", "", "├", "", "┤", "",
)

// collapse removes ALL whitespace from s. Both predicates below match against
// collapsed text: TUIs position footer and box text with per-word cursor-column
// moves, so once ANSI escapes are stripped the inter-word spaces can vanish
// ("Add a follow-up" -> "Addafollow-up"), and boxed phrases re-wrap at narrow
// winsizes. Collapsing both the haystack and the needle makes a marker match
// whichever way it was drawn, instead of only the spelling it was captured in.
// That trap has already bitten this subsystem once — gh#76 / mg-d06a.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), "")
}

// matchesTrustDialog reports whether PTY output contains Cursor's trust dialog.
// It strips ANSI escapes, then the box frame, then all whitespace, before
// matching — see trustDialogMarker for why.
func matchesTrustDialog(output []byte) bool {
	clean := agent.StripANSI(output)
	unboxed := boxDrawing.Replace(string(clean))
	return trustDialogMarker.MatchString(collapse(unboxed))
}

// composerReadySentinels is the marker set that proves Cursor's composer owns
// the screen — and therefore that the trust dialog does not. The dialog blocks
// the TUI, so no composer marker renders for as long as it is up (verified 3/3
// against a live, never-dismissed dialog).
//
// It is a SET rather than the single promptReadySentinel for two reasons.
//
// The load-bearing one is that the pre-turn placeholder is a WINDOW, not a
// permanent screen feature: Cursor REPLACES "Plan, search, build anything" with
// "Add a follow-up" the moment the first turn starts (measured; see
// docs/investigations/cursor-nudge-calibration.md). With only the pre-turn
// string in the set, a spawn whose composer->turn transition falls between two
// polls leaves the gate open for the rest of the budget — that is mg-9270, and
// composerScanBytes is its other half. The post-turn placeholder proves the
// composer at least as well as the pre-turn one does, arguably better: it means
// a turn is already running, so there is certainly no modal in the way.
//
// The second is reword-brittleness. A gate resting on ONE exact string stops
// matching the day the harness rephrases it, and claude's equivalent set
// (DefaultNudgeProfile.PromptReadyAlternates) already learned that lesson the
// expensive way. Both entries here are spelled naturally and collapsed at match
// time, so neither depends on how Cursor spaces them.
var composerReadySentinels = []string{
	// Pre-turn: the empty composer's placeholder. Also
	// Provider.Nudge.PromptReadySentinel — see promptReadySentinel.
	promptReadySentinel,
	// Post-turn: what Cursor swaps the placeholder for once a turn is running.
	"Add a follow-up",
}

// composerReady reports whether any composer marker has rendered, which proves
// the trust dialog is not on screen — see composerReadySentinels.
//
// This is the hook's false-positive guard, and it is load-bearing.
// trustDialogMarker matches on PTY *text*, and Cursor echoes the argv-delivered
// task into the TUI — so a work item whose body merely quotes the dialog ("[a]
// Trust this workspace") matches the marker. Without this guard, a spawn into an
// already-trusted worktree (Registry.Respawn re-enters the same Dir, and
// Cursor persists trust per workspace) would see no dialog, match the echoed
// task instead, and type a stray "a" into the live composer — corrupting the
// next nudge, whose body would arrive prefixed by it.
//
// mg-c146's own ticket body would have tripped this. So would mg-9270's, which
// quotes both placeholders — and that is the safe direction: a false ready
// verdict returns the hook without sending anything, which costs a visible
// stall, where a false dialog verdict costs a keystroke in a live composer.
func composerReady(output []byte) bool {
	collapsed := collapse(string(agent.StripANSI(output)))
	for _, s := range composerReadySentinels {
		if s != "" && strings.Contains(collapsed, collapse(s)) {
			return true
		}
	}
	return false
}

// composerScanBytes is how much PTY output each poll scans.
//
// It is the WHOLE ring, deliberately, and not a smaller slice of it. This gate
// used to read 8KB out of a 64KB ring while looking for a marker that is only on
// screen for a window (see composerReadySentinels), so a burst larger than the
// read could hide that marker from every single tick: the gate never closed, and
// the hook polled out its full budget — which is precisely the window in which an
// echoed task quoting the dialog can match trustDialogMarker. Reading everything
// still retained removes the burst's ability to hide the marker rather than
// betting on a bigger guess (mg-9270).
//
// Widening is safe because BOTH predicates read this same buffer and
// composerReady is checked first: any composer evidence in the window beats any
// dialog evidence in the same window, which is the ordering the gate already
// relies on. Cost is a 64KB copy plus one StripANSI pass per 250ms poll, for at
// most one spawn's cold-start budget — and a healthy spawn exits on its first or
// second tick.
const composerScanBytes = agent.OutputRingBytes

// TrustDialogPollInterval is how often to scan PTY output for the trust dialog.
const TrustDialogPollInterval = 250 * time.Millisecond

// TrustDialogTimeout bounds how long after spawn the hook watches for the
// dialog before giving up.
//
// It is Cursor's own cold-start budget, not an independent guess. The previous
// value was a fixed 12s, and that shape was the defect fixed for Claude in
// drellem2/pogo#91: the hook starts at spawn and gives up N seconds later, so on
// a CPU-starved host under concurrent spawns the dialog can render AFTER the
// hook has returned. Nothing then dismisses it — the composer never appears, the
// ready sentinel never matches, and the polecat hangs until a human answers the
// dialog by hand. A fixed wall-clock bound is the same SHAPE of defect as the
// one it is trying to catch, so a bigger number would move the failure rather
// than remove it.
//
// Sourcing the bound from initialNudgeTimeout means there is ONE cold-start
// budget rather than two that disagree. Watching longer is close to free because
// composerReady returns the hook early on every healthy spawn — the full budget
// is only ever spent when neither marker appears, which is the drift signature
// recorded at the deadline below.
const TrustDialogTimeout = initialNudgeTimeout

// trustDialogAccept is the key that accepts the dialog.
//
// It is the "a" accelerator, not the "\r" that claude.TrustDialogHook and
// codex.TrustDialogHook send. Cursor's dialog is a two-item menu — "[a] Trust
// this workspace" / "[q] Quit" — and Enter selects whatever is *highlighted*.
// Trust happens to be highlighted today, but if Cursor ever reorders the menu,
// Enter would quit the agent. "a" is bound to Trust explicitly, so the worst
// case of a Cursor UI change is a stalled (visible) spawn rather than a silently
// killed one.
const trustDialogAccept = "a"

// TrustDialogHook is the Cursor provider's PostSpawnHook. It auto-accepts
// Cursor's workspace-trust dialog by scanning PTY output and pressing "a", so a
// polecat is not blocked at startup in its fresh worktree.
//
// It mirrors claude.TrustDialogHook — the same spawn-scoped, poll-and-dismiss
// shape, bounded by the same provider's own cold-start budget — because Cursor
// needs the same treatment for an analogous dialog. The dialog renders ~0.7s
// after spawn and the composer settles ~2.3s after it is dismissed; the 250ms
// poll clears it well inside the initial-task path.
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
	// trustWatchConfirmed: the dialog was matched and accepted, or the composer
	// was seen (already-trusted worktree). Either way the sentinel is live.
	trustWatchConfirmed
)

func (o trustWatchOutcome) String() string {
	switch o {
	case trustWatchInconclusive:
		return "inconclusive"
	case trustWatchDrift:
		return "drift"
	case trustWatchConfirmed:
		return "confirmed"
	}
	return "unknown"
}

// watchForTrustDialog is TrustDialogHook's body with the timing injected, so
// tests can drive the real loop against a real PTY on a millisecond budget
// instead of waiting out the production one. Mirrors claude's split for the
// same reason. It runs the watch and records what it concluded.
//
// On a drift outcome: the hook watched the whole window and matched NEITHER
// sentinel — neither the trust-dialog marker nor the composer-ready marker. On a
// healthy spawn it resolves well inside the window (dialog dismissed ~0.7s, or
// composer seen on an already-trusted worktree), so a spent budget is the drift
// signature: a hardcoded UI string has probably changed, leaving trust-dialog
// dismissal unguarded. Record it so a fleet-wide run of these goes loud
// (mg-ce4c / mg-ff2c).
func watchForTrustDialog(a *agent.Agent, budget, poll time.Duration) {
	switch trustDialogWatch(a, budget, poll, time.Now) {
	case trustWatchConfirmed:
		agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, true)
	case trustWatchDrift:
		agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, false)
	}
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
func spentBudgetOutcome(done <-chan struct{}) trustWatchOutcome {
	select {
	case <-done:
		return trustWatchInconclusive
	default:
		return trustWatchDrift
	}
}

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
// gate alongside claude's and codex's, all three sharing this loop shape
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
// instant no longer accepts a dialog it can see. That is what "the budget is
// spent" has to mean for the bound to be a bound at all — and in production the
// budget is the whole cold-start window, past which nothing is waiting for the
// composer anyway.
func trustDialogWatch(a *agent.Agent, budget, poll time.Duration, now func() time.Time) trustWatchOutcome {
	deadlineAt := now().Add(budget)
	wakeup := time.After(budget)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-wakeup:
			return spentBudgetOutcome(a.Done())
		case <-a.Done():
			// Agent exited mid-watch: inconclusive, not a ready-gate result.
			return trustWatchInconclusive
		case <-ticker.C:
			// The tick is only a wakeup hint; the budget decides.
			if !now().Before(deadlineAt) {
				return spentBudgetOutcome(a.Done())
			}
			output := a.RecentOutput(composerScanBytes)
			if len(output) == 0 {
				continue
			}
			// The composer is up, so no dialog is blocking. Stop scanning
			// before the echoed task can be mistaken for the dialog, and
			// return early on an already-trusted worktree instead of polling
			// out the full timeout. See composerReady.
			if composerReady(output) {
				return trustWatchConfirmed
			}
			if matchesTrustDialog(output) {
				log.Printf("agent %s: detected Cursor workspace-trust dialog, auto-accepting", a.Name)
				// Let the TUI finish rendering the dialog before answering.
				time.Sleep(300 * time.Millisecond)
				if err := a.SendRaw(trustDialogAccept); err != nil {
					log.Printf("agent %s: failed to dismiss Cursor trust dialog: %v", a.Name, err)
				}
				// The trust-dialog marker matched and we acted on it — the
				// sentinel is live.
				return trustWatchConfirmed
			}
		}
	}
}
