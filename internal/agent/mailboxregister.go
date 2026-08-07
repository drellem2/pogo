package agent

import (
	"context"
	"log"

	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/mailbox"
)

// Spawn-time mailbox registration (mg-7dc1).
//
// THE DEFECT. Until mg-d639, `mg mail send` filed mail for any name at all: a
// mailbox came into being on first delivery. Nothing ever had to provision a
// polecat's inbox, so nothing did — and because the first sender created it,
// the omission was invisible. mg-d639 made an unknown recipient a refusal
// (no_such_mailbox, exit 3). That is the right fix, and it exposed what had been
// leaning on the old behaviour: a freshly spawned polecat is not addressable by
// anybody.
//
// This was not a corner case. On 2026-08-07 pm-pogo checked every recent
// directory under ~/.pogo/polecats against the registered-mailbox list (1,261
// boxes, so the instrument was live): 10 of the 12 most recent polecats had NO
// mailbox under any name. The two that did were the two someone had already
// tripped over and repaired with --create that same evening — so the population
// was effectively 12 of 12. Both name forms were absent, the agent name AND the
// work-item id, which rules out a naming mismatch: the box did not exist until a
// sender happened to create one.
//
// Mail is the review-loop transport on the gh-issue track (mg-4f8c), so this is
// a dispatch-visible failure rather than a test-harness annoyance. Three
// instances surfaced within 20 minutes of mg-d639 landing only because three
// people happened to mail three polecats; the other nine were never written to
// and so never complained.
//
// WHY REGISTRATION AND NOT --create AT THE CALLSITES. `mg mail send --create`
// would make each of those sends succeed, and it is the wrong fix twice over.
// It puts --create on the very callsites whose typos mg-d639 exists to catch,
// restoring phantom mailboxes under a new name; and it still leaves every
// polecat unreachable to any caller a sweep missed. Registering at spawn keeps
// --create what mg-d639 intended — a rare, deliberate act for a genuinely new
// correspondent — so that a refusal means "you typed the name wrong" instead of
// "this recipient was never provisioned". Ruling: pm-pogo, 2026-08-07.
//
// WHICH BOXES. Both of them, and NOT by writing the list down twice. A polecat's
// mail can be in its agent name or its work-item id, because which one holds it
// is a property of the sender rather than of the polecat (mg-4f8c) — so the
// mail-check nudge instructs it to read both. Registering a different set than
// the nudge reads reopens this ticket as "registered the box nobody reads", so
// the set is DERIVED from that nudge (see polecatMailboxes): the boxes
// provisioned are literally the boxes the polecat is told to open, and a later
// edit to either side moves both.

// MailboxRegistrar provisions an agent's mg mailbox so mail can be addressed to
// it before anyone has mailed it.
//
// pogod backs this with `mg mail register` (client.RegisterMGMailbox). A nil
// registrar makes spawn skip provisioning — a bare registry in tests, or a
// daemon on a host with no macguffin — which is the pre-mg-7dc1 behaviour and
// leaves the polecat running but unaddressable until some sender passes
// --create.
//
// Teardown is deliberately NOT part of this interface, and the asymmetry is
// intentional rather than an omission. A mailbox outlives the process it was
// provisioned for: mail sent to a polecat that has since exited is not garbage,
// it is the record of what its correspondents asked for, and check-strandedmail
// exists to find exactly that. Deleting the box at reap would destroy it. An
// empty Maildir costs three directories.
type MailboxRegistrar interface {
	// RegisterMailbox creates the named mailbox if it does not exist. It MUST be
	// idempotent — spawn calls it unconditionally — and a name carrying an `mg-`
	// prefix must land in the same box as the bare form, because callers on
	// either side of this interface use both spellings.
	RegisterMailbox(name string) error
}

// SetMailboxRegistrar installs the adapter used by spawn-polecat to provision a
// polecat's mailboxes. Call once at startup before any polecat is spawned. A nil
// registrar disables provisioning.
func (r *Registry) SetMailboxRegistrar(m MailboxRegistrar) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mailboxRegistrar = m
}

func (r *Registry) getMailboxRegistrar() MailboxRegistrar {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mailboxRegistrar
}

// polecatMailboxes returns every mailbox that must exist for a polecat to be
// reachable — by READING THEM OUT of the mail-check nudge the same spawn
// registers, rather than by recomputing them.
//
// The recomputation is the bug this avoids. pm-pogo's constraint on the fix was
// that spawn-registration must create whatever set the polecat's instructions
// tell it to read, and that the two must agree; a second derivation that drifts
// by one `mg-` prefix or one dropped work-item box satisfies neither, and fails
// silently in the direction that is hardest to see — a provisioned box nobody
// opens, or an opened box nobody could provision. Reading the nudge makes
// agreement structural: PolecatMailCheckMessage is the single statement of where
// this polecat's mail lives, and both the instruction and the provisioning are
// consequences of it.
//
// This is the same lesson as internal/mailbox's package comment, applied one
// level up: two components answering "where does this agent's mail live?"
// independently is how mg-aa96 and mg-4f8c happened.
func polecatMailboxes(agentName, workItemID string) []string {
	return mailbox.ListInvocations(PolecatMailCheckMessage(agentName, workItemID))
}

// registerPolecatMailboxes provisions the mailboxes a freshly spawned polecat
// must be reachable at.
//
// NON-FATAL to the spawn, like the mail-check loop beside it: the polecat is
// already running, and killing a live worker over an unprovisioned inbox trades
// a reachability problem for a lost work item. But not silent either — an
// unaddressable polecat is the exact failure this ticket is about, and it
// presents as everything looking healthy from both ends (mg-4f8c's four vanished
// mails, both agents correctly reported fine). Every drop emits
// mailbox_register_failed so the condition is recoverable from events.log
// instead of from somebody happening to mail the polecat.
//
// Each box is registered independently rather than stopping at the first error:
// they are separate names and one failing tells us nothing about the other, so
// giving up early would leave a box unprovisioned that would have registered
// fine.
func (r *Registry) registerPolecatMailboxes(agentName, workItemID string) {
	boxes := polecatMailboxes(agentName, workItemID)
	reg := r.getMailboxRegistrar()
	if reg == nil {
		for _, box := range boxes {
			r.reportMailboxRegisterFailed(agentName, box, "nil_registrar")
		}
		return
	}
	for _, box := range boxes {
		if err := reg.RegisterMailbox(box); err != nil {
			log.Printf("polecat %s: mailbox %q registration failed: %v", agentName, box, err)
			r.reportMailboxRegisterFailed(agentName, box, "register_error: "+err.Error())
		}
	}
}

// reportMailboxRegisterFailed records that a polecat mailbox could not be
// provisioned. It emits straight to the event log rather than going through a
// settable reporter: unlike the mail-check registrar there is nothing here that
// pogod wires late, so the indirection that mg-6fe0 needed (a reporter that
// survives a scheduler which never loaded) would buy nothing.
func (r *Registry) reportMailboxRegisterFailed(agentName, box, reason string) {
	events.Emit(context.Background(), events.Event{
		EventType: "mailbox_register_failed",
		Agent:     agentName,
		Details: map[string]any{
			"mailbox": box,
			"reason":  reason,
		},
	})
}
