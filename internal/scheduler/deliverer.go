package scheduler

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/events"
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
//
// The mail fallback is COALESCED — see the fallback-run block below. That is
// the whole of mg-af83's change: which fires are delivered, and by which
// channel, is untouched.
type PogodDeliverer struct {
	Registry AgentLookup
	Mail     MailSender

	// LogPath is the scheduler's own-root events.log, the same path
	// Scheduler.emitSchedulerEvent writes to (see EventLogPath and mg-e06d).
	// Empty means "resolve globally", which is what a bare test deliverer
	// wants; pogod sets it explicitly.
	LogPath string

	runMu sync.Mutex
	runs  map[fallbackKey]*fallbackRun
}

// # The coalesced mail fallback (mg-af83)
//
// A fire that reaches the agent's PTY has never written a mailbox copy — the
// success path returns before sendMail and always has, since mg-bcfa. What
// filled the fleet's mailboxes is the OTHER path repeating: a schedule whose
// fires cannot be delivered as a nudge writes one copy every cron tick, for as
// long as that lasts. Measured 2026-08-09 across ~/.macguffin/mail: 12,295
// messages from `scheduler`, of which architect's 264 unread were a single
// unbroken run spanning the 2026-08-07..09 outage. In architect's box that was
// 264 rows against 1 real message, and the copies are self-defeating — the body
// says "check your mail", into the listing it is burying.
//
// The fallback exists so a schedule is durable across an agent being
// unreachable. ONE copy discharges that: the message is a recurring reminder,
// so copies 2..N of a run repeat an instruction the recipient has not yet acted
// on rather than adding anything to it. The 2026-08-08 outage is the natural
// experiment — 198 copies for pm-pogo, and on return the agent learned from
// them exactly what its own startup mail-check already told it, at the cost of
// burying a real 32-hour-old message at row ~108 of 265.
//
// So: while a schedule's fires cannot be delivered as a nudge, at most one
// mailbox copy is written per unbroken run of undelivered fires. A fire that
// does reach the PTY closes the run, and the next undelivered one opens a new
// one. The run is refreshed every fallbackRefreshInterval so the single copy in
// the box cannot go arbitrarily stale, and so the map cannot accumulate an
// entry per polecat schedule for the life of the process.
//
// The run state is in memory, so a pogod restart during an outage permits one
// extra copy. That is the right direction to fail in: the cost is a message,
// the alternative is persisting a claim about someone else's mailbox across a
// restart that may have been a `mg archive` sweep.
//
// A coalesced fire still returns nil, so the scheduler records it as delivered
// and fires_delivered / unacked_streak read exactly as they did before. That is
// deliberate: the fire's information IS in the recipient's mailbox, unread,
// from the copy that opened the run — and re-scoring delivery is the change
// mg-af83 explicitly defers until the ack is coupled to the work.
//
// What this deliberately does NOT do: gate the write on unacked_streak. That
// counter cannot presently distinguish an agent that did not do the work from
// one that did it and did not ack (measured fleet ack rate ~18-22%), nor a dead
// agent from a live one batching its fires — mg-af83 records both confounds and
// the sequencing that has to come first. Run length is a property of THIS
// deliverer's own delivery attempts, needs no detector, and cannot be confused
// by either.
type fallbackKey struct{ agent, id string }

// fallbackRun is an open run: a mailbox copy for this schedule is sitting in
// the recipient's box, and no fire has reached their PTY since it was written.
type fallbackRun struct {
	writtenAt  time.Time
	reason     string
	suppressed int
}

// fallbackRefreshInterval is how long a single mailbox copy stands for its run
// before the next undelivered fire is allowed to write a fresh one.
//
// It is a bound on staleness, not on volume: an agent unreachable for the 44
// hours of the 2026-08-07 outage gets two copies rather than 264, and the
// newest is never more than a day old. It also bounds the run map, whose keys
// would otherwise include every polecat schedule pogod has ever delivered to.
const fallbackRefreshInterval = 24 * time.Hour

// EventFallbackCoalesced records a mailbox copy that was NOT written because an
// earlier copy from the same run is already unread in the recipient's box.
//
// Emitted for the same reason nudge_suppressed is: a suppression whose reason
// is not observable is indistinguishable from the delivery bug it exists to
// prevent. Between this and the mail that opened the run, every undelivered
// fire leaves exactly one record.
const EventFallbackCoalesced = "scheduler_fallback_coalesced"

// Reasons a fire fell back to mail. They name the branch in the coalescing
// event so a reader can tell "nobody was home" from "the terminal refused it".
const (
	fallbackReasonNotRunning = "agent_not_running"
	fallbackReasonNudgeFail  = "nudge_failed"
	fallbackReasonSuppressed = "wake_suppressed"
)

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
					// The fire reached the agent. Any mailbox copy standing in
					// for an earlier undelivered run has been superseded by a
					// live delivery, so the next failure starts a fresh run.
					p.closeFallbackRun(entry)
					return nil
				}
				// Log and fall through to mail — better to deliver late via
				// mail than drop the fire entirely. A policy decline says so in
				// its own words rather than borrowing "nudge failed": nothing
				// failed, the wake was declined, and a recipient reading its own
				// fire deserves the difference.
				note := "[scheduler] nudge failed: " + err.Error()
				reason := fallbackReasonNudgeFail
				if errors.Is(err, agent.ErrWakeSuppressed) {
					note = "[scheduler] terminal wake suppressed: " + err.Error()
					reason = fallbackReasonSuppressed
				}
				return p.fallbackMail(entry, subject, body+"\n\n"+note, reason, fireTime)
			}
		}
		// Agent not running — fall back to mail so the schedule is durable
		// even when the recipient is offline.
		return p.fallbackMail(entry, subject, body, fallbackReasonNotRunning, fireTime)
	default:
		return fmt.Errorf("scheduler: unsupported delivery %q", entry.Delivery)
	}
}

// mailAfterNudge decides whether a fire whose PTY nudge returned err still
// needs the mail fallback.
//
// Everything except one case does. The exception is ErrNudgeQueued: the message
// was typed into a harness that was mid-turn, which Claude Code takes and acts
// on but emits no UserPromptSubmit receipt for. It was measured answering such
// a prompt while pogod's receipt sat unmoved (see ErrNudgeQueued), so the
// missing receipt is a blind spot, not a lost message — and mailing a second
// copy would put the same instruction in front of the agent twice. An agent
// acting twice on one instruction is a worse outcome than one that acts late,
// which is the same judgement that puts the bare return ahead of the resend in
// the nudge escalation itself.
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

// fallbackMail is the single door to the mail fallback. It writes a mailbox
// copy only when this schedule has no copy already standing unread for the
// current run of undelivered fires.
//
// The run opens only after the send SUCCEEDS. A failed send left nothing in the
// box, so treating it as an open run would suppress every later copy against a
// message that does not exist — trading a mailbox full of duplicates for a
// schedule that is silently undeliverable, which is the strictly worse fault.
func (p *PogodDeliverer) fallbackMail(entry Entry, subject, body, reason string, fireTime time.Time) error {
	if run, open := p.rideOpenRun(entry, fireTime); open {
		p.emitFallbackCoalesced(entry, reason, run, fireTime)
		return nil
	}
	if err := p.sendMail(entry.Agent, subject, body+coalesceNotice(entry)); err != nil {
		return err
	}
	p.openFallbackRun(entry, reason, fireTime)
	return nil
}

// rideOpenRun reports whether a mailbox copy for this schedule is already
// unread from the current run, counting this fire against it if so. Runs older
// than fallbackRefreshInterval are dropped first, which both refreshes a stale
// copy and keeps the map to the schedules actually delivered to recently.
func (p *PogodDeliverer) rideOpenRun(entry Entry, now time.Time) (fallbackRun, bool) {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	for k, r := range p.runs {
		if now.Sub(r.writtenAt) >= fallbackRefreshInterval {
			delete(p.runs, k)
		}
	}
	r, ok := p.runs[fallbackKey{entry.Agent, entry.ID}]
	if !ok {
		return fallbackRun{}, false
	}
	r.suppressed++
	return *r, true
}

// openFallbackRun records that a mailbox copy was just written for this
// schedule, so the fires behind it can ride on it.
func (p *PogodDeliverer) openFallbackRun(entry Entry, reason string, now time.Time) {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	if p.runs == nil {
		p.runs = make(map[fallbackKey]*fallbackRun)
	}
	p.runs[fallbackKey{entry.Agent, entry.ID}] = &fallbackRun{writtenAt: now, reason: reason}
}

// closeFallbackRun ends the run for this schedule: a fire has reached the agent
// directly, so the next one that cannot will write its own copy.
func (p *PogodDeliverer) closeFallbackRun(entry Entry) {
	p.runMu.Lock()
	defer p.runMu.Unlock()
	delete(p.runs, fallbackKey{entry.Agent, entry.ID})
}

// coalesceNotice tells the recipient that the copy they are reading stands for
// every undelivered fire behind it. Without it the single copy would read as
// though the schedule fired once, which is a different and wrong claim.
func coalesceNotice(entry Entry) string {
	return fmt.Sprintf(
		"\n\n[scheduler] This is the only mailbox copy of %s until a fire reaches your terminal: "+
			"further fires that cannot be delivered are coalesced into this one, and a fresh copy "+
			"is written at most once every %s (mg-af83).",
		entry.ID, fallbackRefreshInterval)
}

// emitFallbackCoalesced records the copy that was not written, with the length
// of the run so far and the age of the copy that stands for it.
func (p *PogodDeliverer) emitFallbackCoalesced(entry Entry, reason string, run fallbackRun, fireTime time.Time) {
	ev := events.Event{
		EventType: EventFallbackCoalesced,
		Agent:     "pogod",
		Details: map[string]any{
			"schedule_id":       entry.ID,
			"to":                entry.Agent,
			"delivery":          string(entry.Delivery),
			"reason":            reason,
			"run_reason":        run.reason,
			"copies_suppressed": run.suppressed,
			"mailbox_copy_at":   run.writtenAt.Format(time.RFC3339),
			"fired_at":          fireTime.Format(time.RFC3339),
		},
	}
	if p.LogPath == "" {
		events.Emit(context.Background(), ev)
		return
	}
	events.EmitTo(context.Background(), p.LogPath, ev)
}

func (p *PogodDeliverer) sendMail(to, subject, body string) error {
	if p.Mail == nil {
		return errors.New("scheduler: mail sender not configured")
	}
	return p.Mail(to, "scheduler", subject, body)
}

// buildBody assembles the message text delivered on fire. It always includes
// the schedule id and the original fire time so the receiving agent can
// distinguish a fresh fire from a replay during sleep recovery, the lateness
// line that says how to USE that due time (see latenessInstruction), and — when
// the fire carries a completion token — the one-line command that acknowledges
// it.
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
	return head + latenessInstruction(entry) + ackInstruction(entry)
}

// latenessInstruction tells the recipient how to find out how late it is, and
// it rides every fire because the mechanism cannot know which recipient is
// window-bound.
//
// # The due time was never the missing half — the COMPARISON was
//
// mg-d4a7 was filed on the premise that "a delivered fire carries no DUE TIME".
// It does, and always has: `due=` has been in this footer since the scheduler's
// first commit (mg-bcfa), and it holds the ORIGINAL due time because Tick fires
// off entry.NextFire and only advances it afterwards. Read the footer of any
// fire in ~/.pogo/events.log to see it.
//
// What is genuinely absent is the instruction to compare that due time against
// the CURRENT clock. `fired=` sits right beside it and reads exactly like "when
// you got this" — and it is not. It is when the bytes were SENT. A nudge typed
// into a busy PTY, or a mailbox copy written for an absent agent, is consumed
// by a turn that can run arbitrarily later.
//
// # What that cost, measured
//
// deploy-verify-architect's 2026-08-19 fire: original_due 03:33:00, fired_at
// 03:33:10 — ten seconds, punctual by every measure this repo has, including
// ackwatch's FireEvent.Late(), which is fired−due. It was delivered
// nudge_unconfirmed into a stale architect and redeemed at 07:52:35, latency
// 15,565,050 ms — 4h19m. Several of that procedure's reads are answerable only
// inside the 03:00–03:35 deploy window; run at 06:52 they cannot tell the
// deploy's write from a leftover. The run produced, in architect's own words,
// "a report that was mostly correct with a small unmarked wrong region, which
// is worse than wholly wrong". A wholly wrong report gets discarded; a
// mostly-right one gets believed.
//
// So the footer was not silent. It was misread, in the one way its own shape
// invites — and an agent that had compared due against `date` would have caught
// it. This line names that comparison and names the wrong reference explicitly.
//
// # Why it does not refuse, and why it is unconditional
//
// Late is GRADED, not binary. Most of a late procedure still stands: sections
// reading artifacts that carry their own timestamps are as good at 07:00 as at
// 03:33; only the live-state reads — a stamp mtime, `dig`, `pgrep`, a daemon's
// uptime — go stale. The honest late report is "most of this stands, these
// three lines are REFUSED", which is neither a clean report nor a discarded
// one. That judgement belongs to the procedure, which knows which of its own
// reads are time-sensitive; the mechanism's whole job is to hand it the fact.
//
// Unconditional for the same reason: no per-schedule policy can know that, and
// a schedule whose work becomes window-bound later would not get re-registered
// to say so. A mail-check pays one line of noise; the alternative is the class
// staying open for every window-bound schedule that has not been patched by
// hand.
func latenessInstruction(entry Entry) string {
	return fmt.Sprintf(
		"\nHow late am I: compare due=%s against the CURRENT clock — NOT against fired=, "+
			"which is when these bytes were sent, not when you are reading them (measured gap "+
			"between sent and read: 4h19m). Lateness is graded: if any of this work's reads "+
			"depend on WHEN they run, mark those stale and answer the rest normally.",
		entry.NextFire.Format(time.RFC3339))
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
