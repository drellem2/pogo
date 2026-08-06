package ghteardown

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
	// DefaultInterval is how often the runner samples. Coarse on purpose: each
	// sample costs one network round-trip per done carrier, and a teardown miss
	// that has already lasted hours is not made worse by being found ten minutes
	// later.
	DefaultInterval = 1 * time.Hour
	// DefaultRenotifyAfter is how long an UNCHANGED set of findings stays quiet
	// before it is raised again. See the Watcher doc for why this is not zero
	// and not infinite.
	DefaultRenotifyAfter = 24 * time.Hour
	// DefaultEscalateAfter is how long a single actionable finding may persist
	// before the notice ALSO goes to EscalateTo. See "Routing" on Watcher.
	DefaultEscalateAfter = 72 * time.Hour
)

// SourceFunc yields the carriers to audit. Production binds MGSource.Carriers;
// tests substitute a fixture so no store — live or scratch — is involved.
//
// It returns an error rather than an empty slice when the store cannot be read,
// because those two must never collapse into the same "nothing to report".
type SourceFunc func() ([]Carrier, error)

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. As in internal/driftwatch, this is the ONLY side-effect channel the
// runner has — there is deliberately no seam through which it could close or
// comment on an issue.
type MailFunc func(to, from, subject, body string) error

// Emitter writes an event to the shared log.
type Emitter func(events.Event)

const (
	mailFrom = "gh-teardown-watch"
	// DefaultNotifyTo is the fleet mailbox a teardown miss goes to (mg-b586).
	//
	// The finding is "our gh-issue workflow's last step did not run on carrier
	// X" — a FLEET WORKFLOW FAILURE, not a decision that needs a human. The
	// remedy is for the PM or the mayor to chase the close (the outward-facing
	// comment itself stays human-gated, which is why this package still only
	// reports). Mailing a human an operational task he can only forward back to
	// the fleet is how a sender gets filtered — the same reasoning the cadence
	// policy below already applies to frequency, applied to recipient.
	//
	// It also keeps the standing mail contract intact: `human` gets genuinely
	// urgent items and ONE batched daily digest. An hourly-sampling detector
	// mailing `human` directly would be a third, unbatched channel bypassing the
	// discipline the digest exists to enforce.
	//
	// WHICH fleet mailbox is the coordinator's (mg-f04b). The literal here is a
	// mirror of config.DefaultCoordinator, repeated rather than imported so
	// this package stays independent of a resolved host layout — the same
	// posture agent.DefaultCoordinatorName takes. It was `pm-pogo`, one
	// deployment's product PM, which named an agent no other install has.
	DefaultNotifyTo = "mayor"
	// DefaultEscalateTo receives a finding the fleet has demonstrably failed to
	// clear — see Options.EscalateAfter.
	DefaultEscalateTo = "human"
)

// Options carries the runner's dependencies.
type Options struct {
	// Source lists carriers. Required.
	Source SourceFunc
	// Lookup resolves issue state. Defaults to GHLookup.
	Lookup LookupFunc
	// Mail delivers the notice. Required — a runner that cannot report is pointless.
	Mail MailFunc
	// Emit writes the gh_teardown_watch_fired event. Defaults to events.Emit.
	Emit Emitter
	// Interval is the coarse sampling throttle. Zero means DefaultInterval.
	Interval time.Duration
	// RenotifyAfter is how long unchanged findings stay quiet. Zero means
	// DefaultRenotifyAfter.
	RenotifyAfter time.Duration
	// NotifyTo is the mailbox findings are reported to. Empty means
	// DefaultNotifyTo — a FLEET mailbox, deliberately not `human`.
	NotifyTo string
	// EscalateAfter is how long one actionable finding may persist, unbroken,
	// before the notice ALSO goes to EscalateTo. Zero means
	// DefaultEscalateAfter; negative disables escalation entirely.
	EscalateAfter time.Duration
	// EscalateTo receives escalated notices. Empty means DefaultEscalateTo.
	EscalateTo string
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing gh-issue teardown detector: it rides pogod's
// heartbeat, samples on a coarse interval, and mails the fleet when a carrier
// claims completion but its issue is still open.
//
// It rides the heartbeat rather than a launchd timer for the same reason
// internal/driftwatch does: the nondemand-spawn wedge on this box (mg-50e0)
// leaves launchd timers silently never firing, which is precisely the
// "inert while appearing correct" failure this detector exists to catch. A
// detector that never runs is the bug wearing the fix's clothes.
//
// # Notification policy
//
// Findings are fingerprinted. A CHANGED set mails immediately — a new miss is
// news. An UNCHANGED set stays quiet until RenotifyAfter has elapsed, then
// mails again.
//
// Neither extreme is safe. Mailing every interval trains the reader to filter
// the sender, and a muted detector is worse than none because it also
// manufactures the feeling of coverage — that is the whole reason `gh-open:`
// exists. But mailing only on change lets a miss that nobody actioned fall
// permanently silent, which is how mg-07ba's issue sat open for four days in
// the first place. Daily re-notification keeps an unresolved miss costing
// someone something.
//
// # A blind sample is not a sample (mg-dd22)
//
// The lookup this runner binds by default retries a network-class failure
// before giving up, because a detector sampling twice daily against a ~50%
// intermittent network (mg-0ffc) otherwise turns one blip into a full batch of
// non-answers. When the retries are exhausted anyway, the run says so in a
// different shape: the notice leads with SUSPECTED INSTRUMENT FAILURE and its
// subject line says the run measured nothing, rather than reporting a count
// that reads like a finding.
//
// The escalation clocks survive such a run untouched (see trackAges). A run
// that could not see a miss has not observed that the miss cleared, and letting
// a blip reset the clock would make an outage on this box a way to keep a
// forgotten finding forgotten — the exact failure escalation exists to prevent.
//
// # Routing (mg-b586)
//
// The same reasoning governs WHO is mailed, not just how often. Findings go to
// NotifyTo — a fleet mailbox, the coordinator by default — because a teardown miss is
// a workflow failure the fleet can chase, and a human handed an operational
// task he can only forward back learns to filter the sender exactly as surely
// as one mailed too often.
//
// But a finding the fleet does NOT clear is a different fact. Once a single
// actionable finding has persisted unbroken for EscalateAfter, the notice also
// goes to EscalateTo (`human`): at that point "the fleet is not handling this"
// is itself the signal, and that IS a human's to know. Escalation is per
// FINDING, not per finding-set — a new miss arriving alongside an old one must
// not reset the old one's clock, which is precisely the bug that would let the
// forgotten case stay forgotten.
//
// The escalation clock lives in memory, so a pogod restart restarts it. Stated
// rather than hidden: the detector's job is that a miss cannot be sat on
// indefinitely, and the daily notice to the fleet survives a restart regardless.
//
// Report-only: this type holds no seam through which an issue could be closed
// or commented on.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	renotifyAfter time.Duration
	escalateAfter time.Duration
	notifyTo      string
	escalateTo    string
	source        SourceFunc
	lookup        LookupFunc
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
	// The default lookup RETRIES a network-class failure (mg-dd22). It is the
	// default rather than something pogod opts into, so the unattended path —
	// the one that ran blind twice in 15 hours — cannot be the one that forgets.
	// A caller injecting its own Lookup owns its own retry policy; tests inject
	// bare functions precisely so they can exercise a single attempt.
	lookup := opts.Lookup
	if lookup == nil {
		lookup = RetryingLookup(GHLookup)
	}
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
	// Zero means "unset, use the default"; NEGATIVE means "off". Escalation
	// needs an explicit off switch that is distinguishable from an unset field,
	// or a config that omits the key would silently disable it.
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
		escalateAfter: escalate, notifyTo: notifyTo, escalateTo: escalateTo,
		source: opts.Source, lookup: lookup, mail: opts.Mail, emit: emit,
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
	carriers, err := w.source()
	if err != nil {
		// A store that cannot be read is a real failure, not a clean scan. Emit
		// it so a blind detector is visible in the event log rather than
		// indistinguishable from a quiet one.
		w.emit(events.Event{
			EventType: "gh_teardown_watch_error",
			Agent:     "pogod",
			Details:   map[string]any{"error": err.Error()},
		})
		return
	}

	rep := Detect(carriers, w.lookup)
	if !rep.Actionable() {
		// Clear the fingerprint so a miss that is resolved and later recurs is
		// treated as news again rather than being suppressed as "unchanged".
		// The escalation clocks go with it: a cleared finding that recurs starts
		// its stall clock fresh, because the fleet DID act on the first one.
		w.mu.Lock()
		w.lastPrint = ""
		w.firstSeen = nil
		w.mu.Unlock()
		return
	}

	oldest := w.trackAges(rep, now)

	print := rep.fingerprint()
	if !w.shouldMail(print, now) {
		return
	}

	body := rep.Render() +
		"\nThis is REPORT-ONLY — pogod did NOT close or comment on anything. Closing an\n" +
		"external issue is outward-facing and stays human-gated.\n\n" +
		"Re-check on demand with:\n  pogo check-teardown\n"

	stalled := w.escalateAfter > 0 && !oldest.IsZero() && now.Sub(oldest) >= w.escalateAfter
	recipients := []string{w.notifyTo}
	if stalled && w.escalateTo != w.notifyTo {
		recipients = append(recipients, w.escalateTo)
		body = fmt.Sprintf(
			"ESCALATED: a finding below has been reported to %s for %s without clearing.\n"+
				"The teardown miss itself is a fleet workflow matter, but a fleet that is not\n"+
				"resolving one is not — which is why this notice also reached %s.\n\n",
			w.notifyTo, now.Sub(oldest).Round(time.Hour), w.escalateTo) + body
	}

	subject := "gh-issue teardown: " + rep.MailSubject()
	details := map[string]any{
		"miss_count":          len(rep.Misses),
		"indeterminate_count": len(rep.Indeterminate),
		"blocked_count":       len(rep.Blocked),
		"declared_open_count": len(rep.DeclaredOpen),
		"scanned":             rep.Scanned,
		"notified":            strings.Join(recipients, ","),
		"escalated":           stalled,
		// A run that measured nothing is queryable as such in the event log,
		// not only readable in mail prose. An operator asking "how often does
		// this detector go blind?" needs a field, not a grep over bodies —
		// mg-dd22's own recurrence had to be established by re-reading two
		// mails by hand.
		"instrument_failure": rep.InstrumentFailure(),
	}
	if classes := rep.FailureClasses(); len(classes) > 0 {
		details["failure_classes"] = strings.Join(classes, ",")
	}
	for _, to := range recipients {
		if err := w.mail(to, mailFrom, subject, body); err != nil {
			// The miss was detected but could not be reported — record it, because
			// a notice that reaches nobody is the exact failure this runner exists
			// to prevent, one level up.
			details["mail_error_"+to] = err.Error()
		}
	}
	w.emit(events.Event{EventType: "gh_teardown_watch_fired", Agent: "pogod", Details: details})
}

// trackAges records when each currently-actionable finding was FIRST seen and
// forgets the ones that have cleared, returning the earliest first-seen time
// still outstanding. Ages are tracked per finding rather than per finding-set
// because a set fingerprint changes whenever any member changes: a new miss
// arriving would otherwise reset the clock on an old one, and the stalest
// finding — the one escalation exists for — would be the one that never aged.
func (w *Watcher) trackAges(rep Report, now time.Time) time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	seen := make(map[string]time.Time, len(rep.Misses)+len(rep.Indeterminate)+len(rep.Blocked))

	// A run that reached no verdict for any carrier is not evidence that a
	// previously-reported finding cleared — it is evidence the instrument
	// broke. Carrying the old clocks forward is what stops one network blip
	// from resetting the 72h escalation clock on a real miss it could not even
	// see: without this, a blip every few days would keep a permanently
	// un-actioned miss permanently un-escalated (mg-dd22).
	if rep.InstrumentFailure() {
		for k, v := range w.firstSeen {
			seen[k] = v
		}
	}

	// `oldest` is taken only over findings PRESENT in this report, so the
	// escalation banner always refers to something the reader can see below it.
	var oldest time.Time
	for _, group := range [][]Finding{rep.Misses, rep.Indeterminate, rep.Blocked} {
		for _, f := range group {
			key := string(f.Kind) + "|" + f.Carrier.ID
			at, ok := w.firstSeen[key]
			if !ok {
				at = now
			}
			seen[key] = at
			if oldest.IsZero() || at.Before(oldest) {
				oldest = at
			}
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

// fingerprint identifies a set of findings, so an unchanged set can be
// recognised across samples. Built from the actionable findings only —
// declared-open carriers never mail, so a change among them must not trigger
// one.
func (r Report) fingerprint() string {
	var b strings.Builder
	for _, group := range [][]Finding{r.Misses, r.Indeterminate, r.Blocked} {
		for _, f := range group {
			fmt.Fprintf(&b, "%s|%s|%s\n", f.Carrier.ID, f.Kind, f.Carrier)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
