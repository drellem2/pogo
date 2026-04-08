package claude

import (
	"log"
	"regexp"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// trustDialogMarker matches the Claude Code workspace trust dialog.
// Claude Code shows "Quick safety check: Is this a project you created or
// one you trust?" when launched in a directory it hasn't seen before.
// The --dangerously-skip-permissions flag does NOT suppress this dialog.
var trustDialogMarker = regexp.MustCompile(`(?i)safety.check`)

// TrustDialogPollInterval is how often to check PTY output for the trust dialog.
const TrustDialogPollInterval = 500 * time.Millisecond

// TrustDialogTimeout is how long to watch for the trust dialog after spawn.
const TrustDialogTimeout = 8 * time.Second

// TrustDialogHook returns a PostSpawnHook that auto-dismisses Claude Code's
// workspace trust dialog by monitoring PTY output and sending Enter.
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
			output := a.RecentOutput(4096)
			if len(output) == 0 {
				continue
			}
			clean := agent.StripANSI(output)
			if trustDialogMarker.Match(clean) {
				log.Printf("agent %s: detected workspace trust dialog, auto-accepting", a.Name)
				// Small delay to let the TUI fully render before sending input.
				time.Sleep(300 * time.Millisecond)
				if err := a.SendRaw("\r"); err != nil {
					log.Printf("agent %s: failed to dismiss trust dialog: %v", a.Name, err)
				}
				return
			}
		}
	}
}
