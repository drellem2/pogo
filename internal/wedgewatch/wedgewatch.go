// Package wedgewatch DETECTS an agent that is animating but not working.
//
// # The fault
//
// On 2026-08-04 twelve polecats and the doctor crew agent sat at a Claude Code
// login prompt for THIRTEEN HOURS. On 2026-08-05 it happened again for seven.
// Roughly twenty agent-hours of nothing, and for the whole of both windows
// every liveness instrument pogo has read healthy:
//
//	pogo agent list
//	  teaa9   status=running   uptime=13h44m   last-activity=just now
//
// The agents were not frozen. They were ANIMATING. Claude Code redraws a
// spinner and an elapsed counter while parked at a prompt, and that redraw is
// PTY output — so `last-activity`, which tracks PTY writes, read "just now"
// forever; the process was alive, so status read running; and CPU was near
// zero, which is also what a legitimately blocked agent looks like. Every
// instrument was measuring the animation.
//
// # The two tells, and why the second is the load-bearing one
//
// This package implements the two detectors mg-fc8d asked for, in its order:
//
//  1. A PTY-content check for KNOWN dead-end states — "Please run /login",
//     "API Error: 401", "Unable to connect to API (ENOTFOUND)", the rating
//     dialog, the rate-limit modal. Cheap and direct. See markers.go.
//
//  2. A cross-check of the agent's OWN DECLARED work time against process
//     uptime. The live signature on both nights was a 7h+ uptime beside a
//     counter reading "Baked for 2m 56s". See counter.go.
//
// (2) matters more than (1) even though (1) is what a human would write first,
// because (1) can only ever catch prompts somebody has already enumerated and
// that enumeration is permanently one incident behind. (2) catches the prompt
// nobody has seen yet: it reads the agent's own claim about how long it has
// been working and notices that the claim is impossible.
//
// # What "declared time below uptime" actually means — read this before tuning
//
// The naive form of (2) fires on every healthy agent in the fleet. The declared
// counter measures ONE TURN, not cumulative work, so a perfectly healthy agent
// seven hours into its life and three seconds into a new turn also shows a tiny
// counter beside a huge uptime. A raw ratio is not a wedge signal.
//
// The signal is that the counter is FROZEN. Re-read the incident number: at
// 13h44m of uptime the counter said 2m 56s. Had it been advancing it would have
// said 13h. Had the agent been taking turns it would have shown a different
// value at every sample. A single small value, unchanged across a hold-down
// window that spans several mail-check fires, means no turn has started and
// none has finished — whatever the screen is drawing.
//
// So the gate is: counter parsed, counter UNCHANGED for HoldDown, uptime at
// least Ratio times the frozen value, and uptime past MinUptime. A working
// agent fails the freeze test continuously; a parked-but-healthy one fails it
// every time its 10-minute mail-check fires and runs a turn.
//
// # A 401 after a connectivity failure is ONE signature, not two
//
// mg-fc8d was filed saying the trigger was an interrupted /login that revoked
// the OAuth token. That was WRONG, and the correction is the most important
// thing in this package. Nothing was ever revoked: the refresh grant had 16.5
// days left and the subscription was intact. The actual sequence was
//
//	network outage (ENOTFOUND, ~20:20-20:38Z)
//	  -> an access-token refresh fell inside that window
//	  -> the refresh failed because the network was dead
//	  -> the session surfaced the failure as "401 ... revoked/expired"
//
// The 401 is DOWNSTREAM of the connectivity failure. Concluding "credential
// revoked, page the human" from a 401 alone pages Daniel for a re-login that
// fixes nothing — and the access token turns over about every 8h, so there are
// ~3 refresh windows a day and ANY outage overlapping one reproduces this
// exactly. This is a standing coincidence with a known rate, not a once-off.
//
// classify.go therefore merges the two observations into one cause whenever a
// connectivity failure is within CoincidenceWindow of the 401, and refuses to
// name a revoked credential unless the credential itself says so.
//
// # ENOTFOUND and a poisoned credential need OPPOSITE responses
//
// A failed refresh during an outage wants the agent LEFT ALONE until the
// network is observed back: its context is intact and it resumes once the
// environment permits. A genuinely poisoned credential wants the opposite —
// stop the agent and re-dispatch, because a session that cannot authenticate
// will never resume and holding it burns a slot and a worktree for nothing.
//
// Getting that backwards is expensive in both directions, so when the detector
// cannot tell them apart it says CauseUnknown and ResponseInvestigate. It never
// guesses. In particular a 401 with no connectivity evidence and a credential
// that is READABLE AND IN DATE is UNKNOWN, not "revoked": the credential has
// actively refuted revocation, and the likeliest remaining explanation is a
// connectivity event that aged out of the window.
//
// # No intervention is known to revive a wedged agent — do not imply one
//
// An earlier reading of 2026-08-05 held that a nudge revived the fleet. It is
// FALSE and mayor retracted it with a control (2026-08-05):
//
//	nudges sent during the outage window (10:23Z-17:26Z):  968
//	acks produced by those 968 nudges:                       0
//	acks in the 90s after the network returned:             15
//
// 968 attempts, zero revivals — and crew-doctor, which received no immediate
// nudge at all, woke anyway on an ordinary scheduled fire ten minutes later. So
// a nudge is neither sufficient nor necessary. What changed at ~17:26 was the
// ENVIRONMENT: the network came back, and ordinary nudges started working
// again because everything started working again. Fifteen nudges sent seconds
// before the first ack made a coincidence look like a mechanism.
//
// This package therefore names a RECOVERY CONDITION — connectivity returning —
// and does not name a remedy, because none has been established. Shipping
// "detect ENOTFOUND -> nudge" would be worse than shipping nothing: it would be
// trusted, and it would be 968-for-0. See ResponseAwaitNetworkRecovery.
//
// # The THIRD state: a CPU-starved agent is not a wedged one
//
// There are three states that look identical to every instrument this fleet
// has, not two:
//
//	WEDGED at a dead prompt  -> spinner redraws, last-activity "just now", no progress
//	CPU-STARVED              -> genuinely working,  last-activity "just now", no progress
//	HEALTHY and working      -> last-activity "just now", progress
//
// The counter cross-check separates the first from the third, and mostly
// separates the second too — a starved agent's counter advances honestly,
// because it really has been working for forty minutes; it has just achieved
// almost nothing. But a starved agent BETWEEN turns has a frozen counter for
// the same reason a wedged one does, and on 2026-08-05 pm-onethird watched
// thirteen polecats sit at "last-activity: just now" for hours during a load
// event (1-minute average 300 on a 10-core box) with plain local `git log`
// calls timing out at 180 seconds.
//
// The remedies are opposite AGAIN: a wedged agent needs intervention; a starved
// one needs to be LEFT ALONE and the load reduced. Waking or restarting a
// starved agent destroys real work and adds to the load that caused it.
//
// So when the ONLY evidence is a frozen counter — no dead-end marker, nothing
// the host's CPU could not explain — and the host is measurably saturated, the
// verdict is CauseHostOversubscribed / ResponseReduceLoadNotIntervene:
// "degraded, not wedged". Saturation does NOT reinterpret an enumerated
// finding: a login prompt is not caused by CPU contention.
//
// The instrument is DELIBERATELY NOT the load average, which is what a reader
// of the incident would reach for. internal/hostload disqualified it with a
// measurement (mg-1b8c): a load average of 214 on this very box coincided with
// ~7.5 of 10 cores in use, because Darwin counts uninterruptible-sleep tasks
// too, and a chunk of it was not even the fleet's. The number decided on here
// is used cores against core count, at hostload's own SaturatedAt threshold.
//
// This state is one pogo creates for itself — the 2026-08-05 event was seven
// polecats in one Go repo each running a full double test suite (mg-3977,
// mg-da30) — which is an argument for measuring it rather than assuming it
// away.
//
// # The graceful degradation was itself a silent instrument (mg-20eb)
//
// Everything above is about detectors that read healthy because they could not
// see. This package's own fallback was one, from the day it shipped.
//
// stallOf documents an event-log-silence fallback for the case where the
// declared counter cannot be parsed at all, "so that a harness that renames its
// status line degrades this detector to a coarser one rather than to a silent
// one". It keyed on Observation.EventsLastSeen. Nothing in production ever
// assigned that field — observe() built every live Observation and did not set
// it, and the only writer in the tree was a unit test — so the branch was
// UNREACHABLE outside the suite, and an unparseable counter did not coarsen
// this detector but disabled it.
//
// It went unnoticed for four days because the code shipped on 2026-08-05 and
// the daemon on this box ran ten days behind main; the revision carrying it did
// not start until 09:41 on 08-09. Its entire production lifetime before the
// ticket was 25 minutes, and its hit rate over that lifetime was 0 of 40: every
// pass, every agent, `wedge_watch_error`. Both halves failed at once —
// simultaneously every counter stem stopped matching (see counter.go) and the
// fallback that exists precisely for that turned out to be inert.
//
// Two rules come out of it, and they generalise past this package:
//
//   - A field a fallback keys on is part of the fallback. `IsZero()` on a field
//     nothing writes is not a condition, it is the constant `true`, and a test
//     that sets the field by hand proves only that the branch compiles.
//     buildSnapshot in source.go now assembles everything an Observation
//     carries beyond one agent's self-report, so the fallback is exercised by
//     the production path a test can actually reach.
//   - An error message must say what was established, not what would have been
//     true had the code run. The old blind message asserted the event log had
//     no entry for the identity; nothing had opened the log, and most of those
//     identities plainly had entries. A reader who checks a detector's claim
//     and finds it false stops reading the detector, which costs more than the
//     blindness did.
//
// # Report-only, and deliberately unrouted
//
// mg-fc8d lists a third item — escalate a fleet-level wedge OUTSIDE the wedged
// party, to Daniel rather than to the mayor's inbox. That is the item that
// actually bounds the damage, and it is NOT built here. It is an alerting-policy
// decision reserved to Daniel and he has not ruled, so this package deliberately
// holds NO mail seam at all: there is no MailFunc to configure and no recipient
// to get wrong. It emits events and exposes Latest() for whoever asks.
//
// The assumption this leaves standing, stated rather than chosen: SOMETHING
// must consume these findings, because the 2026-08-04 lesson is precisely that
// a correct alarm delivered only to the wedged party is not an alarm — stall
// watch fired every five minutes for thirteen hours into an inbox nobody
// could read. Wiring a recipient is one call to a notifier once the policy
// exists; see docs/design/wedged-agent-detector.md.
package wedgewatch

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// Default cadences and thresholds for the standing runner.
const (
	// DefaultInterval is how often the runner samples. Matches deafwatch: the
	// hold-downs, not the sampling rate, are what keep this quiet.
	DefaultInterval = 5 * time.Minute

	// DefaultMarkerHoldDown is how long a KNOWN dead-end marker must sit beside
	// a stalled agent before it is a finding.
	//
	// It is not zero, and the reason is this file. An agent writing about the
	// wedge — a ticket, a changelog entry, this package — puts every marker
	// string in its own PTY, and a detector that fired on the text alone would
	// have reported the polecat that built it. mg-4421 hit the same wall with
	// the rating dialog and solved it with an idle gate; this is that gate,
	// expressed as "the agent has also made no progress".
	//
	// Shorter than DefaultFreezeHoldDown because the marker is corroborating
	// evidence: two independent things agree, so less patience is needed.
	DefaultMarkerHoldDown = 10 * time.Minute

	// DefaultFreezeHoldDown is how long the declared counter must hold a single
	// unchanged value before the un-enumerated case is a finding.
	//
	// Sized to span at least two mail-check fires at the fleet's 10-minute
	// cadence. A healthy agent that is merely idle still runs a turn on every
	// fire, which moves the counter; an agent whose counter survives several
	// fires unchanged is absorbing them without running anything, which is the
	// fault.
	DefaultFreezeHoldDown = 30 * time.Minute

	// DefaultMinUptime keeps young agents out. A process that has existed for
	// twenty minutes has not yet had time to produce a discrepancy worth
	// reporting, and spawn is the noisiest part of an agent's life.
	DefaultMinUptime = time.Hour

	// DefaultRatio is the "orders of magnitude below" test from mg-fc8d,
	// spelled as a number: uptime must be at least this many times the frozen
	// declared value. 13h44m beside 2m56s is a ratio of 280; 7h beside 2m56s is
	// 143. Twenty is far below both and still excludes an agent whose counter
	// is merely a bit behind.
	DefaultRatio = 20

	// DefaultCoincidenceWindow is how long a connectivity failure keeps a later
	// 401 explained.
	//
	// The choice is deliberately asymmetric, because the two errors it trades
	// between cost very different amounts. Too SHORT and a merged
	// outage-plus-refresh event gets reported as a poisoned credential, which
	// pages a human for a re-login that fixes nothing — the exact failure the
	// doctor's correction to mg-fc8d exists to prevent. Too LONG and a genuine
	// revocation is reported as an outage artifact, whose response is "verify
	// the network, then nudge" — which fails visibly, immediately, and without
	// waking anybody. Prefer the long window.
	DefaultCoincidenceWindow = 2 * time.Hour

	// DefaultAnimatingWithin is how recently PTY output must have arrived for
	// the agent to count as ANIMATING — the property that fooled every existing
	// instrument. It is recorded on the finding, never gated on: an agent that
	// has gone silent as well is also wedged, and suppressing that would hand
	// back the "absence of evidence read as evidence of health" bug one level
	// down.
	DefaultAnimatingWithin = 5 * time.Minute

	// DefaultRenotifyAfter is how long an UNCHANGED roster stays quiet before
	// the finding is re-emitted.
	DefaultRenotifyAfter = 6 * time.Hour
)

// Event types this package writes to the shared log.
const (
	// EventFired records a confirmed finding.
	EventFired = "wedge_watch_fired"
	// EventPending records an agent entering a hold-down window: seen stalled,
	// not yet long enough to report. Emitted once per entry so the log can tell
	// "we saw it and waited" from "we never saw it".
	EventPending = "wedge_watch_pending"
	// EventCleared records a previously-fired agent resuming.
	EventCleared = "wedge_watch_cleared"
	// EventError records a sample, or an agent within a sample, that could not
	// be judged. A detector that cannot read its source has NOT found a healthy
	// fleet — see internal/credexpiry's absence-as-evidence trap, which is the
	// same rule one layer down.
	EventError = "wedge_watch_error"
)

// Signature is one observable state, not a diagnosis. Several signatures can
// hold at once, and the mapping from signatures to a cause is the whole
// argument of classify.go — never read a signature as a cause.
type Signature string

const (
	// SigLoginPrompt is "Please run /login" on screen.
	SigLoginPrompt Signature = "login_prompt"
	// SigAPI401 is an "API Error: 401" line. NOT by itself evidence that a
	// credential was revoked; see the package doc.
	SigAPI401 Signature = "api_401"
	// SigConnectivity is a name-resolution or connect failure reaching the API.
	SigConnectivity Signature = "connectivity_failure"
	// SigRatingDialog is Claude Code's mid-session rating prompt.
	SigRatingDialog Signature = "rating_dialog"
	// SigRateLimitModal is Claude Code's rate-limit-options modal.
	SigRateLimitModal Signature = "rate_limit_modal"
	// SigDeclaredTimeBelowUptime is the counter/uptime cross-check: the agent's
	// own declared work time, frozen, orders of magnitude below how long its
	// process has existed. This is the one that catches prompts nobody has
	// enumerated.
	SigDeclaredTimeBelowUptime Signature = "declared_time_below_uptime"
)

// Cause is what the detector is willing to say produced the wedge. There is a
// value for "I do not know" and it is used often on purpose.
type Cause string

const (
	// CauseUnknown means the evidence does not distinguish the candidates. It
	// is a first-class answer here, not a fallback: the two responses this
	// package can recommend are opposites, so a guess is worse than a shrug.
	CauseUnknown Cause = "unknown"
	// CauseRefreshFailedDuringOutage is the 2026-08-04 and 2026-08-05 cause: an
	// access-token refresh that fell inside a network outage and surfaced as a
	// 401. NOTHING WAS REVOKED. Do not page anyone to re-login for this.
	CauseRefreshFailedDuringOutage Cause = "refresh_failed_during_outage"
	// CausePoisonedCredential is a credential that is genuinely unusable — and
	// it is only ever named when the credential ITSELF says so, never inferred
	// from a 401.
	CausePoisonedCredential Cause = "poisoned_credential"
	// CauseNetworkDown is a connectivity failure with no auth symptom yet.
	CauseNetworkDown Cause = "network_down"
	// CauseModalWedge is one of the two enumerated Claude Code modals. mg-4421's
	// watcher owns dismissing these; this package only reports that one is
	// still up beside a stalled agent, which means that watcher did not win.
	CauseModalWedge Cause = "modal_wedge"
	// CauseHostOversubscribed is "degraded, not wedged": the agent has made no
	// progress and the HOST has no CPU left to give it. This is a real finding
	// worth reporting and it is NOT a wedge — the agent is fine and the box is
	// not. Reported rather than suppressed because a fleet that assumes its own
	// contention away cannot see the state it creates for itself.
	CauseHostOversubscribed Cause = "host_oversubscribed"
)

// Response is the recommended handling. It is advice carried on a report, not
// an action: this package has no seam through which it could nudge, stop,
// restart or re-dispatch anything.
type Response string

const (
	// ResponseInvestigate is the answer whenever the cause is UNKNOWN. It
	// explicitly does NOT mean "page a human to re-login" — that is the wrong
	// move for the only cause we have ever actually observed here.
	ResponseInvestigate Response = "investigate"
	// ResponseAwaitNetworkRecovery is the outage response, and it is a
	// CONDITION rather than an action: do not stop these agents, and wait for
	// connectivity to be observed back. Their context is intact and they resume
	// once the environment permits.
	//
	// It is deliberately not "nudge them". mayor's control on 2026-08-05 is
	// decisive: 968 nudges inside the outage window produced 0 acks, and
	// crew-doctor — which got no immediate nudge — woke anyway ten minutes
	// later on an ordinary scheduled fire. A nudge is neither sufficient nor
	// necessary; the network returning is what changed. Naming an intervention
	// that merely correlates with recovery would be worse than naming none,
	// because a named remedy gets trusted.
	ResponseAwaitNetworkRecovery Response = "await_network_recovery"
	// ResponseStopAndRedispatch is the poisoned-credential response, and the
	// exact opposite of the one above. A session that genuinely cannot
	// authenticate will never resume on its own, so holding it costs a slot and
	// a worktree for nothing.
	ResponseStopAndRedispatch Response = "stop_and_redispatch"
	// ResponseModalWatcherOwnsIt defers to mg-4421.
	ResponseModalWatcherOwnsIt Response = "modal_watcher_owns_it"
	// ResponseReduceLoadNotIntervene is the starved-agent response, and it is
	// the opposite of intervening: the agent is working and the host is full.
	// Waking or restarting it destroys real work and adds to the load that
	// caused the symptom. Reduce the load; leave the agent alone.
	ResponseReduceLoadNotIntervene Response = "reduce_load_do_not_intervene"
)

// HostView is what the classifier is allowed to know about host contention.
//
// It is a plain struct rather than an internal/hostload import for the same
// reason CredentialView is: the detector stays pure and its tests build
// fixtures by hand. source.go does the conversion.
type HostView struct {
	// Readable is whether host CPU could be measured AT ALL. False means "I
	// could not look" — never "there is headroom". An unreadable host cannot
	// rule CPU starvation out, and the classifier says so rather than
	// proceeding as though it had.
	Readable bool
	// Saturated is whether in-use CPU reached the saturation threshold.
	//
	// The number behind this is USED CORES against CORE COUNT, and it is
	// deliberately NOT the load average — the instrument a reader of the
	// incident report would reach for first. internal/hostload disqualified
	// that one with a measurement on this very box (mg-1b8c): a load average of
	// 214 coincided with roughly 7.5 of 10 cores actually in use, because
	// Darwin's load average counts uninterruptible-sleep tasks as well as
	// runnable ones, and part of what it counted was not the fleet's work at
	// all.
	//
	// Note the limit hostload states for this measure and which is inherited
	// here: CPU time consumed is bounded by the core count, so a host with
	// twice as many runnable tasks as cores reads exactly the same as one with
	// a task per core. This detects "the host is full"; it cannot say how far
	// past full.
	Saturated bool
	// UsedCores and Cores are carried so a report states the measurement it
	// acted on rather than only its conclusion.
	UsedCores float64
	Cores     int
	// Reason explains a non-readable view in fixed language.
	Reason string
}

// CredentialView is what the classifier is allowed to know about the
// credential. It is a plain struct rather than an internal/credexpiry import so
// the detector stays pure and its tests build fixtures by hand — the separation
// internal/deafwatch draws for the same reason (mg-6092, mg-e8e7, mg-5336 are
// three tickets for tests that read the developer's live ~/.pogo).
//
// It carries no token material and has no field capable of holding any.
type CredentialView struct {
	// Readable is whether the credential could be inspected AT ALL. False means
	// "I could not look", which must never be rendered as "fine".
	Readable bool
	// RefreshValid is whether the OAuth refresh grant is still in date.
	//
	// The refresh expiry is the ONLY field consulted, and that is load-bearing.
	// internal/credexpiry's package doc establishes why the 8-hour access-token
	// expiry cannot be used: it is routinely in the past on a perfectly healthy
	// machine because the harness re-mints on demand without rewriting the
	// stored blob. On 2026-08-04 the access token was in fact valid with 7.7h
	// left while every agent was showing 401 — reading it either way would have
	// been noise.
	RefreshValid bool
	// RefreshExpiry is when the refresh grant lapses, carried for the report.
	RefreshExpiry time.Time
	// Reason explains a non-readable view in the fixed vocabulary
	// internal/credexpiry defines. Never interpolated from command output.
	Reason string
}

// Observation is one agent as seen in one sample. Everything the detector needs
// and nothing it does not: no *agent.Agent, no registry, no live pogo state.
type Observation struct {
	// Name is the bare agent name.
	Name string
	// Identity is the event-log identity ("crew-<name>" / "cat-<name>").
	Identity string
	// Type is the agent type as a string ("crew", "polecat").
	Type string
	// Alive is observed liveness.
	Alive bool
	// Uptime is how long the PROCESS has existed. The honest half of the
	// cross-check — it comes from the process table, not from anything the
	// wedged session renders.
	Uptime time.Duration
	// Output is recent RAW PTY bytes, ANSI included. The scanners strip and
	// normalize; handing them raw keeps the normalization in one place and
	// identical to mg-4421's, which is what makes the marker table portable
	// between the two.
	Output []byte
	// LastOutputAt is when the PTY last produced a byte — the instrument that
	// read "just now" for thirteen hours. Recorded so the report can show the
	// animation that fooled it, never used as evidence of health.
	LastOutputAt time.Time
	// EventsLastSeen is the last event-log line for this identity, if known.
	// Used ONLY as a fallback stall signal when the declared counter cannot be
	// parsed. Zero means unknown.
	EventsLastSeen time.Time
	// EventsRead records whether the event log was actually CONSULTED for this
	// identity, which a zero EventsLastSeen cannot tell you on its own: "looked
	// and found nothing" and "never looked" are the same zero.
	//
	// It exists because they were the same zero for the whole of mg-20eb, and
	// the blind error said "the event log has no entry for this identity" on
	// every one of 40 judgements while the log plainly held entries for most of
	// them. An operator who checks a claim like that and finds it false stops
	// reading the detector, which is a worse outcome than the blindness.
	EventsRead bool
}

// EventsIndex is the fleet's event-log recency, read once per sample.
//
// Readable=false is "could not look", on the same footing as CredentialView and
// HostView: it never renders as "this identity has no entries".
type EventsIndex struct {
	// Readable is whether the log was scanned successfully.
	Readable bool
	// Reason says why not, drawn from a fixed vocabulary so it cannot carry
	// file contents. Empty when Readable.
	Reason string
	// LastSeen maps event-log identity ("crew-mayor", "cat-e6cc") to the
	// timestamp of its most recent line. An identity absent from the map was
	// not mentioned anywhere in the scanned log.
	LastSeen map[string]time.Time
}

// Snapshot is one reading of the fleet.
type Snapshot struct {
	// Now is the sample time.
	Now time.Time
	// Scanned is how many agents the registry held.
	Scanned int
	// Agents is the subset the detector had standing to judge (alive, with a
	// PTY buffer to read). Zero length is NOT a clean fleet; the runner reports
	// Scanned alongside it so a reader can tell the two apart.
	Agents []Observation
	// Cred is the fleet-wide credential reading, taken once per sample rather
	// than per agent — there is one credential on the box.
	Cred CredentialView
	// Host is the host-contention reading, likewise once per sample. There is
	// one set of cores on the box, and CPU starvation is a property of it
	// rather than of any agent.
	Host HostView
}

// SourceFunc yields one reading. Production binds a closure over the live agent
// registry (see source.go); tests substitute a fixture.
//
// It returns an error rather than an empty Snapshot when the fleet cannot be
// read, because "nothing is wedged" and "I could not look" must never collapse
// into the same quiet result.
type SourceFunc func(now time.Time) (Snapshot, error)

// Thresholds are the tunable parts of the two checks. Zero values take the
// package defaults; see New.
type Thresholds struct {
	// MarkerHoldDown gates the enumerated-marker check.
	MarkerHoldDown time.Duration
	// FreezeHoldDown gates the counter/uptime cross-check.
	FreezeHoldDown time.Duration
	// MinUptime is the floor below which no cross-check finding is made.
	MinUptime time.Duration
	// Ratio is how many times the frozen declared value uptime must exceed.
	Ratio float64
	// CoincidenceWindow is how long a connectivity failure keeps a later 401
	// explained.
	CoincidenceWindow time.Duration
	// AnimatingWithin is how recently PTY output must have arrived to record
	// the agent as animating.
	AnimatingWithin time.Duration
}

func (t Thresholds) withDefaults() Thresholds {
	if t.MarkerHoldDown == 0 {
		t.MarkerHoldDown = DefaultMarkerHoldDown
	}
	if t.MarkerHoldDown < 0 {
		t.MarkerHoldDown = 0
	}
	if t.FreezeHoldDown == 0 {
		t.FreezeHoldDown = DefaultFreezeHoldDown
	}
	if t.FreezeHoldDown < 0 {
		t.FreezeHoldDown = 0
	}
	if t.MinUptime == 0 {
		t.MinUptime = DefaultMinUptime
	}
	if t.MinUptime < 0 {
		t.MinUptime = 0
	}
	if t.Ratio <= 0 {
		t.Ratio = DefaultRatio
	}
	if t.CoincidenceWindow <= 0 {
		t.CoincidenceWindow = DefaultCoincidenceWindow
	}
	if t.AnimatingWithin <= 0 {
		t.AnimatingWithin = DefaultAnimatingWithin
	}
	return t
}

// Finding is one agent judged wedged, with the evidence that judged it. The
// evidence travels with the verdict on purpose: the 2026-08-04 report was
// believed only once somebody put "13h44m" and "Baked for 3m 2s" beside each
// other, and a finding that states a conclusion without those two numbers asks
// its reader to take it on faith.
type Finding struct {
	Name     string `json:"name"`
	Identity string `json:"identity"`
	Type     string `json:"type"`

	// Uptime is how long the process has existed.
	Uptime time.Duration `json:"uptime"`
	// Declared is the agent's own work counter, and DeclaredRead says whether
	// it could be parsed at all. An unparsed counter is reported, never treated
	// as agreement.
	Declared     time.Duration `json:"declared"`
	DeclaredRead bool          `json:"declared_read"`
	// StalledFor is how long the agent has shown no evidence of progress: the
	// counter frozen at one value, or — when the counter is unreadable — the
	// event log silent.
	StalledFor time.Duration `json:"stalled_for"`
	// StallSource names which of those two produced StalledFor.
	StallSource string `json:"stall_source"`
	// Animating records that the PTY is still producing output. This is the
	// property that made the wedge invisible, so it is stated rather than
	// implied.
	Animating bool `json:"animating"`

	// HostReadable / HostSaturated / HostUsedCores / HostCores carry the
	// contention measurement the verdict was reached under. They are on EVERY
	// finding, not only the oversubscribed ones, because "the host had headroom"
	// is what rules CPU starvation out — and a reader who cannot see that has to
	// take the distinction on faith.
	HostReadable  bool    `json:"host_readable"`
	HostSaturated bool    `json:"host_saturated"`
	HostUsedCores float64 `json:"host_used_cores"`
	HostCores     int     `json:"host_cores"`

	// Signatures are the observable states, sorted. Not a diagnosis.
	Signatures []Signature `json:"signatures"`
	// Cause and Response are classify.go's verdict; Why is its reasoning in one
	// sentence, meant to be read by whoever the finding eventually reaches.
	Cause    Cause    `json:"cause"`
	Response Response `json:"response"`
	Why      string   `json:"why"`
}

func (f Finding) identity() string {
	if f.Identity != "" {
		return f.Identity
	}
	return f.Name
}

// HasSignature reports whether sig is among f's signatures.
func (f Finding) HasSignature(sig Signature) bool { return hasSig(f.Signatures, sig) }

func hasSig(sigs []Signature, want Signature) bool {
	for _, s := range sigs {
		if s == want {
			return true
		}
	}
	return false
}

func sortSigs(sigs []Signature) []Signature {
	out := append([]Signature(nil), sigs...)
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// String renders a finding as one log line: the two numbers first, then the
// verdict.
func (f Finding) String() string {
	declared := "UNREADABLE"
	if f.DeclaredRead {
		declared = f.Declared.Round(time.Second).String()
	}
	sigs := make([]string, 0, len(f.Signatures))
	for _, s := range f.Signatures {
		sigs = append(sigs, string(s))
	}
	return fmt.Sprintf("%s (%s): uptime=%s declared=%s stalled=%s(%s) animating=%t signatures=[%s] cause=%s response=%s",
		f.Name, f.Type, f.Uptime.Round(time.Second), declared,
		f.StalledFor.Round(time.Second), f.StallSource, f.Animating,
		strings.Join(sigs, " "), f.Cause, f.Response)
}

// fingerprint identifies a roster by identity and verdict, not by age. Ages
// advance every tick, so folding them in would re-emit every interval and train
// every reader to filter the sender — which is how a detector stops being an
// alert (internal/ackwatch, same reasoning).
func fingerprint(findings []Finding) string {
	parts := make([]string, 0, len(findings))
	for _, f := range findings {
		parts = append(parts, f.Name+"/"+string(f.Cause))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func names(findings []Finding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, f.Name)
	}
	sort.Strings(out)
	return out
}
