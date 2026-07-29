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
var trustDialogMarker = regexp.MustCompile(`(?i)untrustedcontents|trustingthe(directory|folder)`)

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
// Codex's directory-trust dialog by scanning PTY output and pressing Enter
// (the dialog defaults to "1. Yes, continue" and prompts "Press enter to
// continue"), so a polecat is not blocked at startup in its fresh worktree.
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

// watchForTrustDialog is TrustDialogHook's body with the timing injected, so
// tests can drive the real loop against a real PTY on a millisecond budget
// instead of waiting out the production one.
func watchForTrustDialog(a *agent.Agent, budget, poll time.Duration) {
	deadline := time.After(budget)
	ticker := time.NewTicker(poll)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			// Watched the whole window and matched NEITHER sentinel — neither
			// the trust-dialog marker nor a composer-ready marker. On a healthy
			// spawn the hook resolves well inside the window (dialog dismissed
			// ~0.3s, or composer seen ~190ms in on an already-trusted
			// directory), so a deadline hit is the drift signature: a hardcoded
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
			// return early on an already-trusted directory instead of polling
			// out the full budget. See composerReady.
			if composerReady(output) {
				agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, true)
				return
			}
			if matchesTrustDialog(output) {
				log.Printf("agent %s: detected Codex directory-trust dialog, auto-accepting", a.Name)
				// Let the TUI finish rendering the dialog before answering.
				time.Sleep(300 * time.Millisecond)
				if err := a.SendRaw("\r"); err != nil {
					log.Printf("agent %s: failed to dismiss Codex trust dialog: %v", a.Name, err)
				}
				// The trust-dialog marker matched and we acted on it — the
				// sentinel is live. Record a confirmed outcome.
				agent.RecordTrustDialogReady(a.ProviderID(), promptReadySentinel, true)
				return
			}
		}
	}
}
