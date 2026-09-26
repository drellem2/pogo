package agent

import "log"

// A pre-composer gate is a screen a harness draws BEFORE its composer that pogo
// has recognised and deliberately will not answer, because the answer is the
// operator's to give. The first one is Claude Code's "Detected a custom API
// key" prompt (mg-2037, internal/claude/apikeygate.go): its rows are two
// billing choices, and "No (recommended)" is highlighted.
//
// The provider hook that recognises such a screen reports it here, and the
// agent package's own blind keystrokes stand down while it is held:
//
//   - the initial nudge's deadline path normally types the kickoff
//     "best-effort" into whatever is on screen. On the API-key prompt that CR
//     selects "No" and Claude Code records the key as REJECTED for good —
//     measured on 2.1.283 in a sandbox HOME (mg-2037) — so a pogo spawn would
//     answer the operator's billing question for them, lose the kickoff, and
//     leave the agent at "Not logged in";
//   - the start-verify renudge's bare CR would do the same;
//   - and neither the sentinel-drift detector nor its page should hear about a
//     composer that was held back by a known screen: the sentinel is fine.
//
// A gate is sticky for the life of the process. A human who answers it through
// `pogo agent attach` gets the composer, and the next nudge (or a respawn)
// delivers normally; the gate only suppresses the blind deliveries made while
// the composer was never seen.

// HoldAtPreComposerGate records that the harness is parked on the named gate.
// The first gate named wins; later calls are ignored.
func (a *Agent) HoldAtPreComposerGate(gate string) {
	if gate == "" {
		return
	}
	a.preComposerGate.CompareAndSwap(nil, gate)
}

// PreComposerGate returns the gate this agent was held at, or "".
func (a *Agent) PreComposerGate() string {
	if g, ok := a.preComposerGate.Load().(string); ok {
		return g
	}
	return ""
}

// heldAtGate reports whether a blind delivery must stand down: a gate is held
// and the composer has never been seen. It logs why, once per call site, so a
// withheld kickoff is never silent.
func (a *Agent) heldAtGate(what string) bool {
	gate := a.PreComposerGate()
	if gate == "" || a.promptReadySeen.Load() {
		return false
	}
	log.Printf("agent %s: harness is parked on the %q screen, which pogo does not answer — %s withheld; "+
		"answer it via `pogo agent attach %s` (or fix the cause and respawn)", a.Name, gate, what, a.Name)
	return true
}
