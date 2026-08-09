package ackwatch

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Default cadences for the standing runner.
const (
	// DefaultInterval is how often the runner samples. Coarse on purpose: the
	// condition is a RATE over hundreds of fires, so it moves by fractions of a
	// point per tick and cannot be missed by sampling half-hourly.
	DefaultInterval = 30 * time.Minute
	// DefaultRenotifyAfter is how long an UNCHANGED set of findings stays quiet
	// before it is raised again.
	DefaultRenotifyAfter = 6 * time.Hour
	// DefaultEscalateAfter is how long a single finding may persist, unbroken,
	// before the notice ALSO goes to EscalateTo.
	DefaultEscalateAfter = 24 * time.Hour
	// DefaultBlackoutRenotifyAfter is the renotify window that applies while a
	// FLEET BLACKOUT is outstanding, in place of DefaultRenotifyAfter.
	//
	// 6 hours is a defensible quiet period for "one schedule is lagging its
	// peers": the fleet has been told, and telling it again every half hour
	// achieves nothing but training the recipient to filter the sender. It is the
	// wrong number for "nothing in the fleet has completed a fire", and mg-e2a4
	// is the measurement of how wrong: the 2026-08-09 outage ran 4.5 hours,
	// began and ended inside ONE renotify shadow, and would have produced a
	// single notice for an arbitrarily severe event — with a second identical
	// outage 30 minutes later suppressed entirely.
	//
	// So it is set to the sampling interval: while the fleet is dead, every
	// sample mails. Repetition is not noise here, it IS the signal — the notice
	// leaves pogod for an out-of-process reader with no acknowledgement path
	// back, so "was the last one seen?" is a question nothing on this side can
	// answer, and the honest answer to an unanswerable question is to say it
	// again while it is still true. The all-clear is the notice stopping.
	DefaultBlackoutRenotifyAfter = 30 * time.Minute
)

const (
	mailFrom = "ack-watch"
	// DefaultNotifyTo is the mayor. The remedy for a silently-failing crew agent
	// is `pogo nudge <agent> --immediate` or a doctor restart, and the mayor is
	// the agent that does both — this is coordination work, not a decision only
	// a person can make.
	DefaultNotifyTo = "mayor"
	// DefaultEscalateTo receives a finding the fleet has demonstrably failed to
	// clear. See Options.EscalateAfter.
	//
	// The escalation matters more here than in a typical detector, because the
	// mayor is itself a crew agent and can have the very defect this watcher
	// reports — mg-d385 was the mayor emitting malformed tool calls hours before
	// pm-pogo did the same thing, on the same night. An alert routed only to an
	// agent that may be the patient is an alert nobody reads, which reproduces
	// this ticket's bug one level up.
	DefaultEscalateTo = "human"
)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. As in internal/ghteardown, it is the ONLY side-effect channel this
// package has: there is deliberately no seam through which the watcher could
// nudge, restart, or otherwise act on an agent.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// SourceFunc yields one reading of scheduler state. Production binds a closure
// over the live *scheduler.Scheduler and the events log; tests substitute a
// fixture.
//
// It returns an error rather than an empty Snapshot when state cannot be read,
// because "no schedules" and "could not look" must never collapse into the same
// quiet result.
type SourceFunc func(now time.Time) (Snapshot, error)

// Options carries the runner's dependencies.
type Options struct {
	// Source produces the snapshot. Required.
	Source SourceFunc
	// Mail delivers the notice. Required — a detector that cannot report is
	// exactly the thing this package was written to stop existing.
	Mail MailFunc
	// Emit writes ack_watch_* events. Defaults to events.Emit.
	Emit Emitter
	// Params tunes the detector. Zero fields take DefaultParams values.
	Params Params
	// Interval is the coarse sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// RenotifyAfter is how long an unchanged finding set stays quiet. Zero means
	// DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// BlackoutRenotifyAfter replaces RenotifyAfter while a FLEET BLACKOUT is
	// outstanding. Zero means DefaultBlackoutRenotifyAfter; see there for why the
	// two windows cannot be one number.
	BlackoutRenotifyAfter time.Duration
	// NotifyTo is the mailbox findings go to. Empty means DefaultNotifyTo.
	NotifyTo string
	// EscalateAfter is how long one finding may persist before the notice ALSO
	// goes to EscalateTo. Zero means DefaultEscalateAfter; NEGATIVE disables the
	// AGE-based escalation only — a FLEET BLACKOUT still escalates on its first
	// sample, because that one is not a matter of patience (see
	// Watcher.sample, and deafwatch.escalateNow for the same rule). Zero and
	// negative must differ, or a config that omits the key would silently turn
	// escalation off.
	EscalateAfter time.Duration
	// EscalateTo receives escalated notices. Empty means DefaultEscalateTo.
	EscalateTo string
	// StartedAt is when the hosting process started. It acts as a disruption
	// floor: for SettleAfter following a restart the report is suppressed, on
	// the same footing as a system_wake. A restart is when agents re-register
	// their mail-checks and zero their counters, and mg-42ac made that nightly.
	// Zero means "no restart known", which suppresses nothing.
	StartedAt time.Time
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing completion-deficit detector: it rides pogod's
// heartbeat, samples the scheduler's completion counters on a coarse interval,
// and mails when a schedule is completing far fewer of its fires than its
// directly comparable peers.
//
// It rides the heartbeat rather than a launchd timer for the same reason
// internal/driftwatch and internal/ghteardown do: the nondemand-spawn wedge on
// this box (mg-50e0) leaves launchd timers silently never firing — precisely
// the inert-but-healthy-looking failure this detector exists to catch.
//
// # Notification policy
//
// Findings are fingerprinted by IDENTITY, not by rate (see Report.Fingerprint).
// A changed set mails immediately; an unchanged set stays quiet until
// RenotifyAfter elapses. A completion rate drifts continuously, so
// fingerprinting the numbers would mail every single interval and get the
// sender filtered — which is how a detector becomes an alert nobody consumes.
//
// # Notification policy, second exception: the blackout
//
// A FLEET BLACKOUT is routed differently on both axes, and neither difference is
// a tuning preference:
//
//   - It goes to EscalateTo on its FIRST sample, structurally, never waiting on
//     EscalateAfter. NotifyTo is an agent, and the subject of the finding is
//     that no agent is completing work — so the ordinary recipient is inside the
//     failure being reported. On 2026-08-09 that produced exactly one mail about
//     a dead fleet, addressed to a coordinator whose own mail-check carried the
//     same 27 unacked fires (mg-e2a4).
//   - It renotifies on BlackoutRenotifyAfter rather than RenotifyAfter, which is
//     shorter than any outage worth knowing about rather than longer.
//
// # Report-only
//
// The watcher mails and emits. It never nudges, restarts, or unregisters
// anything. The one instance this was built from needed `pogo nudge --immediate`
// followed by human judgement about a doctor restart; automating that off a
// statistical signal would be acting on a suspicion with a blunt instrument.
type Watcher struct {
	enabled          bool
	interval         time.Duration
	renotifyAfter    time.Duration
	blackoutRenotify time.Duration
	escalateAfter    time.Duration
	notifyTo         string
	escalateTo       string
	startedAt        time.Time
	params           Params
	source           SourceFunc
	mail             MailFunc
	emit             Emitter

	mu         sync.Mutex
	lastRun    time.Time
	ran        bool
	lastPrint  string
	lastMailed time.Time
	firstSeen  map[string]time.Time
}

// New builds a Watcher, applying defaults for zero-valued options.
func New(opts Options) *Watcher {
	emit := opts.Emit
	if emit == nil {
		emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}
	interval := opts.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	renotify := opts.RenotifyAfter
	if renotify <= 0 {
		renotify = DefaultRenotifyAfter
	}
	blackoutRenotify := opts.BlackoutRenotifyAfter
	if blackoutRenotify <= 0 {
		blackoutRenotify = DefaultBlackoutRenotifyAfter
	}
	escalate := opts.EscalateAfter
	if escalate == 0 {
		escalate = DefaultEscalateAfter
	}
	notifyTo := opts.NotifyTo
	if notifyTo == "" {
		notifyTo = DefaultNotifyTo
	}
	escalateTo := opts.EscalateTo
	if escalateTo == "" {
		escalateTo = DefaultEscalateTo
	}
	return &Watcher{
		enabled: opts.Enabled, interval: interval, renotifyAfter: renotify,
		blackoutRenotify: blackoutRenotify,
		escalateAfter:    escalate, notifyTo: notifyTo, escalateTo: escalateTo,
		startedAt: opts.StartedAt, params: opts.Params.withDefaults(),
		source: opts.Source, mail: opts.Mail, emit: emit,
	}
}

// Check runs one sample subject to the coarse throttle. It is the integration
// point for the heartbeat OnTick callback, and a no-op on all but the first
// tick of each interval.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.enabled || w.source == nil || w.mail == nil {
		return
	}
	if !w.due(now) {
		return
	}
	w.sample(now)
}

// due reports whether the interval has elapsed, recording now BEFORE the sample
// runs so a slow or failing sample still consumes its slot.
func (w *Watcher) due(now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ran && now.Sub(w.lastRun) < w.interval {
		return false
	}
	w.lastRun = now
	w.ran = true
	return true
}

func (w *Watcher) sample(now time.Time) {
	snap, err := w.source(now)
	if err != nil {
		// Scheduler state that cannot be read is a real failure, not a clean
		// scan. Emit it so a blind detector is visible in the event log rather
		// than indistinguishable from a quiet one.
		w.emit(events.Event{
			EventType: "ack_watch_error",
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	snap = w.applyStartDisruption(snap)
	rep := Detect(snap, w.params)

	if rep.Suppressed {
		// Deliberate silence. Recorded, so a reader of the event log can tell
		// "suppressed after a wake" from "ran and found nothing" — collapsing
		// those two is the shape of bug this package exists to end.
		w.emit(events.Event{
			EventType: "ack_watch_suppressed",
			Agent:     "pogod",
			Details: map[string]any{
				"reason":  rep.SuppressReason,
				"scanned": rep.Scanned,
			},
		})
		return
	}

	if !rep.Actionable() {
		// Clear the fingerprint so a deficit that resolves and later recurs is
		// news again rather than being suppressed as "unchanged". The escalation
		// clocks go with it: the fleet DID clear the first one.
		w.mu.Lock()
		w.lastPrint = ""
		w.firstSeen = nil
		w.mu.Unlock()

		// A control that correctly declined to fire must SAY so, once, with the
		// circumstances — otherwise a silent correct outcome and a control that
		// is not running are the same observation, and the next editor removes
		// this package as dead weight (mg-ddf7).
		//
		// That is not hypothetical here. On the 2026-07-29 storm night the right
		// answer was silence and the detector would have produced it; the only
		// evidence the design worked is that two agents happened to notice the
		// burst and reason about it, which is not a property of the system. The
		// counts are what make the line worth writing: "eligible 3 of 41" and
		// "eligible 41 of 41" are both no-findings, and only one of them is a
		// clean bill of health.
		//
		// It is emitted on EVERY clear sample rather than only on transitions.
		// Interval is coarse (30m), the event is one line, and a transition-only
		// emit would go quiet during exactly the long calm stretch in which a
		// reader most needs to know the control is still alive — reproducing the
		// bug one level up.
		clear := map[string]any{
			"scanned":               rep.Scanned,
			"eligible":              rep.Eligible,
			"skipped_fresh":         rep.SkippedFresh,
			"skipped_few_fires":     rep.SkippedFewFires,
			"skipped_not_recurring": rep.SkippedNotRecurring,
			"skipped_no_peers":      rep.SkippedNoPeers,
		}
		// Which ARMS ran, not just how many samples were judged. A clear from the
		// peer arm alone is the reading a dead fleet produces, so a clear that does
		// not say whether the absolute arm looked is the same ambiguity one level
		// in (mg-e2a4).
		clear["blackout_judged"] = rep.BlackoutBlind == ""
		if rep.BlackoutBlind != "" {
			clear["blackout_blind"] = rep.BlackoutBlind
		}
		w.emit(events.Event{EventType: "ack_watch_clear", Agent: "pogod", Details: clear})
		return
	}

	oldest := w.trackAges(rep, now)
	blackout := rep.Blackout != nil

	// A blackout uses its own renotify window, and it must be consulted BEFORE
	// the throttle rather than after: reusing the 6-hour window here is the
	// defect, not an implementation detail of it.
	if !w.shouldMail(rep.Fingerprint(), now, blackout) {
		return
	}

	body := rep.Render() +
		"\nThis is REPORT-ONLY — pogod did NOT nudge, restart, or unregister anything.\n\n" +
		"Re-check on demand with:\n  pogo check-acks\n" +
		"Compare rows yourself with:\n  pogo schedule list\n"

	aged := w.escalateAfter > 0 && !oldest.IsZero() && now.Sub(oldest) >= w.escalateAfter
	escalate := aged || blackout
	recipients := []string{w.notifyTo}
	if escalate && w.escalateTo != w.notifyTo {
		recipients = append(recipients, w.escalateTo)
		body = w.escalationPreamble(rep, oldest, now, aged, blackout) + body
	}

	subject := "ack-watch: " + rep.MailSubject()
	details := map[string]any{
		"deficit_count": len(rep.Deficits),
		"fleet_count":   len(rep.Fleet),
		"scanned":       rep.Scanned,
		"eligible":      rep.Eligible,
		"notified":      strings.Join(recipients, ","),
		"escalated":     escalate,
		"escalate_to":   w.escalateTo,
		"blackout":      blackout,
		"schedules":     findingIDs(rep),
	}
	if rep.BlackoutBlind != "" {
		details["blackout_blind"] = rep.BlackoutBlind
	}
	if blackout {
		bo := rep.Blackout
		details["blackout_delivered"] = bo.Delivered
		details["blackout_completed"] = bo.Completed
		details["blackout_rate"] = bo.Rate
		details["blackout_window"] = bo.Window
		details["blackout_agents"] = bo.StalledAgents
		// The remedy is an artifact of the same kind as the defect: an alarm
		// about the fleet being dead, addressed to a member of the fleet. Both
		// recipients are checked against the stalled population and the answer is
		// recorded either way — a bare `notified` field is what let 16:12:59 read
		// as a delivered alarm.
		details["notify_to_stalled"] = inPopulation(w.notifyTo, bo.StalledAgents)
		details["escalate_to_stalled"] = inPopulation(w.escalateTo, bo.StalledAgents)
	}
	failures := 0
	for _, to := range recipients {
		if err := w.mail(to, mailFrom, subject, body); err != nil {
			// The deficit was detected but could not be reported. Record it: a
			// notice that reaches nobody is this ticket's bug, one level up.
			details["mail_error_"+to] = err.Error()
			failures++
		}
	}
	w.emit(events.Event{EventType: "ack_watch_fired", Agent: "pogod", Details: details})

	// A blackout notice that reached nobody gets its own event type, because
	// `ack_watch_fired` with a mail_error_* key buried in details is not
	// greppable as "the fleet was dead and the alarm did not leave the box".
	if blackout && failures == len(recipients) {
		w.emit(events.Event{
			EventType: EventBlackoutUnreported,
			Agent:     "pogod",
			Details: map[string]any{
				"recipients": strings.Join(recipients, ","),
				"delivered":  rep.Blackout.Delivered,
				"completed":  rep.Blackout.Completed,
				"window":     rep.Blackout.Window,
			},
		})
	}
}

// EventBlackoutUnreported is emitted when every recipient of a FLEET BLACKOUT
// notice refused it. It is the one state worse than the bug this arm fixes: the
// fleet is dead, pogod noticed, and the notice did not leave the machine.
const EventBlackoutUnreported = "ack_watch_blackout_unreported"

// escalationPreamble explains why EscalateTo is on this mail. Two reasons, and
// they are not interchangeable: age means the fleet had time and did not fix it,
// a blackout means the fleet cannot be the one to fix it.
func (w *Watcher) escalationPreamble(rep Report, oldest, now time.Time, aged, blackout bool) string {
	if blackout {
		bo := rep.Blackout
		var b strings.Builder
		fmt.Fprintf(&b,
			"ESCALATED IMMEDIATELY, NOT ON A TIMER: the fleet is not completing work, and\n"+
				"%s — the mailbox this alert normally goes to — is an agent inside that fleet.\n"+
				"A detector inside the job cannot report the job not running, so this notice is\n"+
				"also addressed to %s, which is read out of process (mg-e2a4).\n\n",
			w.notifyTo, w.escalateTo)
		if inPopulation(w.notifyTo, bo.StalledAgents) {
			fmt.Fprintf(&b,
				"CONFIRMED: %s is one of the stalled agents listed below. Do not wait for it to\n"+
					"act on this.\n\n", w.notifyTo)
		}
		if inPopulation(w.escalateTo, bo.StalledAgents) {
			fmt.Fprintf(&b,
				"WARNING: %s is ALSO one of the stalled agents below, so this escalation has no\n"+
					"recipient outside the outage. Set `[agents] escalation_box` to a mailbox that\n"+
					"no agent owns — see docs/CONFIGURATION.md.\n\n", w.escalateTo)
		}
		if aged && !oldest.IsZero() {
			fmt.Fprintf(&b, "A finding here has also been outstanding for %s.\n\n",
				now.Sub(oldest).Round(time.Hour))
		}
		return b.String()
	}
	if aged && !oldest.IsZero() {
		age := now.Sub(oldest).Round(time.Hour)
		return fmt.Sprintf(
			"ESCALATED: a finding below has been reported to %s for %s without clearing.\n"+
				"A crew agent completing a fraction of its fires is the fleet's to fix, but a\n"+
				"fleet that has not fixed one in %s is not — and the coordinator is itself a\n"+
				"crew agent that can have this exact defect (mg-d385).\n\n",
			w.notifyTo, age, age)
	}
	return ""
}

// inPopulation reports whether a mailbox name is one of the stalled agents. The
// comparison is on the agent name because that is what a mail-check schedule is
// registered under, and it is what `notified` was silently indifferent to.
func inPopulation(box string, agents []string) bool {
	for _, a := range agents {
		if a == box {
			return true
		}
	}
	return false
}

// applyStartDisruption raises the snapshot's disruption time to the hosting
// process's start when that is more recent. A restart and a system_wake are the
// same class of known-benign event — both leave the counters describing a
// regime that no longer exists — so they share one suppression rather than
// getting a mechanism each.
func (w *Watcher) applyStartDisruption(snap Snapshot) Snapshot {
	if w.startedAt.IsZero() {
		return snap
	}
	if snap.LastDisruption.IsZero() || w.startedAt.After(snap.LastDisruption) {
		snap.LastDisruption = w.startedAt
		snap.DisruptionReason = "pogod restart"
	}
	return snap
}

// trackAges records when each currently-actionable finding was FIRST seen and
// forgets the ones that have cleared, returning the earliest first-seen time
// still outstanding. Per finding rather than per finding-set: a new deficit
// appearing must not reset the clock on an old one, or the stalest finding —
// the one escalation exists for — would be the one that never ages.
func (w *Watcher) trackAges(rep Report, now time.Time) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	seen := make(map[string]time.Time, len(rep.Deficits)+len(rep.Fleet))
	var oldest time.Time
	keys := make([]string, 0, len(rep.Deficits)+len(rep.Fleet))
	for _, f := range rep.Deficits {
		keys = append(keys, f.Key())
	}
	for _, f := range rep.Fleet {
		keys = append(keys, f.Key())
	}
	for _, key := range keys {
		at, ok := w.firstSeen[key]
		if !ok {
			at = now
		}
		seen[key] = at
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	w.firstSeen = seen
	return oldest
}

// shouldMail applies the change-or-periodic policy described on Watcher. The
// periodic half uses the BLACKOUT window while a blackout is outstanding: the
// two conditions have different half-lives, and collapsing them is what let a
// 4.5-hour dead fleet fit inside one 6-hour silence (mg-e2a4).
func (w *Watcher) shouldMail(print string, now time.Time, blackout bool) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	renotify := w.renotifyAfter
	if blackout && w.blackoutRenotify < renotify {
		renotify = w.blackoutRenotify
	}
	if print != w.lastPrint || now.Sub(w.lastMailed) >= renotify {
		w.lastPrint = print
		w.lastMailed = now
		return true
	}
	return false
}

// findingIDs lists the schedule ids in a report, for the emitted event's
// details — so `pogo events list --type=ack_watch_fired` names the schedules
// without anyone having to open the mail.
func findingIDs(rep Report) []string {
	out := make([]string, 0, len(rep.Deficits)+len(rep.Fleet))
	for _, f := range rep.Deficits {
		out = append(out, f.ID)
	}
	for _, f := range rep.Fleet {
		out = append(out, "cohort:"+f.Kind+"/"+f.Cadence)
	}
	if rep.Blackout != nil {
		out = append(out, "fleet-blackout")
	}
	return out
}
