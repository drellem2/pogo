package ghintake

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
	// DefaultInterval is how often the runner samples.
	//
	// Much finer than ghteardown's hour, because the two detectors are racing
	// different clocks. A teardown miss is an issue left open after the work
	// landed — bad, but the work is done and the record exists. An intake miss is
	// a reporter with no acknowledgement and no record anywhere, and #99 spent
	// ten hours in that state. Fifteen minutes costs one `gh issue list` per
	// watched repo (three calls on this host) plus a local store scan.
	DefaultInterval = 15 * time.Minute

	// DefaultGrace is how long an open issue may exist with no carrier before it
	// becomes a finding.
	//
	// Not zero. The poller mails within ~60s of an issue being filed and the
	// coordinator needs a turn to read it, form a triage title, and file the
	// carrier; alarming inside that window would make this detector fire on
	// literally every new issue, and a detector that fires on the happy path is
	// one that gets muted. Thirty minutes is comfortably longer than a
	// coordinator cycle and two orders of magnitude shorter than the failure it
	// exists to catch.
	DefaultGrace = 30 * time.Minute

	// DefaultRenotifyAfter is how long an UNCHANGED set of findings stays quiet
	// before being raised again. Deliberately a full day: the ticket's own
	// requirement is that an issue uncarried for a week must not generate a
	// daily-times-many mail. Notification is on TRANSITION into the state; this
	// is the slow backstop for a state nobody cleared.
	DefaultRenotifyAfter = 24 * time.Hour

	// DefaultEscalateAfter is how long ONE uncarried issue may persist before the
	// notice also goes to EscalateTo. See "Routing, and what happens if the
	// coordinator is down" on Watcher.
	DefaultEscalateAfter = 4 * time.Hour
)

const (
	mailFrom = "gh-intake-watch"

	// DefaultNotifyTo is the mailbox an uncarried issue is reported to.
	//
	// The COORDINATOR, not `human`. (Its sibling ghteardown once defaulted to a
	// named PM instead; mg-f04b brought that back to the coordinator too, since
	// the PM in question existed on one machine.)
	// The remedy for an uncarried issue is to file a carrier and dispatch triage,
	// and the mayor is the only agent that does either. Routing it anywhere else
	// would produce a notice whose recipient can only forward it back, and a
	// human handed an operational task he cannot action learns to filter the
	// sender — the `human` maildir already carries ~990 unread messages, which is
	// what that lesson looks like after it has been ignored.
	//
	// There is an added reason particular to this detector: the failure being
	// detected IS a coordinator failure. Mailing the coordinator is not just
	// routing to the actor, it is closing the loop on the agent whose dropped
	// mail created the gap.
	DefaultNotifyTo = "mayor"

	// DefaultEscalateTo receives a finding the coordinator has demonstrably not
	// cleared.
	DefaultEscalateTo = "human"
)

// SourceFunc yields one full inventory. Production binds a closure over MGSource
// and GHOpenIssues; tests substitute a fixture so no store — live or scratch —
// and no network are involved.
//
// It returns an error rather than an empty Inventory when the carrier scan fails,
// because those two must never collapse into the same "nothing to report".
type SourceFunc func() (Inventory, error)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. As in internal/ghteardown, this is the ONLY side-effect channel the
// runner has — there is deliberately no seam through which it could file a work
// item or comment on an issue.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

// Options carries the runner's dependencies.
type Options struct {
	// Source produces the inventory. Required.
	Source SourceFunc
	// Mail delivers the notice. Required — a runner that cannot report is pointless.
	Mail MailFunc
	// Emit writes the gh_intake_watch_* events. Defaults to events.Emit.
	Emit Emitter
	// Interval is the coarse sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// Grace is the new-issue window. Zero means DefaultGrace; negative disables
	// the window so every uncarried issue counts immediately.
	Grace time.Duration
	// RenotifyAfter is how long unchanged findings stay quiet. Zero means
	// DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox findings are reported to. Empty means
	// DefaultNotifyTo (the coordinator).
	NotifyTo string
	// EscalateAfter is how long one uncarried issue may persist, unbroken, before
	// the notice ALSO goes to EscalateTo. Zero means DefaultEscalateAfter;
	// negative disables escalation entirely.
	EscalateAfter time.Duration
	// EscalateTo receives escalated notices. Empty means DefaultEscalateTo.
	EscalateTo string
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing gh-issue INTAKE detector: it rides pogod's heartbeat,
// samples on a coarse interval, and mails the coordinator when an open issue on a
// watched repo has no work item carrying its `gh:` ref.
//
// It rides the heartbeat rather than a launchd timer for the same reason
// internal/driftwatch and internal/ghteardown do: the nondemand-spawn wedge on
// this box (mg-50e0) leaves launchd timers silently never firing, which is
// precisely the "inert while appearing correct" failure this detector exists to
// catch. A detector that never runs is the bug wearing the fix's clothes.
//
// # Why here and not in the poller
//
// The poller is the only component holding the SENT side of the ledger — it logs
// every `[gh]` mail it delivered — and that is a real argument for putting the
// reconciliation there. It was declined for three reasons.
//
// First, the sent ledger is the wrong population. What matters is not "did we
// mail about this issue" but "does a carrier exist for this open issue". An issue
// filed before the poller's state was last reset, one whose mail was sent by a
// previous poller generation, or one the poller never saw at all would all be
// absent from the sent side and present in the population that counts. GitHub's
// open-issue list is authoritative; the mail log is a proxy for it.
//
// Second, the poller is a standalone utility in another repo, deliberately kept
// as a dumb transport (see docs/design/gh-issue-workflow-design.md §4: "the
// poller keeps mailing mayor; orchestration belongs in prompts"). Teaching it to
// read the mg store would couple it to pogo internals and give it a second job
// with a different failure mode.
//
// Third and decisive: putting the check in the poller would make the poller
// responsible for the coordinator's follow-through, while giving it no way to
// notice its own. pogod already hosts the sibling detectors, already has durable
// mail, condition annunciation, and an escalation path past the coordinator — all
// of which this needs.
//
// # Routing, and what happens if the coordinator is down
//
// Findings go to NotifyTo (`mayor`), the only agent that can file a carrier.
// If that agent is stopped, wedged, or dropping mail — which is exactly the
// condition that produced #99 — the notice does not evaporate: mg mail is a
// durable maildir, so it is waiting when the coordinator next runs, and the
// finding is recomputed from live state every interval regardless of who read
// what.
//
// But a durable notice nobody acts on is still an uncarried issue. So once a
// SINGLE uncarried issue has persisted unbroken for EscalateAfter, the notice
// also goes to EscalateTo (`human`). At that point "the coordinator is not
// handling this" has become the news, and that is a human's to know. This is the
// answer to "what if mayor is down": the detector does not depend on the
// coordinator being alive to be useful, only to be sufficient.
//
// Escalation is per ISSUE, not per finding-set: a new uncarried issue arriving
// alongside an old one must not reset the old one's clock, which would be
// precisely the bug that lets the forgotten case stay forgotten.
//
// # Notification policy: by condition, not by message
//
// Findings are fingerprinted. A CHANGED set mails immediately — a newly
// uncarried issue is news. An UNCHANGED set stays quiet until RenotifyAfter, so
// an issue uncarried for a week costs one mail a day rather than one a sample.
// Crossing the escalation threshold is itself a change and mails at once, so a
// slow renotify interval cannot delay escalation.
//
// The escalation clock lives in memory, so a pogod restart restarts it. Stated
// rather than hidden: the daily notice to the coordinator survives a restart
// regardless, and the finding itself is recomputed from live state every sample.
//
// Report-only: this type holds no seam through which a work item could be filed
// or an issue commented on. Filing the carrier is a judgement about what the
// issue IS, and that stays with the coordinator.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	grace         time.Duration
	renotifyAfter time.Duration
	escalateAfter time.Duration
	notifyTo      string
	escalateTo    string
	source        SourceFunc
	mail          MailFunc
	emit          Emitter

	mu         sync.Mutex
	lastRun    time.Time
	ran        bool
	lastPrint  string    // fingerprint of the last mailed finding set
	lastMailed time.Time // when that fingerprint was last mailed
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
	// Zero means "unset, use the default"; NEGATIVE means "off". Both the grace
	// window and escalation need an off switch distinguishable from an unset
	// field, or a config that omits the key would silently disable them.
	grace := opts.Grace
	if grace == 0 {
		grace = DefaultGrace
	}
	renotify := opts.RenotifyAfter
	if renotify <= 0 {
		renotify = DefaultRenotifyAfter
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
		enabled: opts.Enabled, interval: interval, grace: grace,
		renotifyAfter: renotify, escalateAfter: escalate,
		notifyTo: notifyTo, escalateTo: escalateTo,
		source: opts.Source, mail: opts.Mail, emit: emit,
	}
}

// Grace reports the configured new-issue window, for the startup log line.
func (w *Watcher) Grace() time.Duration {
	if w == nil {
		return 0
	}
	return w.grace
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
	inv, err := w.source()
	if err != nil {
		// A carrier scan that failed is not a clean reconciliation. Emit it so a
		// blind detector is visible in the event log rather than indistinguishable
		// from a quiet one.
		w.emit(events.Event{
			EventType: "gh_intake_watch_error",
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	rep := Detect(inv, now, w.grace)
	if !rep.Actionable() {
		// Clear the fingerprint so an issue that gets carried and a later one
		// that does not are treated as news again rather than suppressed as
		// "unchanged". The escalation clocks go with it: the coordinator DID act,
		// so a later finding starts its clock fresh.
		w.mu.Lock()
		w.lastPrint = ""
		w.firstSeen = nil
		w.mu.Unlock()
		w.emit(events.Event{
			EventType: "gh_intake_watch_clean",
			Agent:     "pogod",
			Details: map[string]any{
				"scanned": rep.Scanned, "carried": rep.Carried,
				"fresh": len(rep.Fresh), "carrier_refs": rep.CarrierRefs,
				"items_scanned": rep.ItemsScanned,
			},
		})
		return
	}

	oldest := w.trackAges(rep, now)
	stalled := w.escalateAfter > 0 && !oldest.IsZero() && now.Sub(oldest) >= w.escalateAfter

	// The escalation bit is part of the fingerprint so crossing the threshold
	// counts as a change and mails at once. Without it a 24h renotify interval
	// would silently postpone a 4h escalation by up to a day.
	if !w.shouldMail(rep.fingerprint(stalled), now) {
		return
	}

	body := rep.Render() +
		"\nThis is REPORT-ONLY — pogod did NOT file a work item and did NOT comment on any\n" +
		"issue. What an issue IS (triage, duplicate, out of scope, a question) is a\n" +
		"judgement, and it stays with the coordinator.\n\n" +
		"Re-check on demand with:\n  pogo check-intake\n"

	recipients := []string{w.notifyTo}
	if stalled && w.escalateTo != w.notifyTo {
		recipients = append(recipients, w.escalateTo)
		body = fmt.Sprintf(
			"ESCALATED: an issue below has been reported to %s for %s without a carrier being\n"+
				"filed. An uncarried issue is a fleet workflow matter, but a fleet that is not\n"+
				"clearing one for that long is not — which is why this notice also reached %s.\n\n",
			w.notifyTo, now.Sub(oldest).Round(time.Minute), w.escalateTo) + body
	}

	subject := "gh-issue intake: " + rep.MailSubject()
	details := map[string]any{
		"uncarried_count": len(rep.Uncarried),
		"repo_errors":     len(rep.RepoErrors),
		"item_errors":     len(rep.ItemErrors),
		"blind_store":     rep.BlindStore,
		"fresh_count":     len(rep.Fresh),
		"scanned":         rep.Scanned,
		"carried":         rep.Carried,
		"carrier_refs":    rep.CarrierRefs,
		"items_scanned":   rep.ItemsScanned,
		"notified":        strings.Join(recipients, ","),
		"escalated":       stalled,
	}
	for _, to := range recipients {
		if err := w.mail(to, mailFrom, subject, body); err != nil {
			// The miss was detected but could not be reported — record it, because
			// a notice that reaches nobody is this package's own failure mode
			// reproduced one level up.
			details["mail_error_"+to] = err.Error()
		}
	}
	w.emit(events.Event{EventType: "gh_intake_watch_fired", Agent: "pogod", Details: details})
}

// trackAges records when each currently-actionable finding was FIRST seen and
// forgets the ones that cleared, returning the earliest first-seen time still
// outstanding.
//
// Ages are tracked per finding rather than per finding-set because a set
// fingerprint changes whenever any member changes: a newly uncarried issue would
// otherwise reset the clock on an old one, and the stalest finding — the one
// escalation exists for — would be the one that never aged.
//
// Repo errors and a blind store are tracked too. A detector that has been unable
// to see for four hours is exactly as escalation-worthy as an issue nobody
// carried for four hours; the ten hours #99 went missing were ten hours in which
// nothing was looking.
func (w *Watcher) trackAges(rep Report, now time.Time) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	seen := make(map[string]time.Time, len(rep.Uncarried)+len(rep.RepoErrors)+1)
	var oldest time.Time
	note := func(key string) {
		at, ok := w.firstSeen[key]
		if !ok {
			at = now
		}
		seen[key] = at
		if oldest.IsZero() || at.Before(oldest) {
			oldest = at
		}
	}
	for _, f := range rep.Uncarried {
		note("uncarried|" + f.Issue.Ref())
	}
	for _, e := range rep.RepoErrors {
		note("repo_error|" + e.Repo)
	}
	if rep.BlindStore {
		note("blind_store")
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
// across samples. Built from the ACTIONABLE findings only — fresh issues never
// mail, so an issue ageing out of the grace window changes the print exactly
// once, when it becomes a finding, which is the transition worth a mail.
// The credential state is part of the print because it changes what the SAME
// set of unreadable repos means and what the notice tells a reader to do: a
// credential that has gone missing turns "two repos are unreadable, check the
// network" into "one credential is gone, run gh auth login". That is news, and
// without it here a 24h renotify window could sit on the transition for a day.
func (r Report) fingerprint(escalated bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "escalated=%t blind=%t cred=%s\n", escalated, r.BlindStore, r.Credential)
	for _, f := range r.Uncarried {
		fmt.Fprintf(&b, "uncarried|%s\n", f.Issue.Ref())
	}
	for _, e := range r.RepoErrors {
		fmt.Fprintf(&b, "repo_error|%s\n", e.Repo)
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
