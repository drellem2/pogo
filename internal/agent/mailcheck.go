package agent

import (
	"fmt"
	"log"
)

// PolecatMailCheckCron is the cadence of the mail-check loop auto-registered
// for every spawned polecat. Every 10 minutes matches the crew-agent
// mail-check convention (see ~/.pogo/schedules.json: mail-check-<name> entries)
// and the cadence the mayor registered by hand before this was automated.
const PolecatMailCheckCron = "*/10 * * * *"

// PolecatMailCheckMessage builds the generic nudge body delivered on each
// mail-check fire. It tells the polecat to drain its inbox and act on the
// builder<->reviewer review-loop traffic (reviewer findings, re-review
// requests) that would otherwise sit unread — the silent-stall this schedule
// exists to prevent (mg-e633).
//
// mailbox MUST be the polecat's AGENT NAME, not its work item id. Mail reaches
// a polecat under the identity its correspondents address, and the protocol has
// them reply to `--from=$POGO_AGENT_NAME`. Deriving this from the work item
// instead sent all eight polecats running on 2026-08-05 to a mailbox nobody
// writes to, invisibly — see internal/scheduler/mailbox.go (mg-aa96).
func PolecatMailCheckMessage(mailbox string) string {
	return fmt.Sprintf(
		"Check your mail with `mg mail list %s` and handle any unread messages — "+
			"act on any reviewer findings or re-review requests and mail your reply back; "+
			"otherwise no-op.",
		mailbox,
	)
}

// MailCheckRegistrar registers a per-polecat mail-check schedule at spawn time
// so the mayor — or a peer polecat in a builder<->reviewer review loop — can
// reach the polecat mid-task without it having to poll mail on its own.
// Polecats are not on pogod's nudge cycle, so before this hook a review loop
// stalled silently: the reviewer's findings mail never woke the builder. See
// mg-e633.
//
// pogod backs this with its scheduler; a nil registrar (a bare registry, or a
// daemon with the scheduler disabled) makes spawn skip registration — the
// polecat still runs, it just does not get proactive mail nudges.
//
// Teardown is deliberately NOT part of this interface. pogod already reaps
// every mail-check-* schedule addressed to an agent when that agent's process
// exits (RemoveMailChecksForAgent, gh drellem2/macguffin #35 / #15). Because
// the schedule is addressed to the polecat's bare registry name — the same
// identity the reap path matches on — the spawn-registered loop is torn down
// automatically with no extra code.
type MailCheckRegistrar interface {
	// RegisterMailCheck adds (or idempotently replaces) the recurring
	// mail-check schedule for a polecat. agentName is the polecat's bare
	// registry name (the identity pogod delivers nudges to and reaps under);
	// workItemID keys the schedule id (mail-check-<workItemID>) so it is
	// specific enough to survive the scheduler's stale-entry sweep (mg-8e5d).
	RegisterMailCheck(agentName, workItemID, cron, message string) error
}

// ScheduleRegisterFailureReporter emits structured schedule_register_failed
// telemetry when a polecat's mail-check loop could not be registered. It is
// deliberately SEPARATE from MailCheckRegistrar for one reason: the registrar
// can itself be nil — pogod installs it only after its scheduler loads, so a
// scheduler that fails to load at startup leaves every polecat spawn on the
// nil-registrar path (mailcheck.go's `reg == nil`). That drop — a live polecat
// with no reachability channel — is exactly the case that most needs a LOUD,
// structured signal, yet the registrar that would carry it is absent. Wiring
// the reporter independently of the registrar is what lets the failure still be
// recorded when the registrar is gone.
//
// Event-ONLY: a nil registrar at startup is benign (bare registry in tests, or
// a daemon with the scheduler disabled), so it must NOT escalate to a mayor
// nudge — that noise is reserved for the persistent post-retry Add-failure
// path, which the registrar adapter owns (mg-6fe0).
type ScheduleRegisterFailureReporter interface {
	// ReportScheduleRegisterFailed records that a polecat's mail-check schedule
	// could not be registered. scheduleKey is the schedule-id key (the work item
	// id, or the agent name when the spawn carried none) — NOT the mailbox,
	// which is always the agent name (mg-aa96); reason is a short,
	// machine-stable cause so a reader can tell the two live suspects apart (a
	// benign startup nil registrar vs. a transient persist-IO failure).
	ReportScheduleRegisterFailed(agentName, scheduleKey, reason string)
}

// SetMailCheckRegistrar installs the scheduler adapter used by spawn-polecat to
// auto-register a polecat's mail-check loop. Call once at startup before any
// polecat is spawned. A nil registrar disables auto-registration.
func (r *Registry) SetMailCheckRegistrar(m MailCheckRegistrar) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mailCheckRegistrar = m
}

func (r *Registry) getMailCheckRegistrar() MailCheckRegistrar {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.mailCheckRegistrar
}

// SetScheduleRegisterFailureReporter installs the reporter used to emit
// schedule_register_failed telemetry. Call once at startup — critically, wire it
// EVEN WHEN the scheduler (and therefore the mail-check registrar) fails to
// load, so the startup nil-registrar drop still produces a structured signal.
// A nil reporter falls back to a plain log line (see reportScheduleRegisterFailed).
func (r *Registry) SetScheduleRegisterFailureReporter(rep ScheduleRegisterFailureReporter) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.scheduleRegisterFailureReporter = rep
}

func (r *Registry) reportScheduleRegisterFailed(agentName, scheduleKey, reason string) {
	r.mu.RLock()
	rep := r.scheduleRegisterFailureReporter
	r.mu.RUnlock()
	if rep == nil {
		// No reporter wired (a unit test, or pogod could not even resolve the
		// scheduler root): fall back to a log line so the drop is never fully
		// silent — the whole point of mg-6fe0 is that this stops being invisible.
		log.Printf("polecat %s: mail-check schedule registration failed (%s); no failure reporter wired", agentName, reason)
		return
	}
	rep.ReportScheduleRegisterFailed(agentName, scheduleKey, reason)
}

// registerPolecatMailCheck registers the mail-check loop for a freshly spawned
// polecat.
//
// Two identities are in play here and they are NOT interchangeable, which is
// the whole of mg-aa96:
//
//   - the schedule KEY (mail-check-<workItemID>) names the unit of work, and
//     falls back to the agent name when the spawn carried no work item id, so
//     every polecat gets a specific, reap-matchable schedule id;
//   - the MAILBOX is always the agent name, because that is the identity mail
//     is addressed to (the protocol replies to --from=$POGO_AGENT_NAME).
//
// Collapsing the second into the first is what pointed all eight polecats
// running on 2026-08-05 at a mailbox nobody writes to, with no error to notice.
// Scheduler.Add now refuses a mail-check whose message names a mailbox other
// than its agent, so this pairing cannot silently drift again.
//
// Registration stays NON-fatal to the spawn (the polecat is already running),
// but it is no longer best-effort-and-silent: a mail-check loop is a polecat's
// PRIMARY reachability channel — the modify<->review loop is driven by it — so a
// drop is a reliability event, not a cosmetic one. Both failure paths are made
// LOUD via schedule_register_failed telemetry (mg-6fe0):
//
//   - nil registrar: pogod's scheduler failed to load, so SetMailCheckRegistrar
//     was never called. Event-only — this is a benign startup condition and the
//     registrar is nil synchronously here, so there is nothing to retry and a
//     mayor nudge would be pure noise.
//   - RegisterMailCheck error: the adapter already ran verify-after-register +
//     retry-once (recovering the transient persist-IO suspect) and escalated to
//     the mayor before returning; a non-nil error here means the entry is
//     genuinely absent after that. Record it so the telemetry is complete.
func (r *Registry) registerPolecatMailCheck(agentName, workItemID string) {
	scheduleKey := workItemID
	if scheduleKey == "" {
		scheduleKey = agentName
	}
	reg := r.getMailCheckRegistrar()
	if reg == nil {
		r.reportScheduleRegisterFailed(agentName, scheduleKey, "nil_registrar")
		return
	}
	if err := reg.RegisterMailCheck(agentName, scheduleKey, PolecatMailCheckCron, PolecatMailCheckMessage(agentName)); err != nil {
		log.Printf("polecat %s: mail-check schedule registration failed after verify+retry: %v", agentName, err)
		r.reportScheduleRegisterFailed(agentName, scheduleKey, "register_error: "+err.Error())
	}
}
