package wedgewatch

import (
	"fmt"
	"time"
)

// This file decides what the observed signatures MEAN, and it is where mg-fc8d's
// two non-negotiable rules live.
//
// # Rule 1: a 401 shortly after a connectivity failure is ONE signature
//
// mg-fc8d was filed blaming an interrupted /login for revoking the OAuth token.
// The doctor refuted it: the refresh grant had 16.5 days left, the subscription
// was intact, nothing was revoked, and nobody logged in to fix it — every agent
// resumed on the same credential. What actually happened was a network outage,
// an access-token refresh that fell inside it, and a failed refresh surfacing as
// "401 ... revoked/expired".
//
// So this classifier will not read a 401 as a revoked credential. If a
// connectivity failure is within CoincidenceWindow, the two collapse into
// CauseRefreshFailedDuringOutage. The cost of getting this wrong is specific and
// known: it pages Daniel for a re-login THAT FIXES NOTHING, and it hides a cause
// that recurs about three times a day (the access token turns over roughly every
// 8h, and any outage overlapping a refresh window reproduces it exactly).
//
// # Rule 2: ENOTFOUND and a poisoned credential get OPPOSITE responses
//
// A refresh that failed inside an outage wants the agent left alone until
// connectivity is observed back — its context is intact and it resumes when the
// environment permits. A genuinely poisoned credential wants the agent stopped
// and re-dispatched, because it will never resume.
//
// Since the responses are opposites, a guess is worse than a shrug. When the
// evidence does not separate them this returns CauseUnknown /
// ResponseInvestigate, and says which piece of evidence was missing.
//
// # What is NOT here: an intervention
//
// ResponseAwaitNetworkRecovery names a CONDITION, not a remedy. mayor's control
// on 2026-08-05 — 968 nudges inside the outage window, 0 acks; and crew-doctor,
// which got no nudge, waking anyway on a routine fire — establishes that no
// intervention is known to revive a wedged agent. Naming one that merely
// correlates with recovery would be worse than naming none, because it would be
// believed.

// Evidence is everything the classifier gets. It is deliberately small: three
// booleans derived from the PTY, one timestamp of fleet memory, and a credential
// view that carries no token material.
type Evidence struct {
	// Signatures are the observed states for this agent.
	Signatures []Signature
	// LastConnFailure is the most recent connectivity failure observed ANYWHERE
	// in the fleet, not just on this agent. A network outage is a property of
	// the box, and on 2026-08-04 the two halves of the evidence were split
	// across observers — mayor read the 401 in a PTY, the doctor read ENOTFOUND
	// in the logs — which is exactly how they were mistaken for two events.
	// Zero means none remembered.
	LastConnFailure time.Time
	// Cred is the fleet-wide credential reading.
	Cred CredentialView
	// Host is the host-contention reading. It reinterprets ONE case — see
	// Classify's final branches — and is carried on every verdict regardless,
	// because "the host had headroom" is what positively rules CPU starvation
	// out.
	Host HostView
	// Now is the classification time.
	Now time.Time
}

// Verdict is the classifier's answer.
type Verdict struct {
	Cause    Cause
	Response Response
	// Why is one sentence of reasoning, written for whoever the finding
	// eventually reaches rather than for a log parser.
	Why string
}

// Classify maps evidence to a cause and a recommended response.
func Classify(ev Evidence, th Thresholds) Verdict {
	th = th.withDefaults()

	auth := hasSig(ev.Signatures, SigLoginPrompt) || hasSig(ev.Signatures, SigAPI401)
	// Connectivity counts if this agent showed it, or if the fleet saw one
	// recently. The window is what merges an outage with a 401 that surfaces
	// after it — see DefaultCoincidenceWindow for why it is set long rather
	// than short.
	conn := hasSig(ev.Signatures, SigConnectivity)
	connRemembered := !ev.LastConnFailure.IsZero() &&
		!ev.Now.IsZero() &&
		ev.Now.Sub(ev.LastConnFailure) <= th.CoincidenceWindow
	modal := hasSig(ev.Signatures, SigRatingDialog) || hasSig(ev.Signatures, SigRateLimitModal)

	switch {
	case auth && (conn || connRemembered):
		return Verdict{
			Cause:    CauseRefreshFailedDuringOutage,
			Response: ResponseAwaitNetworkRecovery,
			Why: fmt.Sprintf(
				"a 401/login prompt WITH a connectivity failure %s is ONE signature, not two: "+
					"an access-token refresh fell inside a network outage and the failure surfaced as 401. "+
					"NOTHING IS REVOKED — do not page anyone to re-login, it fixes nothing. "+
					"The recovery condition is connectivity returning; no intervention is known to cause revival "+
					"(968 nudges inside the 2026-08-05 outage produced 0 acks).",
				connPhrase(conn, connRemembered, ev, th)),
		}

	case auth && ev.Cred.Readable && !ev.Cred.RefreshValid:
		return Verdict{
			Cause:    CausePoisonedCredential,
			Response: ResponseStopAndRedispatch,
			Why: fmt.Sprintf(
				"a 401/login prompt with NO connectivity failure in the last %s, and the credential ITSELF "+
					"says it is unusable (refresh grant lapsed %s). This is the one case where the "+
					"credential is genuinely at fault, and it is named because the credential said so — "+
					"never inferred from the 401. The session will not resume; stop it and re-dispatch "+
					"rather than holding a slot and a worktree.",
				th.CoincidenceWindow, ev.Cred.RefreshExpiry.UTC().Format(time.RFC3339)),
		}

	case auth && ev.Cred.Readable && ev.Cred.RefreshValid:
		return Verdict{
			Cause:    CauseUnknown,
			Response: ResponseInvestigate,
			Why: fmt.Sprintf(
				"a 401/login prompt with NO connectivity failure in the last %s — but the credential is "+
					"READABLE AND IN DATE (refresh grant good until %s), which REFUTES revocation. "+
					"The likeliest remaining explanation is a connectivity event that aged out of the "+
					"window, so treat this as UNKNOWN. Do NOT page for a re-login: on the only two "+
					"occasions this fleet has seen the symptom, nothing was revoked and no login was "+
					"performed.",
				th.CoincidenceWindow, ev.Cred.RefreshExpiry.UTC().Format(time.RFC3339)),
		}

	case auth:
		return Verdict{
			Cause:    CauseUnknown,
			Response: ResponseInvestigate,
			Why: fmt.Sprintf(
				"a 401/login prompt with NO connectivity failure in the last %s and NO readable "+
					"credential (%s). The two candidates — a refresh that failed during an outage, and a "+
					"genuinely poisoned credential — need OPPOSITE responses, and nothing here separates "+
					"them, so the detector declines to guess. Read the credential's refresh expiry to "+
					"resolve it; do not page for a re-login on the 401 alone.",
				th.CoincidenceWindow, credReason(ev.Cred)),
		}

	case conn || connRemembered:
		return Verdict{
			Cause:    CauseNetworkDown,
			Response: ResponseAwaitNetworkRecovery,
			Why: fmt.Sprintf(
				"a connectivity failure %s with no auth symptom yet. Leave the agent alone: its context "+
					"is intact and it resumes when the network does. Expect 401s to follow if a token "+
					"refresh falls inside the outage — those would be the SAME event, not a new one.",
				connPhrase(conn, connRemembered, ev, th)),
		}

	case modal:
		return Verdict{
			Cause:    CauseModalWedge,
			Response: ResponseModalWatcherOwnsIt,
			Why: "an enumerated Claude Code modal is on screen beside a stalled agent. mg-4421's modal " +
				"watcher owns dismissing these, so a finding here means it did not win — which is worth " +
				"knowing, because mg-f36b is a ticket about that watcher being silently unable to match " +
				"its own marker for two months.",
		}

	// From here down there is NO enumerated dead-end state on screen — the only
	// evidence is that the agent's own counter has been frozen far below its
	// uptime. That is the one case host contention can explain, so it is the
	// only case host contention is allowed to reinterpret. A login prompt or an
	// ENOTFOUND is not caused by a full CPU, and the branches above never reach
	// here.

	case ev.Host.Readable && ev.Host.Saturated:
		return Verdict{
			Cause:    CauseHostOversubscribed,
			Response: ResponseReduceLoadNotIntervene,
			Why: fmt.Sprintf(
				"DEGRADED, NOT WEDGED. The agent has made no progress and the host has no CPU left to "+
					"give it: %.1f of %d cores in use. Nothing on screen suggests a dead end, so "+
					"starvation explains the silence — on 2026-08-05 thirteen polecats read "+
					"'last-activity: just now' for hours during a load event while plain local `git log` "+
					"calls timed out at 180s. LEAVE THE AGENT ALONE and reduce the load: waking or "+
					"restarting it destroys real work and adds to the load that caused this. "+
					"(Measured as used cores against core count, NOT the load average — a load average "+
					"of 214 on this box once coincided with 7.5 of 10 cores actually in use, mg-1b8c.)",
				ev.Host.UsedCores, ev.Host.Cores),
		}

	case !ev.Host.Readable:
		return Verdict{
			Cause:    CauseUnknown,
			Response: ResponseInvestigate,
			Why: fmt.Sprintf(
				"no enumerated dead-end state is on screen, but the agent's own work counter has been "+
					"frozen far below its process uptime — and HOST CPU COULD NOT BE MEASURED (%s), so "+
					"CPU starvation could not be ruled out. A starved agent looks identical to a wedged "+
					"one here and needs the OPPOSITE handling (leave it alone, reduce load), so this "+
					"stays UNKNOWN. Measure the host, then read the PTY "+
					"(`pogo agent output <name>`).",
				hostReason(ev.Host)),
		}

	default:
		return Verdict{
			Cause:    CauseUnknown,
			Response: ResponseInvestigate,
			Why: fmt.Sprintf(
				"no enumerated dead-end state is on screen, but the agent's own work counter has been "+
					"frozen far below its process uptime — and the host had headroom (%.1f of %d cores "+
					"in use), which RULES OUT CPU starvation as the explanation. This is the case the "+
					"enumeration cannot cover and the reason the cross-check exists: a prompt nobody has "+
					"met yet looks exactly like this. Read the PTY (`pogo agent output <name>`) and add "+
					"whatever is there to DefaultMarkers.",
				ev.Host.UsedCores, ev.Host.Cores),
		}
	}
}

func hostReason(h HostView) string {
	if h.Reason != "" {
		return h.Reason
	}
	return "host load was not sampled"
}

func connPhrase(direct, remembered bool, ev Evidence, th Thresholds) string {
	switch {
	case direct:
		return "on this agent's own PTY"
	case remembered:
		return fmt.Sprintf("observed fleet-wide %s ago (within the %s coincidence window)",
			ev.Now.Sub(ev.LastConnFailure).Round(time.Second), th.CoincidenceWindow)
	default:
		return ""
	}
}

func credReason(c CredentialView) string {
	if c.Reason != "" {
		return c.Reason
	}
	return "the credential was not inspected"
}
