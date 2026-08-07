package scheduler

import (
	"fmt"
	"strings"

	"github.com/drellem2/pogo/internal/mailbox"
)

// The mail-check mailbox guard (mg-aa96, corrected by mg-4f8c).
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
// and exits 0, while a real-but-empty box prints
//
//	No unread messages for aa96
//
// and also exits 0. The two sentences differ, but nothing downstream can use
// the difference: under `--json` both emit NOTHING AT ALL (verified against the
// live binary, 2026-08-07), so an agent polling the wrong inbox forever reads
// exactly what an agent with no mail reads. On 2026-08-05 all eight running
// polecats were mismatched; one sat on an unread mayor correction retracting a
// false premise in its own brief, and one polled a mailbox belonging to a
// different work item entirely.
//
// Fixing the template alone would not have held: the next hand-registered
// schedule, or the next template edit, reintroduces it with no signal. So the
// disagreement is made impossible to HAVE rather than merely discouraged —
// registration refuses it, at the one chokepoint (Entry.Validate, reached from
// Scheduler.Add) that both the `pogo schedule` CLI and pogod's spawn-time
// auto-registration pass through.
//
// # Why the guard is a MEMBERSHIP test and not an equality test (mg-4f8c)
//
// mg mailboxes have NO REGISTRATION. A box is created on first delivery, so an
// agent's inbox is whichever name its SENDERS happened to use — it is not a
// property of the agent at all. mg-aa96 read that as "the mailbox must be the
// agent name" and wrote an equality test. That over-corrects: an agent whose
// correspondents addressed the WORK-ITEM id has real, unread mail sitting in
// the work-item box, and an equality guard refuses the only schedule that would
// ever open it. Agent ba465 hit exactly that.
//
// The rule that actually holds is READ BOTH. So the guard requires the agent's
// OWN mailbox to be among those the message names, and permits any others
// alongside it:
//
//	agent p4f8c, message names p4f8c and mg-4f8c  -> accepted (the both-boxes form)
//	agent p4f8c, message names p4f8c only         -> accepted (mail can only be at p4f8c)
//	agent p4f8c, message names mg-4f8c only       -> REFUSED  (mail sent to p4f8c is invisible)
//	agent p4f8c, message names no mailbox         -> accepted (nothing to disagree with)
//
// The refused row is precisely mg-aa96's defect and it stays refused. The first
// row is what mg-aa96 wrongly refused, and it is the shape every polecat
// template now prescribes.

// MailCheckMailboxes extracts EVERY mailbox a schedule message tells its agent
// to read, in the order the message names them, by finding each
// `mg mail list <mailbox>` invocation in the body. An empty result means the
// message prescribes no such invocation — a message that says "check your mail"
// without naming a mailbox cannot disagree with anything, so there is nothing
// to guard.
//
// It returns a SET rather than a single name because the correct instruction is
// to read more than one box: an agent's mail can be at its agent name or at its
// work-item id depending only on what its senders typed, and there is no
// registration that would settle which (mg-4f8c). A parser that returned just
// the first would make the guard, and the stranded-mail sweep, blind to the
// second — the same one-answer-per-question assumption that caused the bug.
//
// Parsing is token-based rather than a strict regexp because these messages are
// prose: the invocation shows up bare, in backticks, quoted, or trailed by a
// comma. Flags between `list` and the mailbox (`--json`) are skipped.
//
// The parse itself lives in internal/mailbox, a leaf package, because
// internal/agent — which writes these messages — cannot import internal/scheduler
// (this package already imports it). These three wrappers exist so schedule code
// keeps reading in scheduler terms while there stays exactly ONE implementation
// of "same mailbox?" in the tree.
func MailCheckMailboxes(message string) []string {
	return mailbox.ListInvocations(message)
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
	return mailbox.Canonical(s)
}

// ReadsMailbox reports whether a mail-check message sends its agent to the
// named mailbox, comparing canonically. It is the one predicate for "does this
// schedule open that box?", shared by the registration guard and the
// stranded-mail sweep so the two cannot answer it differently.
func ReadsMailbox(message, name string) bool {
	return mailbox.Reads(message, name)
}

// validateMailCheckMailbox refuses a mail-check schedule that never opens its
// own agent's mailbox.
//
// It is a membership test, not an equality test: naming EXTRA boxes is allowed
// and is what the templates prescribe, because a box is created on first
// delivery and an agent's mail may legitimately be sitting under its work-item
// id (mg-4f8c). What stays refused is the case mg-aa96 caught — a schedule that
// calls itself a mail-check for agent A while sending A somewhere that is not
// A, so mail addressed to A is never seen.
//
// The escape hatch is deliberate and deliberately loud: a schedule that really
// is meant to watch ONLY another agent's inbox is not a mail-check, and saying
// so in its id (`--id watch-<x>`) makes it KindOther, which this guard ignores.
func validateMailCheckMailbox(agent, message string) error {
	boxes := MailCheckMailboxes(message)
	if len(boxes) == 0 {
		return nil
	}
	if ReadsMailbox(message, agent) {
		return nil
	}
	return fmt.Errorf(
		"scheduler: mail-check for agent %q reads %s but never %q — "+
			"mail addressed to %q would never be seen, and `mg mail list` on a mailbox "+
			"that was never used prints \"no mail has ever been delivered\" and exits 0 "+
			"(and emits nothing at all under --json), which is indistinguishable from "+
			"having no mail (mg-aa96). "+
			"Add `mg mail list %s` — the identity replies are sent to with "+
			"--from=$POGO_AGENT_NAME. Reading the other box AS WELL is fine and is what "+
			"the polecat templates prescribe: mailboxes have no registration, so mail "+
			"already delivered under another name stays in that box and must still be "+
			"drained (mg-4f8c). If you genuinely mean to poll only another agent's "+
			"mailbox, register it under a non-mail-check id (e.g. --id watch-%s)",
		agent, quotedList(boxes), agent, agent, agent, boxes[0],
	)
}

// quotedList renders the parsed mailboxes for the refusal message. The guard
// now accepts multiple, so naming only the first would misreport which
// instruction was actually rejected.
func quotedList(boxes []string) string {
	quoted := make([]string, len(boxes))
	for i, b := range boxes {
		quoted[i] = fmt.Sprintf("%q", b)
	}
	return strings.Join(quoted, ", ")
}
