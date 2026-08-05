package scheduler

import (
	"fmt"
	"strings"
)

// The mail-check mailbox guard (mg-aa96).
//
// A mail-check schedule carries a message telling its agent how to read its
// mail — in practice `mg mail list <mailbox>`. Nothing used to require that
// <mailbox> be the agent the schedule is registered FOR, and for a while the
// polecat template derived it from the WORK-ITEM ID while the protocol told
// polecats to send with `--from=$POGO_AGENT_NAME`. Those two agree only when
// the agent name happens to be the work-item id minus its `mg-` prefix.
//
// The mismatch was undetectable from inside the agent. `mg mail list` on a
// mailbox nothing was ever delivered to prints
//
//	No mailbox for aa96 yet — no mail has ever been delivered to it
//
// and exits 0 — byte-for-byte the healthy empty case. An agent polling the
// wrong inbox forever reads exactly what an agent with no mail reads. On
// 2026-08-05 all eight running polecats were mismatched; one of them sat on an
// unread mayor correction retracting a false premise in its own brief, and one
// polled a mailbox belonging to a different work item entirely.
//
// Fixing the template alone would not have held: the next hand-registered
// schedule, or the next template edit, reintroduces it with no signal. So the
// disagreement is made impossible to HAVE rather than merely discouraged —
// registration refuses it, at the one chokepoint (Entry.Validate, reached from
// Scheduler.Add) that both the `pogo schedule` CLI and pogod's spawn-time
// auto-registration pass through.

// MailCheckMailbox extracts the mailbox a schedule message tells its agent to
// read, by finding an `mg mail list <mailbox>` invocation in the message body.
// It reports false when the message prescribes no such invocation — a message
// that says "check your mail" without naming a mailbox cannot disagree with
// anything, so there is nothing to guard.
//
// Parsing is token-based rather than a strict regexp because these messages are
// prose: the invocation shows up bare, in backticks, quoted, or trailed by a
// comma. Flags between `list` and the mailbox (`--json`) are skipped.
func MailCheckMailbox(message string) (string, bool) {
	fields := strings.Fields(strings.Map(func(r rune) rune {
		switch r {
		case '`', '"', '\'':
			return ' '
		}
		return r
	}, message))

	for i := 0; i+2 < len(fields); i++ {
		if fields[i] != "mg" || fields[i+1] != "mail" || fields[i+2] != "list" {
			continue
		}
		for _, tok := range fields[i+3:] {
			tok = strings.Trim(tok, ".,;:!?()[]")
			if tok == "" {
				continue
			}
			if strings.HasPrefix(tok, "-") {
				continue // a flag, e.g. --json; the mailbox is still ahead
			}
			return tok, true
		}
	}
	return "", false
}

// CanonicalMailbox reduces a mailbox name to the identity mg itself resolves it
// to, so the guard compares what mg compares. mg strips a leading `mg-`:
// `mg mail list mg-aa96` and `mg mail list aa96` both read the mailbox `aa96`
// (verified against the live binary, 2026-08-05). Comparison is
// case-insensitive because a name that differs only in case is a typo, not a
// second mailbox.
//
// Exported so the stranded-mail report (internal/strandedmail) decides "same
// mailbox?" with the SAME function the registration guard uses. Two answers to
// that question that could drift is how this whole class of bug starts.
func CanonicalMailbox(s string) string {
	return strings.TrimPrefix(strings.ToLower(strings.TrimSpace(s)), "mg-")
}

// validateMailCheckMailbox refuses a mail-check schedule whose message sends its
// agent to somebody else's mailbox.
//
// The escape hatch is deliberate and deliberately loud: a schedule that really
// is meant to watch another agent's inbox is not a mail-check, and saying so in
// its id (`--id watch-<x>`) makes it KindOther, which this guard ignores. What
// cannot happen any more is the silent case — a schedule that calls itself a
// mail-check for agent A while pointing A at mailbox B.
func validateMailCheckMailbox(agent, message string) error {
	mailbox, ok := MailCheckMailbox(message)
	if !ok {
		return nil
	}
	if CanonicalMailbox(mailbox) == CanonicalMailbox(agent) {
		return nil
	}
	return fmt.Errorf(
		"scheduler: mail-check for agent %q tells it to read mailbox %q — "+
			"mail addressed to %q would never be seen, and `mg mail list %s` on a mailbox "+
			"that was never used prints \"no mail has ever been delivered\" and exits 0, "+
			"which is indistinguishable from having no mail (mg-aa96). "+
			"Use `mg mail list %s` — the same identity replies are sent to with "+
			"--from=$POGO_AGENT_NAME. If you genuinely mean to poll another agent's "+
			"mailbox, register it under a non-mail-check id (e.g. --id watch-%s)",
		agent, mailbox, agent, mailbox, agent, mailbox,
	)
}
