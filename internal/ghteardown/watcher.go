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
	// mailTo is `human`, not the mayor. A teardown miss is resolved by a human
	// posting an outward-facing comment and closing an issue; routing it to the
	// mayor's coordination inbox would put a human-gated action in a queue that
	// no human reads directly.
	mailTo = "human"
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
	// Enabled arms the runner.
	Enabled bool
}

// Watcher is the standing gh-issue teardown detector: it rides pogod's
// heartbeat, samples on a coarse interval, and mails `human` when a carrier
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
// Report-only: this type holds no seam through which an issue could be closed
// or commented on.
type Watcher struct {
	enabled       bool
	interval      time.Duration
	renotifyAfter time.Duration
	source        SourceFunc
	lookup        LookupFunc
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
	lookup := opts.Lookup
	if lookup == nil {
		lookup = GHLookup
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
	return &Watcher{
		enabled: opts.Enabled, interval: interval, renotifyAfter: renotify,
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
		w.mu.Lock()
		w.lastPrint = ""
		w.mu.Unlock()
		return
	}

	print := rep.fingerprint()
	if !w.shouldMail(print, now) {
		return
	}

	body := rep.Render() +
		"\nThis is REPORT-ONLY — pogod did NOT close or comment on anything. Closing an\n" +
		"external issue is outward-facing and stays human-gated.\n\n" +
		"Re-check on demand with:\n  pogo check-teardown\n"

	mailErr := w.mail(mailTo, mailFrom, "gh-issue teardown: "+rep.MailSubject(), body)

	details := map[string]any{
		"miss_count":          len(rep.Misses),
		"indeterminate_count": len(rep.Indeterminate),
		"declared_open_count": len(rep.DeclaredOpen),
		"scanned":             rep.Scanned,
	}
	if mailErr != nil {
		// The miss was detected but could not be reported — record it, because a
		// notice that reaches nobody is the exact failure this runner exists to
		// prevent, one level up.
		details["mail_error"] = mailErr.Error()
	}
	w.emit(events.Event{EventType: "gh_teardown_watch_fired", Agent: "pogod", Details: details})
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
	for _, group := range [][]Finding{r.Misses, r.Indeterminate} {
		for _, f := range group {
			fmt.Fprintf(&b, "%s|%s|%s\n", f.Carrier.ID, f.Kind, f.Carrier)
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
