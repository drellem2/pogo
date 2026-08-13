package reviewdecl

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Default cadences for the standing runner.
const (
	// DefaultInterval is how often the runner samples. Coarse, and cheap: the
	// scan is a filesystem walk of four directories with no network and no
	// subprocess, so the interval is set by how fast a finding needs to reach a
	// coordinator, not by cost. A missed declaration costs ONE ROUND, and a
	// round runs for a median of 8 minutes — half an hour is inside the window
	// where the information can still change what happens.
	DefaultInterval = 30 * time.Minute
	// DefaultRenotifyAfter is how long an UNCHANGED set of findings stays quiet
	// before being raised again. See the Watcher doc.
	DefaultRenotifyAfter = 24 * time.Hour
)

// DefaultNotifyTo is the mailbox findings go to: the COORDINATOR, because the
// coordinator is the agent that files review tickets (mayor.md transition 3) and
// therefore the only one that can write the missing line.
//
// It is deliberately NOT `human`, and not escalated to `human` after any
// interval. mg-253e priced this residual itself: a missed write costs one
// recoverable round, not an indefinite slot. Routing a recoverable-round gap to
// a maildir carrying ~990 unread messages would spend a scarce reader on
// defence-in-depth and get every sibling detector filtered alongside it.
//
// A mirror of config.DefaultCoordinator rather than an import of it, so this
// package stays independent of a resolved host layout — the posture ghteardown
// and agent.DefaultCoordinatorName already take.
const DefaultNotifyTo = "mayor"

const mailFrom = "review-decl-watch"

// SourceFunc yields the items to audit. Production binds Source.Items; tests
// substitute a fixture so no store — live or scratch — is involved.
//
// It returns an error rather than an empty slice when the store cannot be read,
// because those two must never collapse into the same "nothing to report".
type SourceFunc func() ([]Item, error)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. It is the ONLY side-effect channel this runner has — there is
// deliberately no seam through which it could edit the work item it names.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	// Source lists work items. Required.
	Source SourceFunc
	// Mail delivers the notice. Required — a runner that cannot report is pointless.
	Mail MailFunc
	// Emit writes the review_decl_watch_* events. Defaults to events.Emit.
	Emit Emitter
	// Interval is the coarse sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// RenotifyAfter is how long unchanged findings stay quiet. Zero means
	// DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox findings are reported to. Empty means
	// DefaultNotifyTo.
	NotifyTo string
	// Boundary is the convention start date. Zero means ConventionLandedAt.
	Boundary time.Time
	// Statuses names the coverage the source provides, carried into the report
	// so the rendered denominator can state it.
	Statuses []string
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing detector for review tickets that declare no build
// item: it rides pogod's heartbeat, samples on a coarse interval, and mails the
// coordinator when a review ticket filed since the convention landed carries no
// usable `reviews:` line.
//
// It rides the heartbeat rather than a launchd timer for the reason
// internal/driftwatch and internal/ghteardown do: the nondemand-spawn wedge on
// this box (mg-50e0) leaves launchd timers silently never firing, which is
// precisely the "inert while appearing correct" failure this detector exists to
// catch. A detector that never runs is the bug wearing the fix's clothes.
//
// # A DETECTOR FOR AN UNWRITTEN LINE MUST NOT ITSELF GO UNRUN
//
// This runner exists at all — rather than a CLI command a coordinator is
// instructed to run — because of what the sibling next door cost. verdictwatch
// was a working, audited detector that NOTHING RAN: zero schedules, zero cron
// entries, zero references outside its own directory. Shipping a check for "the
// coordinator did not do the thing it was told to do", and then relying on the
// coordinator to remember to run it, would reproduce mg-253e's own defect one
// level up. The CLI (`pogo check-review-decl`) is the on-demand half of this
// runner, not the delivery mechanism.
//
// The same reasoning is why sample() emits an event on EVERY run and not only on
// a finding. "The detector ran and found nothing" and "the detector has not run
// since the last restart" are the two states this whole lineage keeps confusing,
// and an absence cannot distinguish them. review_decl_watch_ran is the positive
// record.
//
// # Notification policy
//
// Findings are fingerprinted. A CHANGED set mails immediately — a new unprotected
// review is news, and it is news with a short shelf life, since the round it
// affects is running now. An UNCHANGED set stays quiet until RenotifyAfter has
// elapsed, then mails again.
//
// Neither extreme is safe. Mailing every interval trains the reader to filter the
// sender, and a muted detector is worse than none because it also manufactures
// the feeling of coverage. But mailing only on change lets a finding nobody
// actioned fall permanently silent.
//
// # Report-only
//
// This type holds no seam through which a work item could be written. See the
// package doc for why that is load-bearing here rather than conventional.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	renotifyAfter time.Duration
	notifyTo      string
	boundary      time.Time
	statuses      []string
	source        SourceFunc
	mail          MailFunc
	emit          Emitter

	mu         sync.Mutex
	lastRun    time.Time
	ran        bool
	lastPrint  string    // fingerprint of the last mailed finding set
	lastMailed time.Time // when that fingerprint was last mailed
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
	notifyTo := opts.NotifyTo
	if notifyTo == "" {
		notifyTo = DefaultNotifyTo
	}
	boundary := opts.Boundary
	if boundary.IsZero() {
		boundary = ConventionLandedAt
	}
	return &Watcher{
		enabled: opts.Enabled, interval: interval, renotifyAfter: renotify,
		notifyTo: notifyTo, boundary: boundary, statuses: opts.Statuses,
		source: opts.Source, mail: opts.Mail, emit: emit,
	}
}

// Check runs one sample subject to the coarse throttle. It is the integration
// point for the heartbeat OnTick callback, and a no-op on all but the first tick
// of each interval.
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
	items, err := w.source()
	if err != nil {
		// A store that cannot be read is a real failure, not a clean scan. Emit
		// it so a blind detector is visible in the event log rather than
		// indistinguishable from a quiet one.
		w.emit(events.Event{
			EventType: "review_decl_watch_error",
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	rep := Detect(items, w.boundary)
	rep.Statuses = w.statuses

	// The positive record, emitted on EVERY run including the clean ones — see
	// the Watcher doc. The denominators travel with it, so an operator asking
	// "was this detector seeing anything?" gets an answer from the event log
	// without re-reading mail bodies.
	w.emit(events.Event{
		EventType: "review_decl_watch_ran",
		Agent:     "pogod",
		Details: map[string]any{
			"scanned":        rep.Scanned,
			"population":     rep.Population,
			"declared":       len(rep.Declared),
			"missing":        len(rep.Missing),
			"self_reference": len(rep.SelfReference),
			"malformed":      len(rep.Malformed),
			"undatable":      len(rep.Undatable),
			"pre_convention": len(rep.PreConvention),
			"opaque":         len(rep.Opaque),
			// The build half of each gh-issue pair, set aside rather than
			// audited. It travels with the denominators because it is a
			// SUBTRACTION from them: an operator reading `scanned` needs to see
			// what the stage line collected and the classifier removed, or a
			// classifier that over-excludes looks exactly like a quiet week.
			"build_tickets": len(rep.BuildTickets),
			"unprotected":   rep.Unprotected(),
			"actionable":    rep.Actionable(),
			"boundary":      w.boundary.UTC().Format(time.RFC3339),
			"statuses":      strings.Join(rep.Statuses, ","),
		},
	})

	if !rep.Actionable() {
		// Clear the fingerprint so a finding that is resolved and later recurs is
		// treated as news again rather than suppressed as "unchanged".
		w.mu.Lock()
		w.lastPrint = ""
		w.mu.Unlock()
		return
	}

	if !w.shouldMail(rep.fingerprint(), now) {
		return
	}

	body := rep.Render() +
		"\nThis is REPORT-ONLY — pogod did NOT write a `reviews:` line on anything. A\n" +
		"coordinator that skipped one may have had a reason, and a detector that repairs\n" +
		"the thing it measures cannot be trusted to measure it (mg-253e).\n\n" +
		"Re-check on demand with:\n  pogo check-review-decl\n"

	subject := "review declarations: " + rep.MailSubject()
	details := map[string]any{
		"unprotected": rep.Unprotected(),
		"scanned":     rep.Scanned,
		"population":  rep.Population,
		"notified":    w.notifyTo,
	}
	if err := w.mail(w.notifyTo, mailFrom, subject, body); err != nil {
		// The finding was detected but could not be reported — record it, because
		// a notice that reaches nobody is this detector's own failure mode, one
		// level up.
		details["mail_error"] = err.Error()
	}
	w.emit(events.Event{EventType: "review_decl_watch_fired", Agent: "pogod", Details: details})
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

// fingerprint identifies a set of findings, so an unchanged set can be
// recognised across samples. Built from the ACTIONABLE findings only —
// pre-convention, declared and opaque items never mail, so a change among them
// must not trigger one.
func (r Report) fingerprint() string {
	var b strings.Builder
	for _, g := range [][]Finding{r.Missing, r.SelfReference, r.Malformed, r.Undatable} {
		for _, f := range g {
			fmt.Fprintf(&b, "%s|%s|%s\n", f.Item.ID, f.Kind, f.Detail)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
