package agent

// Wake-cycle policy — the two SUPPRESSIONS (mg-8184, from mg-1c0c candidate (e)).
//
// A WAKE is an automated nudge whose only purpose is to rouse an agent that is
// not doing anything: the stall watcher's fire, a scheduler fire landing on a
// live PTY. Two rules constrain it:
//
//  1. wake each silence once — do not re-wake a silence that an earlier wake
//     already spoke into and that has said nothing since.
//  2. never wake inside a known limit episode — an agent wedged on a provider
//     usage-limit modal cannot act on a nudge, so writing to it buys a
//     nudge_sent event and no work.
//
// Both are SUPPRESSIONS. They only ever make the wake path fire LESS, and that
// is what makes them safe to build while the detectors stay report-only
// (internal/deafwatch's package doc, internal/ackwatch, internal/synthwatch):
// the worst case of a wrong or confused answer here is a wake that does not
// happen, which is the behaviour every one of these call sites already had. A
// suppression cannot be exploited by a mistaken detector the way a trigger can.
//
// # The third rule is deliberately absent
//
// The policy as originally written had a third rule — "always nudge once when
// the episode clears". It is NOT implemented here, and its absence is not an
// oversight or a judgement that it is low value; it is the highest-value of the
// three. It is a TRIGGER: nothing else watches for the clear, so implementing
// it means a detector observing a transition and causing a nudge — exactly the
// detector→actor seam the report-only stance exists to keep closed. Whether to
// open that seam is a decision about the stance, not a build. Do not add it
// here; it needs its own ticket.
//
// # Every suppression is BOUNDED (mg-3a8a)
//
// Both rules above are suppressions, and a suppression with no ceiling is not a
// debounce — it is a mute button that engages itself. Rule 1 is the measured
// case: its suppressing condition is "the agent has produced no PTY output since
// we woke it", which is EXACTLY the condition a wake exists to break. Once an
// agent goes silent, every subsequent wake is declined by the fact that the
// previous one did not work. Over 2026-08-14..19 that declined 143 consecutive
// wakes to crew-pa across 106 hours, the "already woken N ago" age climbing
// monotonically and never resetting; the only exit was an operator running
// `pogo agent stop`/`start` out of band. Nothing inside the system could have
// recovered it, because the scheduler is the only thing that wakes crew agents.
//
// The population contained its own control: over the same window crew-mayor's
// suppressions all read "already woken 0s ago" — the intended debounce, firing
// and releasing normally. Same rule, same daemon, two regimes, and nothing in
// the rule distinguished 0s from 106h.
//
// So the policy carries a bound that sits ABOVE the rules rather than inside one
// of them: a run of consecutive suppressions may not outlive
// DefaultWakeSuppressionBound. Past it the next wake is RELEASED — delivered
// regardless of which rule declined it — and the run stays released until a wake
// actually lands. See noteSuppression.
//
// The bound is on rule 2 as well, and that is deliberate rather than incidental.
// Rule 2's clearing signal is the agent's event log advancing (internal/claude's
// modal hook clears the flag only when the agent produces events again), which
// is the same shape: a suppression whose release depends on activity from the
// agent it is suppressing wakes for. Bounding at the policy layer covers both
// rules and any rule added later, which is the property a per-rule fix would not
// have had.
//
// What a released wake costs in the case the bound is wrong: one nudge_sent and
// no work — a wake written to an agent that could not act on it. That is the
// pre-policy behaviour, once per bound period, which is the ceiling the safety
// argument above already accepts. What an unbounded suppression costs is an
// agent no part of the system can reach.
//
// # Direction: PULL, never PUSH
//
// The nudge path had no episode awareness before this file, so the policy could
// not be implemented purely inside the nudge decision — that path has to learn
// episode state from somewhere. It ASKS, at the moment it is about to write:
//
//	PULL (this file)  the wake decision, already about to fire, asks
//	                  "are we in an episode?" and suppresses itself.
//	PUSH (forbidden)  a detector observes state and reaches into the nudge path.
//
// That is why the seam is a query FUNCTION installed by the composition root
// (cmd/pogod) rather than a callback a detector holds. internal/claude, which
// owns the fleet usage-limit episode, gains a read-only accessor and no
// knowledge whatsoever of this path; internal/agent gains no import of it.
// Nothing in a detector package calls into this file — TestNoDetectorCallsIntoWakePath
// pins that. If a future change adds such a call the direction has been
// inverted and the safety argument above no longer holds.

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Rule identifiers. They name the suppressing rule in the returned error and in
// the nudge_suppressed event, so "the nudge did not fire" is never recorded
// without which rule declined it — a suppression whose reason is not observable
// is indistinguishable from the delivery bug it is supposed to prevent.
const (
	// RuleWakeSilenceOnce is rule 1: this silence has already been woken.
	RuleWakeSilenceOnce = "wake_silence_once"
	// RuleLimitEpisode is rule 2: a provider limit episode is known to be open.
	RuleLimitEpisode = "limit_episode"
)

// DefaultWakeSuppressionBound is how long an unbroken run of suppressions may
// continue before the policy releases a wake regardless of the rule that
// declined it (mg-3a8a).
//
// The bound is on ELAPSED TIME, not on a count of suppressions, and the choice
// matters. A count means different things at different fire cadences — ten
// declines is 100 minutes for an agent on a `*/10` mail-check and ten seconds
// for something firing every second — so a count bound is either useless or
// defeats the debounce depending on traffic nobody controls at this layer.
// Elapsed silence is the invariant the incident was actually measured in. The
// run count is still recorded on every event, because it is what a reader greps
// for; it just does not decide anything.
//
// Why fifteen minutes. It is far longer than the debounce needs — the intended
// case is two fires landing in one wake cycle, seconds apart (crew-mayor's
// suppressions all read "0s") — so it costs the debounce nothing real. And it is
// long enough that a released wake cannot be mistaken for chatter: every harness
// pogo drives animates its PTY while it works, so fifteen minutes with not one
// byte written is not a working agent under any provider we ship. The value is
// per-agent overridable (Agent.wakeSuppressionBound) so tests need not sleep it
// out.
const DefaultWakeSuppressionBound = 15 * time.Minute

// EventNudgeSuppressed is the event type recorded for a wake the policy
// declined. It is the counterpart of nudge_sent: between them, every wake this
// process decided on leaves exactly one record.
const EventNudgeSuppressed = "nudge_suppressed"

// EventWakeSuppressionReleased is recorded when the bound fires: a wake that a
// rule declined is delivered anyway because the run of suppressions has outlived
// DefaultWakeSuppressionBound.
//
// It is its own event rather than a field on nudge_sent because it is the only
// record that the bound exists and works. A release that looked exactly like an
// ordinary wake would leave the bound in the same position the defect was in —
// a mechanism whose operation cannot be observed from outside, which is
// indistinguishable from one that was never wired up.
const EventWakeSuppressionReleased = "wake_suppression_released"

// ErrWakeSuppressed is wrapped by every suppression so a caller can tell a
// policy decline from a delivery failure with errors.Is.
//
// It is deliberately an ERROR rather than a silent nil. A suppressed wake wrote
// nothing, and reporting success for an undelivered message is its own defect
// (mg-ebee is in flight against precisely that). Callers that hold a durable
// fallback — the stall nudger, the scheduler deliverer — therefore still reach
// the recipient by mail; only the PTY wake is suppressed.
var ErrWakeSuppressed = errors.New("wake suppressed by wake-cycle policy")

// LimitEpisodeQuery reports whether a provider limit episode is currently known
// to be open, with a short human-readable detail naming it. It is the PULL seam
// for rule 2: the wake decision calls it, and the answering package never calls
// back.
//
// It answers for the FLEET, because that is the unit a usage-limit episode has
// — the provider's account-level limit hits every agent on that provider at
// once, which is why internal/claude coalesces per-agent hits into one episode
// in the first place. A per-agent answer is available separately and more
// cheaply from Agent.IsRateLimited, and evaluateWake asks that first.
type LimitEpisodeQuery func() (open bool, detail string)

var (
	limitEpisodeMu    sync.RWMutex
	limitEpisodeQuery LimitEpisodeQuery
)

// SetLimitEpisodeQuery installs the query the wake decision asks before every
// wake. pogod installs claude.UsageLimitEpisodeOpen at startup; passing nil
// clears it.
//
// A process with no query installed — a bare registry, a unit test, a library
// caller — simply never suppresses on the fleet rule. That is the correct
// default for a suppression: absent knowledge, behave as before.
func SetLimitEpisodeQuery(q LimitEpisodeQuery) {
	limitEpisodeMu.Lock()
	defer limitEpisodeMu.Unlock()
	limitEpisodeQuery = q
}

// askLimitEpisode performs the pull. A nil query answers "not known to be in an
// episode", never "in one".
func askLimitEpisode() (bool, string) {
	limitEpisodeMu.RLock()
	q := limitEpisodeQuery
	limitEpisodeMu.RUnlock()
	if q == nil {
		return false, ""
	}
	return q()
}

// wakeDecision is one evaluation of the policy. An empty Rule means the wake may
// proceed; the pass-through case carries no detail because there is nothing to
// explain about a nudge that fired.
//
// Released is the bound's answer (mg-3a8a): a Rule DID decline this wake, and it
// is being delivered anyway because the run of suppressions has outlived the
// bound. Such a decision reports suppressed() == false — the wake fires — while
// still carrying the rule and reason, because "which rule we overrode" is the
// interesting half of a release.
type wakeDecision struct {
	Rule   string
	Detail string

	// Released is set when the bound overrode the rule named in Rule.
	Released bool
	// Count is how many wakes the current run of suppressions has declined,
	// this one included; Age is how long that run has lasted. Both are zero on
	// a decision no rule declined.
	Count int
	Age   time.Duration
}

func (d wakeDecision) suppressed() bool { return d.Rule != "" && !d.Released }

// evaluateWake asks the rules and then applies the BOUND, which is the only
// thing that can turn a rule's decline back into a delivery.
//
// The order is deliberate: the rules decide first and without any knowledge of
// the bound, so each rule stays a statement about the agent alone; the bound
// then decides how long ANY such statement is allowed to keep a wake from
// landing. A rule cannot opt out of it, and a rule added later inherits it
// without having to remember to.
//
// It mutates the run bookkeeping, so it is called exactly once per wake attempt
// — from NudgeWake — and never as a peek.
func (a *Agent) evaluateWake(now time.Time) wakeDecision {
	d := a.rulesDecision(now)
	if !d.suppressed() {
		// The wake is going out on its own merits: whatever run was open is
		// over. (A DELIVERED wake also resets it, in recordWake — this branch
		// covers the case where the rules simply stopped declining.)
		a.endSuppressionRun()
		return d
	}

	count, age, release := a.noteSuppression(now)
	d.Count, d.Age = count, age
	if !release {
		return d
	}

	// Past the bound. Deliver, and say so — the run is deliberately NOT reset
	// here. It ends when a wake actually LANDS (recordWake). Resetting on the
	// decision instead would mean a release whose delivery failed on a busy PTY
	// bought another full bound period of silence, which is the defect in
	// miniature: a remedy cancelled by the failure it exists to survive.
	d.Released = true
	d.Detail = fmt.Sprintf(
		"%s — released by the wake-suppression bound after %s and %d consecutive suppression(s) (bound %s)",
		d.Detail, age.Round(time.Second), count, a.suppressionBound())
	return d
}

// rulesDecision asks both rules and returns the first that declines. It knows
// nothing about the bound.
//
// Rule 2 is asked first. It is the more consequential answer — an agent inside a
// limit episode cannot act on a wake at all — whereas rule 1 only says this
// particular silence has already been spoken into, which is a weaker claim about
// a healthier agent. Reporting the stronger reason when both hold is what makes
// the event log readable.
func (a *Agent) rulesDecision(now time.Time) wakeDecision {
	// The per-agent answer, already maintained by the modal watcher via
	// SetRateLimited and already read by `pogo status` and `pogo agent
	// diagnose`. Reading it here is a pull: the watcher records a condition on
	// the agent, it does not reach into the nudge path.
	if a.IsRateLimited() {
		return wakeDecision{
			Rule:   RuleLimitEpisode,
			Detail: fmt.Sprintf("agent %s is flagged rate-limited", a.Name),
		}
	}
	if open, detail := askLimitEpisode(); open {
		if detail == "" {
			detail = "a provider limit episode is open"
		}
		return wakeDecision{Rule: RuleLimitEpisode, Detail: detail}
	}
	if d, ok := a.sameSilenceAsLastWake(now); ok {
		return d
	}
	return wakeDecision{}
}

// sameSilenceAsLastWake reports whether the agent is still in the SAME unbroken
// silence that a previous wake already spoke into.
//
// The definition has to survive the PTY's own echo. Bytes written to the master
// come back through readOutput into outputBuf and bump LastWriteTime — the same
// mechanism documented on Agent.applyResize, where a redraw's bytes defeat the
// wait-idle gate. So "has the agent written anything since we woke it" answers
// YES after every wake, including one that landed on a wedged harness and
// produced nothing but a re-rendered composer. Read that literally and rule 1
// would suppress nothing, ever.
//
// The silence is therefore broken only by output that arrives after the wake has
// SETTLED: a LastWriteTime later than lastWakeAt + IdleThreshold. The window is
// the provider's own NudgeProfile.IdleThreshold (2s by default), which is
// already this codebase's answer to "how long must a PTY be quiet before we call
// it quiet". An echo or a composer redraw lands inside it; a turn the agent
// actually ran runs long past it — which is exactly the discrimination rule 1
// needs, since an agent that did work is no longer in the silence we woke.
func (a *Agent) sameSilenceAsLastWake(now time.Time) (wakeDecision, bool) {
	a.wakeMu.Lock()
	last := a.lastWakeAt
	a.wakeMu.Unlock()
	if last.IsZero() {
		// Never woken by this process: whatever silence it is in, it is one we
		// have not spoken into. Rule 1 permits the FIRST wake by construction.
		return wakeDecision{}, false
	}

	settle := a.nudge.IdleThreshold
	if settle <= 0 {
		settle = DefaultNudgeProfile.IdleThreshold
	}

	var lastWrite time.Time
	if a.outputBuf != nil {
		lastWrite = a.outputBuf.LastWriteTime()
	}
	if lastWrite.After(last.Add(settle)) {
		return wakeDecision{}, false // it spoke after the wake settled: a new silence
	}

	return wakeDecision{
		Rule: RuleWakeSilenceOnce,
		Detail: fmt.Sprintf(
			"agent %s was already woken %s ago and has produced no PTY output since that wake settled (%s)",
			a.Name, now.Sub(last).Round(time.Second), settle),
	}, true
}

// suppressionBound is this agent's ceiling on a run of consecutive
// suppressions. A zero or negative per-agent value means the default, matching
// how sameSilenceAsLastWake treats a missing IdleThreshold.
func (a *Agent) suppressionBound() time.Duration {
	a.wakeMu.Lock()
	b := a.wakeSuppressionBound
	a.wakeMu.Unlock()
	if b <= 0 {
		return DefaultWakeSuppressionBound
	}
	return b
}

// noteSuppression records one suppression against the current run and reports
// whether the run has outlived the bound.
//
// The run's clock starts at the FIRST suppression of the run and is not touched
// again until the run ends, so the age it returns is the true length of the
// unbroken decline — not the gap since the previous wake attempt, which would
// reset on every fire and could never accumulate. It is also independent of
// lastWakeAt: rule 2 can decline a wake for an agent this process has never
// woken, and that run needs a clock too.
//
// The first suppression of a run has age 0, so it always declines: the bound can
// only release a wake that an earlier attempt already declined, never the one
// that opens the run. That is what keeps the debounce intact — two fires in one
// wake cycle are seconds apart and the second is the run's first.
func (a *Agent) noteSuppression(now time.Time) (count int, age time.Duration, release bool) {
	bound := a.suppressionBound()

	a.wakeMu.Lock()
	if a.suppressRunSince.IsZero() {
		a.suppressRunSince = now
		a.suppressRunCount = 0
	}
	a.suppressRunCount++
	count = a.suppressRunCount
	since := a.suppressRunSince
	a.wakeMu.Unlock()

	age = now.Sub(since)
	if age < 0 {
		// A clock step backwards must not extend a run indefinitely; treat it
		// as no elapsed time rather than as negative time.
		age = 0
	}
	return count, age, age >= bound
}

// endSuppressionRun closes the current run. Called when the rules let a wake
// through and when one is delivered — both are evidence the run is over.
func (a *Agent) endSuppressionRun() {
	a.wakeMu.Lock()
	a.suppressRunSince = time.Time{}
	a.suppressRunCount = 0
	a.wakeMu.Unlock()
}

// NudgeWake delivers a WAKE nudge — an automated nudge whose purpose is to rouse
// an agent — subject to the wake-cycle policy documented at the top of this
// file. It is the ONLY entry point the policy governs, so which nudges are wakes
// is a decision written at the call site rather than inferred from a mode.
//
// On a suppression it records EventNudgeSuppressed and returns an error wrapping
// ErrWakeSuppressed. Callers with a durable channel should fall back to it: the
// policy suppresses the PTY WAKE, not the information.
//
// Deliberately NOT routed through here:
//
//   - The spawn kickoff (NudgeWaitReady from Registry.Spawn / Respawn). A
//     kickoff is not a wake, and the safety argument does not cover it: for a
//     repeat wake the worst case of a suppression is the pre-existing
//     behaviour, but a suppressed kickoff strands a freshly spawned polecat that
//     nothing will nudge again — rule 3, the one rule that would recover it, is
//     out of scope by design.
//   - Operator nudges (`pogo nudge`, POST /agents/{name}/nudge). A person typing
//     a nudge has already made by hand the decision this policy automates, and
//     silently declining it would be a worse surprise than a redundant write.
func (a *Agent) NudgeWake(msg string, mode NudgeMode, timeout time.Duration, corr string) error {
	d := a.evaluateWake(time.Now())
	if d.suppressed() {
		emitNudgeSuppressed(a, msg, d, corr)
		return fmt.Errorf("%w: %s (rule %s)", ErrWakeSuppressed, d.Detail, d.Rule)
	}
	if d.Released {
		emitWakeSuppressionReleased(a, msg, d, corr)
	}
	if err := a.NudgeWithModeCorrelated(msg, mode, timeout, corr); err != nil {
		// Not delivered, so this silence has NOT been woken: leave lastWakeAt
		// alone so the next attempt is judged on its own. A failed wake that
		// suppressed the retry would turn one busy-PTY error into permanent
		// silence.
		return err
	}
	a.recordWake(time.Now())
	return nil
}

// recordWake stamps the moment a wake was actually written to the PTY and closes
// any open suppression run. Only a DELIVERED wake counts — see NudgeWake.
//
// Delivery is the ONLY thing that ends a run, which is what makes the bound
// self-clearing in the case that matters: past the bound every attempt is
// released until one of them lands, so a busy PTY or a failed write postpones
// the release rather than cancelling it.
func (a *Agent) recordWake(at time.Time) {
	a.wakeMu.Lock()
	a.lastWakeAt = at
	a.suppressRunSince = time.Time{}
	a.suppressRunCount = 0
	a.wakeMu.Unlock()
}

// emitNudgeSuppressed records a wake the policy declined. Shape mirrors
// emitNudgeSent — same actor, same recipient field, same optional correlation id
// — so a reader joining on fire_token sees the suppression exactly where the
// delivery would have been rather than seeing nothing at all.
func emitNudgeSuppressed(a *Agent, msg string, d wakeDecision, corr string) {
	details := wakeEventDetails(a, msg, d, corr)
	events.Emit(context.Background(), events.Event{
		EventType: EventNudgeSuppressed,
		Agent:     "pogod",
		Details:   details,
	})
}

// emitWakeSuppressionReleased records a wake the bound let through over a rule's
// decline (mg-3a8a). Same shape as the suppression it replaces, so a reader
// following one agent's run sees the release land in the same stream as the
// declines that led to it rather than having to join two.
func emitWakeSuppressionReleased(a *Agent, msg string, d wakeDecision, corr string) {
	details := wakeEventDetails(a, msg, d, corr)
	details["bound_seconds"] = a.suppressionBound().Seconds()
	events.Emit(context.Background(), events.Event{
		EventType: EventWakeSuppressionReleased,
		Agent:     "pogod",
		Details:   details,
	})
}

// wakeEventDetails is the shared payload of both policy events.
//
// consecutive and suppressed_for_seconds are structured fields rather than prose
// in `reason` on purpose: the run length was the load-bearing number in mg-3a8a
// and reading it required regexing an age out of an English sentence, which is
// how a run climbing past 100 hours stayed a thing you had to already suspect in
// order to see.
func wakeEventDetails(a *Agent, msg string, d wakeDecision, corr string) map[string]any {
	details := map[string]any{
		"to":                     a.eventAgent(),
		"message":                msg,
		"rule":                   d.Rule,
		"reason":                 d.Detail,
		"consecutive":            d.Count,
		"suppressed_for_seconds": d.Age.Seconds(),
	}
	if corr != "" {
		details["fire_token"] = corr
	}
	return details
}
