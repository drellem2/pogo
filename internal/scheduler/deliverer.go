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
				// Pass the completion token as the nudge's correlation id so
				// nudge_sent joins to scheduler_fire_completed (mg-a754).
				if err := a.NudgeWithModeCorrelated(body, agent.NudgeWaitIdle, agent.DefaultNudgeTimeout, entry.PendingToken); err == nil {
					return nil
				} else {
					// Log and fall through to mail — better to deliver late
					// via mail than drop the fire entirely.
					return p.sendMail(entry.Agent, subject, body+"\n\n[scheduler] nudge failed: "+err.Error())
				}
			}
		}
		// Agent not running — fall back to mail so the schedule is durable
		// even when the recipient is offline.
		return p.sendMail(entry.Agent, subject, body)
	default:
		return fmt.Errorf("scheduler: unsupported delivery %q", entry.Delivery)
	}
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
