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

// watchForTrustDialog is TrustDialogHook's body with the timing injected, so
// tests can drive the real loop against a real PTY on a millisecond budget
// instead of waiting out the production one. Mirrors claude's split for the
// same reason.
func watchForTrustDialog(a *agent.Agent, budget, poll time.Duration) {
	deadline := time.After(budget)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			// Watched the whole window and matched NEITHER sentinel — neither
			// the trust-dialog marker nor the composer-ready marker. On a
			// healthy spawn the hook resolves well inside the window (dialog
			// dismissed ~0.7s, or composer seen on an already-trusted
			// worktree), so a deadline hit is the drift signature: a hardcoded
			// UI string has probably changed, leaving trust-dialog dismissal
			// unguarded. Record it so a fleet-wide run of these goes loud
			// (mg-ce4c / mg-ff2c).
			agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, false)
			return
		case <-a.Done():
			// Agent exited mid-watch: inconclusive, not a ready-gate result.
			return
		case <-ticker.C:
			output := a.RecentOutput(composerScanBytes)
			if len(output) == 0 {
				continue
			}
			// The composer is up, so no dialog is blocking. Stop scanning
			// before the echoed task can be mistaken for the dialog, and
			// return early on an already-trusted worktree instead of polling
			// out the full timeout. See composerReady.
			if composerReady(output) {
				agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, true)
				return
			}
			if matchesTrustDialog(output) {
				log.Printf("agent %s: detected Cursor workspace-trust dialog, auto-accepting", a.Name)
				// Let the TUI finish rendering the dialog before answering.
				time.Sleep(300 * time.Millisecond)
				if err := a.SendRaw(trustDialogAccept); err != nil {
					log.Printf("agent %s: failed to dismiss Cursor trust dialog: %v", a.Name, err)
				}
				// The trust-dialog marker matched and we acted on it — the
				// sentinel is live. Record a confirmed outcome.
				agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, true)
				return
			}
		}
	}
}
