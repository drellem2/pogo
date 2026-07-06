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
// exists to prevent (mg-e633). mailbox is the identity the polecat reads mail
// under (its work item id, or its agent name when no work item is set).
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

// registerPolecatMailCheck best-effort registers the mail-check loop for a
// freshly spawned polecat. workItemID falls back to the agent name when the
// spawn carried no work item id, so every polecat gets a specific,
// reap-matchable schedule id. Failure is logged, never fatal: the polecat is
// already running and a missing mail-check only degrades proactive reachability.
func (r *Registry) registerPolecatMailCheck(agentName, workItemID string) {
	reg := r.getMailCheckRegistrar()
	if reg == nil {
		return
	}
	mailbox := workItemID
	if mailbox == "" {
		mailbox = agentName
	}
	if err := reg.RegisterMailCheck(agentName, mailbox, PolecatMailCheckCron, PolecatMailCheckMessage(mailbox)); err != nil {
		log.Printf("polecat %s: mail-check schedule registration failed: %v", agentName, err)
	}
}
