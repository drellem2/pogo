package agent

// Installation of the harness-side half of the submission receipt.
//
// pogod cannot observe a prompt being submitted; only the harness can. So the
// harness is asked to say so — a hook it runs on every submitted prompt, whose
// whole job is to append one line to this agent's receipt file. The pogod side
// then reads a number instead of guessing from quiescence.
//
// Every step here is allowed to fail into "no receipt signal", which switches
// confirmed delivery back to the pre-existing wait-idle behaviour for that
// agent. That is deliberate: a half-installed hook must degrade to what pogo
// did yesterday, never to an escalation firing against an agent whose silence
// means nothing.

import (
	"log"
	"os"
	"os/exec"
	"path/filepath"
)

// installReceiptHook asks the provider to install its prompt-submission hook
// into the agent's working directory and returns the receipt file the hook will
// write to, or "" when no signal is available (no provider, no hook for this
// provider, no resolvable pogo binary, or the install failed).
//
// A returned path is also RESET: any receipt left by a previous agent of the
// same name is removed, so the count a nudge compares against belongs to the
// live process.
func installReceiptHook(p *Provider, name, dir string) string {
	if p == nil || p.SubmitReceiptHook == nil || name == "" {
		return ""
	}

	// No binary to run means the hook would be installed and never fire, and a
	// hook that never fires is indistinguishable on disk from a message that
	// was dropped. Refuse to install rather than manufacture a signal that
	// lies in exactly the direction that causes a duplicate resend.
	bin := pogoBinaryPath()
	if bin == "" {
		log.Printf("agent %s: no pogo binary on PATH; skipping submission-receipt hook "+
			"(nudges fall back to wait-idle delivery)", name)
		return ""
	}

	receipt := SubmitReceiptPath(name)
	if err := ResetReceipt(receipt); err != nil {
		log.Printf("agent %s: could not reset submission receipt %s: %v "+
			"(nudges fall back to wait-idle delivery)", name, receipt, err)
		return ""
	}

	if err := p.SubmitReceiptHook(dir, bin+" hook prompt-submit"); err != nil {
		log.Printf("agent %s: could not install submission-receipt hook: %v "+
			"(nudges fall back to wait-idle delivery)", name, err)
		return ""
	}
	return receipt
}

// pogoBinaryPath resolves the `pogo` CLI the harness hook will invoke.
//
// The sibling of the running executable is preferred over PATH: pogod and pogo
// ship and are installed together, so a daemon running from a build directory
// must hand its agents that build's pogo, not whatever an older install left on
// PATH. Returns "" when neither can be found.
func pogoBinaryPath() string {
	if exe, err := os.Executable(); err == nil {
		sibling := filepath.Join(filepath.Dir(exe), "pogo")
		if fi, err := os.Stat(sibling); err == nil && !fi.IsDir() && fi.Mode()&0o111 != 0 {
			return sibling
		}
	}
	if path, err := exec.LookPath("pogo"); err == nil {
		return path
	}
	return ""
}
