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

// matchesTrustDialog reports whether PTY output contains Cursor's trust dialog.
// It strips ANSI escapes, then the box frame, then all whitespace, before
// matching — see trustDialogMarker for why.
func matchesTrustDialog(output []byte) bool {
	clean := agent.StripANSI(output)
	unboxed := boxDrawing.Replace(string(clean))
	collapsed := strings.Join(strings.Fields(unboxed), "")
	return trustDialogMarker.MatchString(collapsed)
}

// composerReady reports whether Cursor's composer placeholder has rendered,
// which proves the trust dialog is not on screen: the dialog blocks the TUI,
// and the placeholder is absent for as long as it is up (verified 3/3 against a
// live, never-dismissed dialog).
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
// mg-c146's own ticket body would have tripped this.
func composerReady(output []byte) bool {
	return strings.Contains(string(agent.StripANSI(output)), promptReadySentinel)
}

// TrustDialogPollInterval is how often to scan PTY output for the trust dialog.
const TrustDialogPollInterval = 250 * time.Millisecond

// TrustDialogTimeout bounds how long after spawn the hook watches for the
// dialog before giving up.
const TrustDialogTimeout = 12 * time.Second

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
// It mirrors codex.TrustDialogHook — the same spawn-scoped, poll-and-dismiss
// shape — because Cursor needs the same treatment for an analogous dialog. The
// dialog renders ~0.7s after spawn and the composer settles ~2.3s after it is
// dismissed; the 250ms poll clears it well inside the initial-task path.
func TrustDialogHook(a *agent.Agent) {
	deadline := time.After(TrustDialogTimeout)
	ticker := time.NewTicker(TrustDialogPollInterval)
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
			output := a.RecentOutput(8192)
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
