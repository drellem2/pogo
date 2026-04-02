package agent

import (
	"bytes"
	"log"
	"regexp"
	"time"
)

// trustDialogMarker matches the Claude Code workspace trust dialog.
// Claude Code shows "Quick safety check: Is this a project you created or
// one you trust?" when launched in a directory it hasn't seen before.
// The --dangerously-skip-permissions flag does NOT suppress this dialog
// (it only covers tool execution permissions, not the workspace trust check).
var trustDialogMarker = regexp.MustCompile(`(?i)safety.check`)

// TrustDialogPollInterval is how often to check PTY output for the trust dialog.
const TrustDialogPollInterval = 500 * time.Millisecond

// TrustDialogTimeout is how long to watch for the trust dialog after spawn.
// Claude Code typically shows the dialog within the first few seconds of startup.
const TrustDialogTimeout = 8 * time.Second

// watchAndDismissTrustDialog monitors the agent's PTY output during startup
// and automatically dismisses Claude Code's workspace trust dialog if detected.
//
// The trust dialog asks "Quick safety check: Is this a project you created or
// one you trust?" and blocks all agent interaction until answered. This function
// detects the dialog by matching the output text and sends Enter (\r) to accept
// the default "Yes" option.
//
// This runs as a goroutine started during Spawn. It exits after the dialog is
// dismissed or after TrustDialogTimeout (whichever comes first).
func (a *Agent) watchAndDismissTrustDialog() {
	deadline := time.After(TrustDialogTimeout)
	ticker := time.NewTicker(TrustDialogPollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-deadline:
			return
		case <-a.done:
			return
		case <-ticker.C:
			output := a.RecentOutput(4096)
			if len(output) == 0 {
				continue
			}
			clean := StripANSI(output)
			if trustDialogMarker.Match(clean) {
				log.Printf("agent %s: detected workspace trust dialog, auto-accepting", a.Name)
				// Small delay to let the TUI fully render before sending input.
				time.Sleep(300 * time.Millisecond)
				if err := a.sendRaw("\r"); err != nil {
					log.Printf("agent %s: failed to dismiss trust dialog: %v", a.Name, err)
				}
				return
			}
		}
	}
}

// sendRaw writes raw bytes to the PTY master without appending \r.
// Used for sending individual keypresses like Enter.
func (a *Agent) sendRaw(s string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.master == nil {
		return nil
	}
	_, err := a.master.WriteString(s)
	return err
}

// OutputContains checks if the agent's recent output contains the given
// byte sequence. Useful for detecting specific prompts or states.
func (a *Agent) OutputContains(marker []byte, n int) bool {
	output := a.RecentOutput(n)
	clean := StripANSI(output)
	return bytes.Contains(clean, marker)
}
