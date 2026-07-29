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

// EventNudgeSuppressed is the event type recorded for a wake the policy
// declined. It is the counterpart of nudge_sent: between them, every wake this
// process decided on leaves exactly one record.
const EventNudgeSuppressed = "nudge_suppressed"

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
type wakeDecision struct {
	Rule   string
	Detail string
}

func (d wakeDecision) suppressed() bool { return d.Rule != "" }

// evaluateWake asks both rules and returns the first that declines.
//
// Rule 2 is asked first. It is the more consequential answer — an agent inside a
// limit episode cannot act on a wake at all — whereas rule 1 only says this
// particular silence has already been spoken into, which is a weaker claim about
// a healthier agent. Reporting the stronger reason when both hold is what makes
// the event log readable.
func (a *Agent) evaluateWake(now time.Time) wakeDecision {
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
	if d := a.evaluateWake(time.Now()); d.suppressed() {
		emitNudgeSuppressed(a, msg, d, corr)
		return fmt.Errorf("%w: %s (rule %s)", ErrWakeSuppressed, d.Detail, d.Rule)
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

// recordWake stamps the moment a wake was actually written to the PTY. Only a
// DELIVERED wake counts — see NudgeWake.
func (a *Agent) recordWake(at time.Time) {
	a.wakeMu.Lock()
	a.lastWakeAt = at
	a.wakeMu.Unlock()
}

// emitNudgeSuppressed records a wake the policy declined. Shape mirrors
// emitNudgeSent — same actor, same recipient field, same optional correlation id
// — so a reader joining on fire_token sees the suppression exactly where the
// delivery would have been rather than seeing nothing at all.
func emitNudgeSuppressed(a *Agent, msg string, d wakeDecision, corr string) {
	details := map[string]any{
		"to":      a.eventAgent(),
		"message": msg,
		"rule":    d.Rule,
		"reason":  d.Detail,
	}
	if corr != "" {
		details["fire_token"] = corr
	}
	events.Emit(context.Background(), events.Event{
		EventType: EventNudgeSuppressed,
		Agent:     "pogod",
		Details:   details,
	})
}
