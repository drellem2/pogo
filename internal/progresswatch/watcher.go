package progresswatch

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Event types this package writes to the shared log.
const (
	// EventStalled records a mailed finding: the conjunction held for longer
	// than the hold-down.
	EventStalled = "fleet_progress_stalled"
	// EventPending records the conjunction holding but not yet long enough to
	// announce. Emitted once per entry so the log distinguishes "we saw it and
	// waited" from "we never saw it" — indistinguishable from the mail alone.
	EventPending = "fleet_progress_pending"
	// EventCleared records the episode closing, with what cleared it.
	EventCleared = "fleet_progress_cleared"
	// EventError records a sample that could not be taken, or one taken with a
	// member of the conjunction unmeasurable. A detector that cannot see has
	// NOT found a healthy fleet, and the two must be separable afterwards.
	EventError = "fleet_progress_error"
)

// EpisodeKind is this detector's value for details.kind on the generic
// incident_episode_cleared event (the mg-55b2 contract), which is what lets the
// pogo-reminders notifier coalesce a close into one notification rather than a
// swarm (mg-e0f6). Minting a new KIND is expected; minting a new EVENT TYPE is
// not.
const EpisodeKind = "fleet_progress"

// IncidentEpisodeClearedEvent is the generic episode-close event type, spelled
// here rather than imported from internal/claude so this package does not take a
// dependency on the harness layer for one string constant. It must stay
// byte-identical to claude.IncidentEpisodeClearedEvent;
// TestEpisodeKindMatchesContract pins that.
const IncidentEpisodeClearedEvent = "incident_episode_cleared"

// Runner cadences.
const (
	// DefaultInterval is the gap between samples. The condition is a state, not
	// a rate, so nothing is being averaged; five minutes is frequent enough
	// that the hold-down rather than the sampling interval decides latency, and
	// each sample costs one process-table pair and one walk per worktree.
	DefaultInterval = 5 * time.Minute
	// DefaultHoldDown is how long the conjunction must hold CONTINUOUSLY before
	// it is mailed. The four thresholds already carry their own patience — 30
	// minutes without a completion, 10 without a file — so this is not a second
	// helping of it. It exists because CPU is an instantaneous reading and
	// seven workers can all be between things for one sample: 10 minutes is two
	// samples at the default interval, which is the smallest hold-down that
	// cannot be a single unlucky instant.
	DefaultHoldDown = 10 * time.Minute
	// DefaultRenotifyAfter is how long an OPEN, unchanged episode stays quiet.
	// The condition is fleet-wide and one mail per interval is how a detector
	// gets filtered — mg-70f3 counted 49 pages and 44 all-clears in one log for
	// one fault, which is the failure this number exists to avoid.
	DefaultRenotifyAfter = 2 * time.Hour
	// DefaultEscalateAfter is how long the condition may hold, unbroken, before
	// the notice ALSO goes to EscalateTo. Two hours of a fleet landing nothing
	// is past what a coordinator can be assumed to be handling quietly.
	DefaultEscalateAfter = 2 * time.Hour
	// DefaultBlindFor is how long the detector may be UNABLE TO MEASURE,
	// continuously, before it mails about ITSELF.
	//
	// This is the remedy being held to the standard of the defect. A detector
	// that goes blind and only whispers into events.log is exactly the shape
	// this package exists to remove: a thing that answers the question it was
	// built for, truthfully, while the question that matters goes unanswered
	// and nothing says so. Thirty minutes is one completion window — long
	// enough that a transient unreadable worktree during a reap is not news,
	// short enough that a night spent blind is not silent.
	DefaultBlindFor = 30 * time.Minute
	// DefaultNotifyTo is the mayor: deciding what to do about a blocked fleet
	// is coordination work.
	DefaultNotifyTo = "mayor"
	// DefaultEscalateTo receives what the fleet has not cleared.
	DefaultEscalateTo = "human"

	mailFrom = "progress-watch"
)

// SourceFunc yields one snapshot. pogod binds a closure over the live registry,
// the refinery and the host; tests substitute a fixture.
//
// It returns an error rather than an empty Snapshot when the fleet cannot be
// read at all, because "nobody is working" and "I could not look" must never
// collapse into the same quiet result.
type SourceFunc func(now time.Time) (Snapshot, error)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. It is the ONLY side-effect channel this package has: there is
// deliberately no seam through which the watcher could restart or nudge the
// workers it names. See the package comment.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	// Source produces the snapshot. Required.
	Source SourceFunc
	// Mail delivers the announcement. Required — a detector that cannot report
	// is the thing this package exists to stop existing.
	Mail MailFunc
	// Emit writes fleet_progress_* events. Defaults to events.Emit.
	Emit Emitter
	// Thresholds tunes the conjunction. Zero value uses the shipped judgement.
	Thresholds Thresholds
	// Interval is the sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// HoldDown is how long the conjunction must hold before it is mailed. Zero
	// means DefaultHoldDown; NEGATIVE disables it, which only tests should do.
	HoldDown time.Duration
	// RenotifyAfter is how long an open episode stays quiet. Zero means
	// DefaultRenotifyAfter. It also paces the blind notice.
	RenotifyAfter time.Duration
	// BlindFor is how long the detector may be unable to measure before it
	// mails about itself. Zero means DefaultBlindFor; NEGATIVE disables the
	// self-report, which nothing but a test should do.
	BlindFor time.Duration
	// EscalateAfter is how long the condition may hold before the notice also
	// goes to EscalateTo. Zero means DefaultEscalateAfter; NEGATIVE disables
	// escalation. Zero and negative must differ, or a config that omits the key
	// would silently turn escalation off.
	EscalateAfter time.Duration
	// NotifyTo is the mailbox findings go to. Empty means DefaultNotifyTo.
	NotifyTo string
	// EscalateTo receives escalated notices. Empty means DefaultEscalateTo.
	EscalateTo string
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing instrument for "the fleet is alive and landing
// nothing". It rides pogod's heartbeat rather than a launchd timer, for the
// reason internal/deafwatch, internal/absentwatch and internal/ghteardown do:
// the nondemand-spawn wedge on this box (mg-50e0) leaves launchd timers
// silently never firing, which for a detector of silence would be especially
// apt.
//
// # Episode semantics
//
// While the conjunction holds an episode is open: one mail when it opens, a
// repeat only after RenotifyAfter, and one clear when it breaks — carrying what
// broke it. A blind sample (a member of the conjunction unmeasurable) neither
// opens nor closes an episode: it emits EventError and leaves the hold-down
// clock alone, because a detector that went blind mid-incident must not report
// the incident as over.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	holdDown      time.Duration
	renotifyAfter time.Duration
	escalateAfter time.Duration
	blindFor      time.Duration
	notifyTo      string
	escalateTo    string
	thresholds    Thresholds
	source        SourceFunc
	mail          MailFunc
	emit          Emitter

	mu      sync.Mutex
	lastRun time.Time
	ran     bool
	// since is when the conjunction was FIRST observed in the current unbroken
	// run. Cleared when it breaks, so a flap restarts the hold-down rather than
	// accumulating toward it.
	since time.Time
	// pendingLogged records that EventPending has been emitted for this run, so
	// entering the hold-down is one line rather than one per sample.
	pendingLogged bool
	// open is the announced episode. openedAt is when the condition started,
	// not when it was announced — the mail states both.
	open         bool
	openedAt     time.Time
	lastMailed   time.Time
	toldEscalate bool
	// blindSince is when the CURRENT unbroken run of unmeasurable samples
	// started, and blindMailed when it was last reported. Both are cleared by a
	// sample that measured, so a detector that recovers stops complaining.
	blindSince  time.Time
	blindMailed time.Time
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
	hold := opts.HoldDown
	if hold == 0 {
		hold = DefaultHoldDown
	}
	if hold < 0 {
		hold = 0
	}
	renotify := opts.RenotifyAfter
	if renotify <= 0 {
		renotify = DefaultRenotifyAfter
	}
	escalate := opts.EscalateAfter
	if escalate == 0 {
		escalate = DefaultEscalateAfter
	}
	blind := opts.BlindFor
	if blind == 0 {
		blind = DefaultBlindFor
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
		enabled: opts.Enabled, interval: interval, holdDown: hold,
		renotifyAfter: renotify, escalateAfter: escalate, blindFor: blind,
		notifyTo: notifyTo, escalateTo: escalateTo,
		thresholds: opts.Thresholds,
		source:     opts.Source, mail: opts.Mail, emit: emit,
	}
}

// Check runs one sample subject to the throttle. It is the integration point
// for the heartbeat OnTick callback, and a no-op on all but the first tick of
// each interval.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.enabled || w.source == nil || w.mail == nil {
		return
	}
	if now.IsZero() {
		now = time.Now()
	}
	if !w.due(now) {
		return
	}
	w.sample(now)
}

// Sample takes one reading ON DEMAND: it bypasses the interval throttle,
// touches none of the episode state, and mails nothing. It is what the
// on-demand surface reads (`pogo check-progress` through pogod's
// /health/progress), and it shares the runner's source and thresholds so the
// answer a coordinator gets by asking is the same one the runner would act on.
//
// That sharing is the point rather than a convenience. mg-516e was found by a
// human running three checks by hand and noticing they disagreed; an on-demand
// surface that re-derived the judgement would be a second opinion to reconcile
// at exactly the moment nobody has time to.
func (w *Watcher) Sample(now time.Time) (Reading, error) {
	if w == nil || w.source == nil {
		return Reading{}, errors.New("progresswatch: no source configured")
	}
	if now.IsZero() {
		now = time.Now()
	}
	snap, err := w.source(now)
	if err != nil {
		return Reading{}, err
	}
	return Evaluate(snap, w.thresholds), nil
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
		// A fleet that could not be read is a real failure, not a healthy one.
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details: map[string]any{
				"error": err.Error(),
				"phase": "sample",
				"why":   "fleet-progress source failed; nothing was measured this tick",
			},
		})
		return
	}
	r := Evaluate(snap, w.thresholds)
	w.apply(now, r)
}

// apply is the state machine, split from sample so tests drive readings
// directly without building a source.
func (w *Watcher) apply(now time.Time, r Reading) {
	if len(r.Blind) > 0 {
		// Blind: report it, and leave the hold-down clock exactly where it was.
		// Clearing it would let a detector that went blind mid-incident restart
		// the incident's clock from zero; closing the episode would report the
		// incident as over on the strength of not having looked.
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details: map[string]any{
				"blind":       r.Blind,
				"measurement": r.Measurements(),
				"phase":       "evaluate",
				"why":         "a member of the conjunction could not be measured; no finding is possible from this sample",
			},
		})
		w.reportBlind(now, r)
		return
	}
	w.sighted()

	if !r.Stalled {
		w.clear(now, r)
		return
	}

	w.mu.Lock()
	if w.since.IsZero() {
		w.since = now
	}
	held := now.Sub(w.since)
	firstPending := !w.pendingLogged
	w.pendingLogged = true
	open := w.open
	openedAt := w.openedAt
	lastMailed := w.lastMailed
	toldEscalate := w.toldEscalate
	w.mu.Unlock()

	if held < w.holdDown {
		if firstPending {
			w.emit(events.Event{
				EventType: EventPending,
				Agent:     "pogod",
				Details: map[string]any{
					"measurement": r.Measurements(),
					"hold_down":   w.holdDown.String(),
					"why":         "the conjunction holds; waiting out the hold-down before announcing",
				},
			})
		}
		return
	}

	escalate := w.escalateAfter > 0 && held >= w.escalateAfter
	// Crossing the escalation threshold ESCAPES the renotify floor. Without
	// this escape the escalation is silently gated behind RenotifyAfter, so a
	// deployment that sets EscalateAfter shorter than RenotifyAfter — the whole
	// point of having two knobs — would delay the human's notice to the
	// coordinator's cadence and nothing would say so. synthwatch's paging floor
	// carries the same unconditional escape, for the same reason.
	newlyEscalating := escalate && !toldEscalate
	if open && now.Sub(lastMailed) < w.renotifyAfter && !newlyEscalating {
		return
	}
	if !open {
		openedAt = w.since
	}

	to := []string{w.notifyTo}
	if escalate {
		to = append(to, w.escalateTo)
	}
	body := w.body(r, held, openedAt)
	for _, box := range to {
		if err := w.mail(box, mailFrom, r.Subject(), body); err != nil {
			w.emit(events.Event{
				EventType: EventError,
				Agent:     "pogod",
				Details: map[string]any{
					"error": err.Error(),
					"to":    box,
					"phase": "mail",
					"why":   "the finding was measured and could not be delivered",
				},
			})
		}
	}

	w.mu.Lock()
	w.open = true
	w.openedAt = openedAt
	w.lastMailed = now
	if escalate {
		w.toldEscalate = true
	}
	w.mu.Unlock()

	w.emit(events.Event{
		EventType: EventStalled,
		Agent:     "pogod",
		Details: map[string]any{
			"measurement":     r.Measurements(),
			"blocked_workers": r.Blocked,
			"blocked_names":   r.BlockedNames,
			"worker_cores":    r.WorkerCores,
			"host_cores":      r.HostCores,
			"since_progress":  r.SinceProgress.String(),
			"held_for":        held.String(),
			"escalated":       escalate || toldEscalate,
			"notified":        to,
		},
	})
}

// reportBlind mails when the detector has been unable to measure for longer
// than BlindFor, and paces the repeat at RenotifyAfter.
//
// It exists because this package's own subject matter applies to itself. An
// instrument whose failure is visible only as a line in events.log is one that
// can stop measuring for a night while every surface that consumes it stays
// quiet — which is the description of the bug, not of the fix. So the detector
// reports ITSELF through the same channel it reports the fleet through.
//
// It never escalates on age. A blind detector is a defect in the tooling and
// the coordinator's to route; the human box is for the fleet not producing,
// which is the thing that costs the night.
func (w *Watcher) reportBlind(now time.Time, r Reading) {
	if w.blindFor < 0 {
		return
	}
	w.mu.Lock()
	if w.blindSince.IsZero() {
		w.blindSince = now
	}
	since := w.blindSince
	last := w.blindMailed
	w.mu.Unlock()

	if now.Sub(since) < w.blindFor {
		return
	}
	if !last.IsZero() && now.Sub(last) < w.renotifyAfter {
		return
	}

	subject := fmt.Sprintf("fleet-progress detector has measured NOTHING for %s", round(now.Sub(since)))
	var b strings.Builder
	b.WriteString("This is the detector reporting ITSELF, not the fleet.\n\n")
	fmt.Fprintf(&b, "  blind since  %s (%s)\n", since.UTC().Format(time.RFC3339), round(now.Sub(since)))
	fmt.Fprintf(&b, "  last reading %s\n", r.Measurements())
	b.WriteString("  could not measure\n")
	for _, x := range r.Blind {
		b.WriteString("    - " + x + "\n")
	}
	b.WriteString("\nWhile this holds, NO finding is possible: a fleet that stops producing\n" +
		"will not be reported, and the absence of an alarm means nothing.\n\n")
	b.WriteString(detectorNote)

	if err := w.mail(w.notifyTo, mailFrom, subject, b.String()); err != nil {
		w.emit(events.Event{
			EventType: EventError,
			Agent:     "pogod",
			Details: map[string]any{
				"error": err.Error(), "to": w.notifyTo, "phase": "blind_mail",
				"why": "the detector could not report its own blindness",
			},
		})
	}
	w.mu.Lock()
	w.blindMailed = now
	w.mu.Unlock()
}

// sighted records a sample that measured, ending any blind run.
func (w *Watcher) sighted() {
	w.mu.Lock()
	w.blindSince = time.Time{}
	w.blindMailed = time.Time{}
	w.mu.Unlock()
}

// clear ends the run, and the episode with it when one was announced.
func (w *Watcher) clear(now time.Time, r Reading) {
	w.mu.Lock()
	open := w.open
	openedAt := w.openedAt
	toldEscalate := w.toldEscalate
	w.since = time.Time{}
	w.pendingLogged = false
	w.open = false
	w.openedAt = time.Time{}
	w.lastMailed = time.Time{}
	w.toldEscalate = false
	w.mu.Unlock()

	if !open {
		return
	}
	window := now.Sub(openedAt)
	// Everyone who was alarmed is told, or somebody holds an open incident
	// forever.
	to := []string{w.notifyTo}
	if toldEscalate {
		to = append(to, w.escalateTo)
	}
	subject := fmt.Sprintf("cleared: fleet is landing work again after %s", round(window))
	body := "The fleet-progress conjunction no longer holds.\n\n" +
		"  window   " + openedAt.UTC().Format(time.RFC3339) + " .. " + now.UTC().Format(time.RFC3339) +
		" (" + round(window) + ")\n" +
		"  now      " + r.Measurements() + "\n" +
		whyCleared(r) +
		"\n" + detectorNote
	for _, box := range to {
		if err := w.mail(box, mailFrom, subject, body); err != nil {
			w.emit(events.Event{
				EventType: EventError,
				Agent:     "pogod",
				Details: map[string]any{
					"error": err.Error(), "to": box, "phase": "clear_mail",
					"why": "the clear was measured and could not be delivered",
				},
			})
		}
	}
	w.emit(events.Event{
		EventType: EventCleared,
		Agent:     "pogod",
		Details: map[string]any{
			"measurement": r.Measurements(),
			"window":      window.String(),
			"cleared_by":  r.Held,
		},
	})
	// The generic close, for the notifier's coalescing (mg-55b2 contract).
	w.emit(events.Event{
		EventType: IncidentEpisodeClearedEvent,
		Agent:     "pogod",
		Details: map[string]any{
			"kind":     EpisodeKind,
			"window":   window.String(),
			"opened":   openedAt.UTC().Format(time.RFC3339),
			"cleared":  now.UTC().Format(time.RFC3339),
			"notified": to,
		},
	})
}

func whyCleared(r Reading) string {
	if len(r.Held) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("  cleared by\n")
	for _, h := range r.Held {
		b.WriteString("    - " + h + "\n")
	}
	return b.String()
}

// detectorNote travels with every mail this watcher sends. A reader who has
// never met this detector needs to know what it did and did not measure before
// acting on it, and a footnote in a source file is not where they will look.
const detectorNote = `This detector measures a CONJUNCTION and reports the numbers, not a verdict:
workers alive on their PTY, AND writing no file, AND their process subtrees
computing nothing, AND nothing landing in the merge queue or the work items.
Any one of those alone is ordinary. Together they have one ordinary
explanation — everyone is waiting on the same remote — so the first thing to
check is whether the model API or the git remote is reachable from this host.

It is REPORT-ONLY: it has restarted nothing and stopped nothing. A fleet
waiting on a remote is not fixed by killing the agents that are waiting.
`

func (w *Watcher) body(r Reading, held time.Duration, openedAt time.Time) string {
	var b strings.Builder
	b.WriteString("Every worker is awake. None of them is producing anything.\n\n")
	fmt.Fprintf(&b, "  measured  %s\n", r.Measurements())
	fmt.Fprintf(&b, "  holding   %s (since %s)\n", round(held), openedAt.UTC().Format(time.RFC3339))
	if len(r.BlockedNames) > 0 {
		fmt.Fprintf(&b, "  workers   %s\n", strings.Join(r.BlockedNames, ", "))
	}
	fmt.Fprintf(&b, "  thresholds  alive within %s, no write for %s, under %.2f cores, nothing landed for %s, at least %d workers\n",
		round(r.Thresholds.PTYActiveWithin), round(r.Thresholds.QuietWritesFor), r.Thresholds.IdleCores,
		round(r.Thresholds.NoProgressFor), r.Thresholds.MinWorkers)
	b.WriteString("\n")
	b.WriteString(detectorNote)
	return b.String()
}
