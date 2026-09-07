package carrierdrift

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Default cadences for the standing runner.
const (
	// DefaultInterval is how often the runner re-reads every live carrier.
	//
	// An hour, matching ghteardown rather than ghintake's fifteen minutes,
	// because the cost profile is ghteardown's: one `gh issue view` per live
	// carrier, on somebody else's issue tracker. Fifteen minutes would be four
	// times the network for a class of drift measured in days.
	//
	// The number that matters is not the interval anyway — it is that the answer
	// is recomputed AT ALL. All three founding instances were found by accident,
	// on a fleet where the one number anybody watched was recomputed every
	// fifteen minutes and could not have shown any of them.
	DefaultInterval = 1 * time.Hour

	// DefaultRenotifyAfter is how long an UNCHANGED set of findings stays quiet
	// before being raised again. A full day: notification is on TRANSITION into
	// the state, and this is the slow backstop for a state nobody cleared.
	DefaultRenotifyAfter = 24 * time.Hour

	// DefaultEscalateAfter is how long ONE finding may persist, unbroken, before
	// the notice also goes to EscalateTo.
	//
	// 72h, ghteardown's figure rather than ghintake's four hours. The findings
	// here are already measured in days by construction — the windows that
	// produce them are 24h and 72h — so a four-hour escalation would escalate
	// essentially every finding on its first mail, which is escalation that
	// carries no information.
	DefaultEscalateAfter = 72 * time.Hour
)

const (
	mailFrom = "carrier-drift-watch"

	// DefaultNotifyTo is the mailbox drift findings are reported to.
	//
	// The COORDINATOR, because the coordinator is the agent that can act on all
	// three: it resolves a carrier against a closed issue, it dispatches the
	// triage that posts an acknowledgement, and it moves a stage. Routing this to
	// `human` would land an operational task in a maildir carrying ~990 unread
	// messages, where it could only be forwarded back — and a human handed work
	// they cannot action learns to filter the sender.
	DefaultNotifyTo = "mayor"

	// DefaultEscalateTo receives a finding the coordinator has demonstrably not
	// cleared in three days.
	DefaultEscalateTo = "human"
)

// SourceFunc yields the live carrier population, the number of work items
// examined to find it, and an error when the store could not be read.
//
// The error is not optional decoration: zero carriers and an unreadable store
// both render as "nothing to report", and letting them collapse is the failure
// this detector is about, one level up.
type SourceFunc func() ([]Carrier, int, error)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. As in the sibling detectors this is the ONLY side-effect channel the
// runner has — there is deliberately no seam through which it could comment on
// an issue, close one, or edit a work item.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	// Source produces the live carriers. Required.
	Source SourceFunc
	// Snapshot re-reads one issue. Required — a runner that cannot re-read is
	// the defect, not the detector.
	Snapshot SnapshotFunc
	// Statuses names the mg statuses Source covers, so the mailed report can
	// state its own coverage. Without it the coverage line reads "in status []"
	// — a report asserting it examined nothing, which is the claim this detector
	// exists to catch, made by the detector about itself.
	Statuses []string
	// Mail delivers the notice. Required.
	Mail MailFunc
	// Emit writes the carrier_drift_watch_* events. Defaults to events.Emit.
	Emit Emitter
	// Interval is the coarse sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// Windows are the three thresholds. Zero fields take their defaults;
	// negative fields turn individual checks off.
	Windows Windows
	// RenotifyAfter is how long unchanged findings stay quiet. Zero means
	// DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox findings are reported to. Empty means
	// DefaultNotifyTo.
	NotifyTo string
	// EscalateAfter is how long ONE finding may persist before the notice ALSO
	// goes to EscalateTo. Zero means DefaultEscalateAfter; negative disables
	// escalation entirely.
	EscalateAfter time.Duration
	// EscalateTo receives escalated notices. Empty means DefaultEscalateTo.
	EscalateTo string
	// Workers bounds concurrent issue re-reads. Zero picks a small default.
	Workers int
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing carrier RE-READ: it rides pogod's heartbeat, samples
// on a coarse interval, and mails the coordinator when a live carrier's record
// has drifted from what its issue looks like now.
//
// # Why a standing runner and not a command
//
// This is the load-bearing half of the ticket, not a deployment detail. All
// three founding instances were found on the same day by three unrelated
// accidents — a coordinator reading an issue for another reason, a coordinator
// noticing a duplicate, a coordinator checking a state before a dispatch.
// Everything the fleet ran on a schedule reported clean throughout, accurately.
// The requirement the ticket states is that a carrier's staleness be visible
// WITHOUT anyone having an accident, and a CLI nobody is scheduled to run does
// not meet it: internal/verdictwatch was a correct, audited detector that
// NOTHING RAN.
//
// It rides the heartbeat rather than a launchd timer for the reason its siblings
// do: the nondemand-spawn wedge on this box (mg-50e0) leaves launchd timers
// silently never firing, which is the same "inert while appearing correct"
// shape this detector exists to catch.
//
// # Not a replacement for the dispatch-time check
//
// The coordinator already runs `gh issue view <n> --json state` before every
// dispatch, which catches the CLOSED case at the one moment it does the most
// harm. That stopgap is better than this runner at what it covers and blind to
// the other two: it fires only when a dispatch is being considered, so a carrier
// nobody is about to dispatch — which is every one of the acknowledgement and
// stage instances — is never examined. The two are complements. This one answers
// the question when nobody is asking it.
//
// # Notification policy: by condition, not by message
//
// Findings are fingerprinted. A CHANGED set mails immediately — a newly drifted
// carrier is news. An UNCHANGED set stays quiet until RenotifyAfter, so a
// carrier drifting for a week costs one mail a day rather than one an hour.
// Crossing the escalation threshold is itself a change and mails at once, so a
// slow renotify interval cannot postpone escalation.
//
// Escalation is per FINDING, not per finding-set: a new finding arriving
// alongside an old one must not reset the old one's clock, which would be
// exactly the bug that lets the forgotten case stay forgotten.
//
// The escalation clock lives in memory, so a pogod restart restarts it. Stated
// rather than hidden — the daily notice to the coordinator survives a restart
// regardless, and the findings themselves are recomputed from live state every
// sample. That last part is the guarantee that matters here: NOTHING this
// watcher remembers can keep a finding alive that has cleared, or suppress one
// that has not, because every field it keeps is a mail-throttling decision and
// every field it reports comes from the sample in front of it.
//
// Report-only: this type holds no seam through which an issue could be
// commented on or a work item edited.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	windows       Windows
	renotifyAfter time.Duration
	escalateAfter time.Duration
	notifyTo      string
	escalateTo    string
	workers       int
	statuses      []string
	source        SourceFunc
	snapshot      SnapshotFunc
	mail          MailFunc
	emit          Emitter

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
	// Zero means "unset, use the default"; NEGATIVE means "off". A config that
	// omits the key must not silently disable escalation.
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
		enabled: opts.Enabled, interval: interval, windows: opts.Windows.resolve(),
		renotifyAfter: renotify, escalateAfter: escalate,
		notifyTo: notifyTo, escalateTo: escalateTo, workers: opts.Workers,
		statuses: append([]string(nil), opts.Statuses...),
		source:   opts.Source, snapshot: opts.Snapshot, mail: opts.Mail, emit: emit,
	}
}

// Windows reports the configured thresholds, for the startup log line.
func (w *Watcher) Windows() Windows {
	if w == nil {
		return Windows{}
	}
	return w.windows
}

// Check runs one sample subject to the coarse throttle. It is the integration
// point for the heartbeat OnTick callback, and a no-op on all but the first tick
// of each interval.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.enabled || w.source == nil || w.snapshot == nil || w.mail == nil {
		return
	}
	if !w.due(now) {
		return
	}
	w.sample(now)
}

// due reports whether the interval has elapsed, recording now BEFORE the sample
// runs so a slow or failing sample still consumes its slot — one sample per
// interval, never one per tick.
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
	carriers, items, err := w.source()
	if err != nil {
		// A store read that failed is not a clean pass. Emit it so a blind
		// detector is visible in the event log rather than indistinguishable from
		// a quiet one.
		w.emit(events.Event{
			EventType: "carrier_drift_watch_error",
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	rep := Detect(carriers, Prefetch(carriers, w.snapshot, w.workers), now, w.windows)
	rep.StoreItems = items
	rep.Statuses = w.statuses
	if !rep.Actionable() {
		// Clear the fingerprint and the clocks so a carrier that drifts later is
		// treated as news rather than suppressed as "unchanged". The coordinator
		// DID act, so a later finding starts its clock fresh.
		w.mu.Lock()
		w.lastPrint = ""
		w.firstSeen = nil
		w.mu.Unlock()
		w.emit(events.Event{
			EventType: "carrier_drift_watch_clean",
			Agent:     "pogod",
			Details: map[string]any{
				"scanned": rep.Scanned, "current": rep.Current,
				"declared": len(rep.Declared), "store_items": rep.StoreItems,
			},
		})
		return
	}

	oldest := w.trackAges(rep, now)
	stalled := w.escalateAfter > 0 && !oldest.IsZero() && now.Sub(oldest) >= w.escalateAfter

	// The escalation bit is part of the fingerprint so crossing the threshold
	// counts as a change and mails at once. Without it a 24h renotify window
	// could sit on a 72h escalation for a day.
	if !w.shouldMail(rep.fingerprint(stalled), now) {
		return
	}

	body := rep.Render() +
		"\nThis is REPORT-ONLY — pogod did NOT comment on any issue, close any issue, or\n" +
		"edit any work item. What to do about a drifted carrier is a judgement, and it\n" +
		"stays with the coordinator.\n\n" +
		"Re-read on demand with:\n  pogo check-carriers\n"

	recipients := []string{w.notifyTo}
	if stalled && w.escalateTo != w.notifyTo {
		recipients = append(recipients, w.escalateTo)
		body = fmt.Sprintf(
			"ESCALATED: a finding below has stood, unbroken, for %s since it was first reported\n"+
				"to %s. A drifted carrier is a fleet workflow matter; a fleet not clearing one for\n"+
				"that long is not — which is why this notice also reached %s.\n\n",
			now.Sub(oldest).Round(time.Minute), w.notifyTo, w.escalateTo) + body
	}

	subject := "carrier re-read: " + rep.MailSubject()
	details := map[string]any{
		"closed_count":         len(rep.Closed),
		"unacknowledged_count": len(rep.Unacknowledged),
		"stuck_stage_count":    len(rep.StuckStage),
		"blocked_count":        len(rep.Blocked),
		"indeterminate_count":  len(rep.Indeterminate),
		"declared_count":       len(rep.Declared),
		"instrument_failure":   rep.InstrumentFailure(),
		"scanned":              rep.Scanned,
		"current":              rep.Current,
		"store_items":          rep.StoreItems,
		"notified":             strings.Join(recipients, ","),
		"escalated":            stalled,
	}
	for _, to := range recipients {
		if err := w.mail(to, mailFrom, subject, body); err != nil {
			// Detected and not reported — record it, because a notice that
			// reaches nobody is this package's own subject matter one level up.
			details["mail_error_"+to] = err.Error()
		}
	}
	w.emit(events.Event{EventType: "carrier_drift_watch_fired", Agent: "pogod", Details: details})
}

// trackAges records when each currently-actionable finding was FIRST seen and
// forgets the ones that cleared, returning the earliest first-seen time still
// outstanding.
//
// Ages are tracked per FINDING rather than per finding-set because a set
// fingerprint changes whenever any member changes: a newly drifted carrier would
// otherwise reset the clock on an old one, and the stalest finding — the one
// escalation exists for — would be the one that never aged.
func (w *Watcher) trackAges(rep Report, now time.Time) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	findings := rep.Findings()
	seen := make(map[string]time.Time, len(findings))
	var oldest time.Time
	for _, f := range findings {
		at, ok := w.firstSeen[f.Key()]
		if !ok {
			at = now
		}
		seen[f.Key()] = at
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	w.firstSeen = seen
	return oldest
}

// shouldMail applies the change-or-daily policy described on Watcher.
func (w *Watcher) shouldMail(print string, now time.Time) bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if print != w.lastPrint || now.Sub(w.lastMailed) >= w.renotifyAfter {
		w.lastPrint = print
		w.lastMailed = now
		return true
	}
	return false
}

// fingerprint identifies a set of findings so an unchanged set can be recognised
// across samples.
//
// Built from the finding KEYS — kind plus carrier plus ref — and not from their
// ages. A finding whose age ticks up by an hour is the same finding, and folding
// the age in would make every sample "changed", which is a renotify policy that
// does not exist. The instrument-failure bit IS included: a pass that measured
// nothing means something different from the same list measured successfully,
// and without it here a 24h window could sit on that transition for a day.
func (r Report) fingerprint(escalated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "escalated=%t instrument_failure=%t\n", escalated, r.InstrumentFailure())
	keys := make([]string, 0, len(r.Closed)+len(r.Unacknowledged)+len(r.StuckStage)+
		len(r.Blocked)+len(r.Indeterminate))
	for _, f := range r.Findings() {
		keys = append(keys, f.Key())
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k)
		b.WriteString("\n")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
