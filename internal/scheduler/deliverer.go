package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// AgentLookup is the subset of *agent.Registry the scheduler needs. Defining
// the interface here (rather than depending on the full Registry surface)
// keeps the test seam small.
type AgentLookup interface {
	Get(name string) *agent.Agent
}

// MailSender sends a macguffin mail message. Mirrors client.SendMGMail but is
// declared as a function value so tests can pass a recorder instead of
// shelling out to `mg`.
type MailSender func(to, from, subject, body string) error

// PogodDeliverer is the production Deliverer used by pogod. On fire it tries
// the configured delivery mode first, then falls back as follows:
//
//   - DeliveryNudge: deliver via the agent's PTY if the agent is currently
//     running. Falls back to mail if the agent is not registered with the
//     daemon — matches client.NudgeOrMail's existing semantics so a sleeping
//     polecat picks the message up next time it lists mail.
//   - DeliveryMail:  always send via macguffin mail.
type PogodDeliverer struct {
	Registry AgentLookup
	Mail     MailSender
}

// Deliver implements Deliverer.
func (p *PogodDeliverer) Deliver(ctx context.Context, entry Entry, fireTime time.Time) error {
	body := buildBody(entry, fireTime)
	subject := buildSubject(entry, fireTime)

	switch entry.Delivery {
	case DeliveryMail:
		return p.sendMail(entry.Agent, subject, body)
	case "", DeliveryNudge:
		// Try PTY first.
		if p.Registry != nil {
			a := p.Registry.Get(entry.Agent)
			if a != nil && a.Status == agent.StatusRunning {
				// NudgeWake, not NudgeWithModeCorrelated: a fire landing on a
				// live PTY is a WAKE, so it passes through the wake-cycle
				// policy (internal/agent/wakepolicy.go) — one wake per unbroken
				// silence, none inside a known limit episode. Pass the
				// completion token as the correlation id so nudge_sent (or
				// nudge_suppressed) joins to scheduler_fire_completed (mg-a754).
				//
				// The mode is NudgeConfirm, not NudgeWaitIdle: a scheduled fire
				// lands on a WORKING agent by design — pm-pogo's 09:00 sweep
				// died on "still producing output after 30s" and only ran
				// because the mail fallback caught it — and wait-idle's
				// precondition is the negation of that state (mg-ebee).
				err := a.NudgeWake(body, agent.NudgeConfirm, agent.DefaultNudgeTimeout, entry.PendingToken)
				if !mailAfterNudge(err) {
					return nil
				}
				// Log and fall through to mail — better to deliver late via
				// mail than drop the fire entirely. A policy decline says so in
				// its own words rather than borrowing "nudge failed": nothing
				// failed, the wake was declined, and a recipient reading its own
				// fire deserves the difference.
				note := "[scheduler] nudge failed: " + err.Error()
				if errors.Is(err, agent.ErrWakeSuppressed) {
					note = "[scheduler] terminal wake suppressed: " + err.Error()
				}
				return p.sendMail(entry.Agent, subject, body+"\n\n"+note)
			}
		}
		// Agent not running — fall back to mail so the schedule is durable
		// even when the recipient is offline.
		return p.sendMail(entry.Agent, subject, body)
	default:
		return fmt.Errorf("scheduler: unsupported delivery %q", entry.Delivery)
	}
}

// mailAfterNudge decides whether a fire whose PTY nudge returned err still
// needs the mail fallback.
//
// Everything except one case does. The exception is ErrNudgeQueued: the message
// was typed into a live harness that was mid-turn, and pogod did not see it
// submitted before the deadline. Its bytes are in a real input queue and will
// almost certainly be processed when the turn ends — so mailing it too would
// put the same instruction in front of the agent twice. An agent acting twice
// on one instruction is a worse outcome than one that acts late, which is the
// same judgement that puts the bare return ahead of the resend in the nudge
// escalation itself.
//
// This is not a silent success: the nudge path emits nudge_unconfirmed for
// exactly this outcome, so a fire that ended here is visible in the event log
// next to the ones that were confirmed, rather than indistinguishable from
// them.
//
// ErrWakeSuppressed is NOT the exception and must not become one: a suppressed
// wake wrote nothing at all, so mail is the only delivery rather than a second
// one — which is the property mg-8184 built the suppression against.
func mailAfterNudge(err error) bool {
	if err == nil {
		return false
	}
	return !errors.Is(err, agent.ErrNudgeQueued)
}

func (p *PogodDeliverer) sendMail(to, subject, body string) error {
	if p.Mail == nil {
		return errors.New("scheduler: mail sender not configured")
	}
	return p.Mail(to, "scheduler", subject, body)
}

// buildBody assembles the message text delivered on fire. It always includes
// the schedule id and the original fire time so the receiving agent can
// distinguish a fresh fire from a replay during sleep recovery, and — when the
// fire carries a completion token — the one-line command that acknowledges it.
func buildBody(entry Entry, fireTime time.Time) string {
	original := entry.NextFire.Format(time.RFC3339)
	now := fireTime.Format(time.RFC3339)
	var head string
	switch {
	case entry.Message != "":
		head = fmt.Sprintf("%s\n\n[scheduler id=%s due=%s fired=%s%s]",
			entry.Message, entry.ID, original, now, ackField(entry))
	case entry.OneShot:
		head = fmt.Sprintf("Scheduled wakeup id=%s — fired at %s (was due %s).", entry.ID, now, original)
	default:
		head = fmt.Sprintf("Scheduled fire id=%s cron=%q — fired at %s (was due %s).", entry.ID, entry.Cron, now, original)
	}
	return head + ackInstruction(entry)
}

// ackField renders the ` ack=<token>` addition to the metadata footer. Kept
// separate so the footer stays byte-identical to its pre-mg-a754 form when no
// token was issued (a hand-constructed Entry, or a test).
func ackField(entry Entry) string {
	if entry.PendingToken == "" {
		return ""
	}
	return " ack=" + entry.PendingToken
}

// ackInstruction is the line that turns a delivered fire into a completable
// one. It rides the message body rather than living in a prompt file on
// purpose: the instruction then reaches EVERY recipient of EVERY schedule
// without depending on which prompt template the agent was spawned with, or on
// whether its harness exposes a readable transcript.
//
// Running this command is the completion signal. It requires a live model turn
// that executed a tool, which is precisely what the ~5500 synthetic zero-token
// failure turns in this fleet's history could not do — see completion.go.
func ackInstruction(entry Entry) string {
	if entry.PendingToken == "" {
		return ""
	}
	return fmt.Sprintf(
		"\nWhen this fire's work is done, run: pogo schedule ack %s --agent %s --token %s",
		entry.ID, entry.Agent, entry.PendingToken)
}

func buildSubject(entry Entry, fireTime time.Time) string {
	if entry.OneShot {
		return "scheduler: " + entry.ID
	}
	return "scheduler: " + entry.ID + " (cron " + entry.Cron + ")"
}
