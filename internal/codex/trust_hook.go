package codex

import (
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// trustDialogMarker matches Codex's directory-trust dialog. Codex shows it the
// first time it is launched in a directory that is not in its trusted-projects
// list (~/.codex/config.toml [projects."<path>"]). Every polecat runs in a
// freshly-created worktree, so the dialog appears on every spawn.
//
// --dangerously-bypass-approvals-and-sandbox does NOT suppress this dialog —
// it governs command approvals and the sandbox, not project trust. Verified
// empirically against Codex 0.132.0; see docs/codex-nudge-calibration.md.
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
var trustDialogMarker = regexp.MustCompile(`(?i)untrustedcontents|trustingthe(directory|folder)`)

// matchesTrustDialog reports whether PTY output contains Codex's trust dialog.
// It strips ANSI escapes and then all whitespace before matching — see
// trustDialogMarker for why the whitespace must go.
func matchesTrustDialog(output []byte) bool {
	clean := agent.StripANSI(output)
	collapsed := strings.Join(strings.Fields(string(clean)), "")
	return trustDialogMarker.MatchString(collapsed)
}

// TrustDialogPollInterval is how often to scan PTY output for the trust dialog.
const TrustDialogPollInterval = 250 * time.Millisecond

// TrustDialogTimeout bounds how long after spawn the hook watches for the
// dialog before giving up.
const TrustDialogTimeout = 12 * time.Second

// TrustDialogHook is the Codex provider's PostSpawnHook. It auto-accepts
// Codex's directory-trust dialog by scanning PTY output and pressing Enter
// (the dialog defaults to "1. Yes, continue" and prompts "Press enter to
// continue"), so a polecat is not blocked at startup in its fresh worktree.
//
// It mirrors claude.TrustDialogHook — the same spawn-scoped, poll-and-dismiss
// shape — because Codex needs the same treatment for an analogous dialog. The
// poll is faster than Claude's (250ms) so the dialog is dismissed well before
// the initial nudge's wait-idle timer can elapse; see the IdleThreshold note
// in provider.go.
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
				log.Printf("agent %s: detected Codex directory-trust dialog, auto-accepting", a.Name)
				// Let the TUI finish rendering the dialog before answering.
				time.Sleep(300 * time.Millisecond)
				if err := a.SendRaw("\r"); err != nil {
					log.Printf("agent %s: failed to dismiss Codex trust dialog: %v", a.Name, err)
				}
				return
			}
		}
	}
}
