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
			return
		case <-a.Done():
			return
		case <-ticker.C:
			output := a.RecentOutput(8192)
			if len(output) == 0 {
				continue
			}
			if matchesTrustDialog(output) {
				log.Printf("agent %s: detected Cursor workspace-trust dialog, auto-accepting", a.Name)
				// Let the TUI finish rendering the dialog before answering.
				time.Sleep(300 * time.Millisecond)
				if err := a.SendRaw(trustDialogAccept); err != nil {
					log.Printf("agent %s: failed to dismiss Cursor trust dialog: %v", a.Name, err)
				}
				return
			}
		}
	}
}
