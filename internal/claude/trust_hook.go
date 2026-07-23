package claude

import (
	"log"
	"regexp"
	"strings"
	"time"

	"github.com/drellem2/pogo/internal/agent"
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

// matchesTrustDialog reports whether PTY output contains Claude's trust dialog.
// It strips ANSI escapes and then all whitespace before matching — see
// trustDialogMarker for why the whitespace must go.
func matchesTrustDialog(output []byte) bool {
	clean := agent.StripANSI(output)
	collapsed := strings.Join(strings.Fields(string(clean)), "")
	return trustDialogMarker.MatchString(collapsed)
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
	clean := string(agent.StripANSI(output))
	for _, s := range readySentinels() {
		if s != "" && strings.Contains(clean, s) {
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

// TrustDialogHook returns a PostSpawnHook that auto-dismisses Claude Code's
// workspace trust dialog by monitoring PTY output and sending Enter.
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
			// spawn the hook resolves well inside the window (dialog dismissed,
			// or composer seen on an already-trusted worktree), so a deadline
			// hit is the drift signature: a hardcoded UI string has probably
			// changed, leaving trust-dialog dismissal unguarded. Record it so a
			// fleet-wide run of these goes loud (mg-ce4c / mg-ff2c).
			agent.RecordTrustDialogReady(a.ProviderID(), agent.DefaultNudgeProfile.PromptReadySentinel, false)
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
			// before the echoed kickoff prompt can be mistaken for the dialog,
			// and return early on an already-trusted worktree instead of
			// polling out the full budget. See composerReady.
			if composerReady(output) {
				agent.RecordTrustDialogReady(a.ProviderID(), agent.DefaultNudgeProfile.PromptReadySentinel, true)
				return
			}
			if matchesTrustDialog(output) {
				log.Printf("agent %s: detected workspace trust dialog, auto-accepting", a.Name)
				// Small delay to let the TUI fully render before sending input.
				time.Sleep(300 * time.Millisecond)
				if err := a.SendRaw("\r"); err != nil {
					log.Printf("agent %s: failed to dismiss trust dialog: %v", a.Name, err)
				}
				// The trust-dialog marker matched and we acted on it — the
				// sentinel is live. Record a confirmed outcome.
				agent.RecordTrustDialogReady(a.ProviderID(), agent.DefaultNudgeProfile.PromptReadySentinel, true)
				return
			}
		}
	}
}
