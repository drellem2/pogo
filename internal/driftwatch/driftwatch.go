// Package driftwatch implements pogod's drift-check RUNNER — the DETECTION
// backstop that mg-5701 shipped without (a detector proven to fire in a test
// but never wired to actually run: "a detector you have to remember to ask").
//
// mg-75f9 ruled the shape, and this package implements it, it does not re-open
// it:
//
//   - The refinery `[deploy]` mechanism is PREVENTION — it deploys at merge time
//     so drift never opens. It does not cover every path. This runner is the
//     backstop for the four paths prevention misses:
//     (a) merge.go's probeAlreadyMerged early-return resolves as merged but
//     SKIPS deploy — a crash/restart between push and deploy leaves the
//     repo undeployed;
//     (b) a deploy_command that fails silently (DeployError set but unread);
//     (c) a service that dies AFTER a successful deploy;
//     (d) any repo not (yet) enrolled, or a change that lands with no refinery
//     merge.
//
//   - The runner is pogod's heartbeat OnTick loop (see internal/heartbeat), NOT
//     a launchd timer. The nondemand-spawn wedge on this box (mg-50e0) means a
//     launchd timer would silently never fire — the exact "inert while appearing
//     correct" failure this detector exists to catch. The heartbeat already
//     ticks ~30s and already drives the reaper and stall-nudger; the runner
//     piggybacks on it and throttles itself to a COARSE interval so it does not
//     sample every tick.
//
//   - It is REPORT-ONLY. It runs internal/reconcile.CheckDrift (which never
//     mutates) and, on drift, mails `human`. It NEVER reconciles: this package
//     holds no KickFunc and never calls reconcile.Reconcile — a reconcile loop
//     fighting a genuinely-broken artifact is the unbounded-reaper failure shape
//     that the reconcile package's own doc warns against directly. The detector
//     mails; a human (or a deliberate `pogo service reconcile`) acts.
//
// Why pogod rather than a mayor-side check: the same reasoning as the stall
// watcher (internal/stallwatch). A drift detector that lives inside the loop it
// watches drifts with it. pogod's heartbeat is the only cadence guaranteed
// independent of the agents whose deploys it is checking.
package driftwatch

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/reconcile"
)

// mailFrom is the sender stamped on the drift notice. It is not an agent — the
// notice is a system-level alert to a human — so it uses a fixed pogod-side
// identity, mirroring how stall-watch mail is attributed.
const mailFrom = "drift-watch"

// mailTo is the recipient of every drift notice. Drift is a deploy failure a
// human must resolve (report-only — the runner never fixes it), so it goes to
// `human`, whose inbox the apple-side notifier surfaces, NOT to the mayor's
// coordination inbox.
const mailTo = "human"

// MailFunc sends durable mail. pogod injects client.SendMGMail; tests inject a
// recorder. It is the ONLY side-effect channel this package has — there is no
// reconcile/kickstart seam, by design (report-only).
type MailFunc func(to, from, subject, body string) error

// CheckFunc runs the drift detector for one mirror. It defaults to
// reconcile.CheckDrift bound to the injected Deps; tests substitute a fake so
// drift can be produced deterministically and invocations counted (the coarse
// throttle is proven by counting calls). It MUST NOT mutate anything — it is a
// detector, and substituting a mutating implementation would break the
// report-only guarantee this package exists to keep.
type CheckFunc func(reconcile.Mirror) reconcile.Drift

// Emitter writes an event to the shared log. Defaults to events.Emit; tests
// substitute a recorder.
type Emitter func(events.Event)

// Options carries the watcher's dependencies so the package stays testable
// without launchctl, a real process table, or a live mailer.
type Options struct {
	// Mirrors is the set of host artifacts to check, already parsed from
	// [reconcile] mirrors and converted to reconcile.Mirror. Empty means the
	// runner is a no-op (nothing to watch).
	Mirrors []reconcile.Mirror
	// NewDeps builds a FRESH reconcile.Deps for each sample. Production passes
	// reconcile.HostDeps — and it MUST be the factory, not a pre-built Deps:
	// HostDeps carries a per-label launchctl cache that dedups the two
	// running-reality lookups WITHIN one sample. Reusing one Deps across samples
	// would freeze that cache after the first sample and the runner would never
	// see drift open or close again. Ignored when Check is set. When both NewDeps
	// and Check are nil, Deps is used as a static fallback (stateless-Deps tests).
	NewDeps func() reconcile.Deps
	// Deps is a static fallback used only when both NewDeps and Check are nil.
	// Suitable for a stateless Deps (e.g. file-drift-only tests); production uses
	// NewDeps so each sample gets a fresh launchctl cache.
	Deps reconcile.Deps
	// Check overrides the per-mirror detector. nil derives one from NewDeps/Deps.
	// Tests set this to inject drift and count runs.
	Check CheckFunc
	// Mail delivers the drift notice. Required — a runner that cannot report is
	// pointless.
	Mail MailFunc
	// Emit writes the drift_watch_fired event. Defaults to events.Emit.
	Emit Emitter
}

// Watcher samples the reconcile mirrors on a coarse interval and mails `human`
// on drift. Report-only: it never reconciles.
type Watcher struct {
	enabled  bool
	interval time.Duration
	mirrors  []reconcile.Mirror
	check    CheckFunc
	mail     MailFunc
	emit     Emitter

	mu sync.Mutex
	// lastRun is the wall-clock time of the most recent sample. The coarse
	// throttle gates on now.Sub(lastRun) >= interval, so the runner samples at
	// most once per interval no matter how often the ~30s heartbeat ticks.
	lastRun time.Time
	// ran records whether a sample has ever happened, so a zero lastRun does not
	// have to double as "never ran" (a legitimate now could be near the zero
	// time in a test clock).
	ran bool
}

// New builds a Watcher from cfg and opts, applying defaults for a zero interval
// or unset emitter so a zero-value cfg is still usable.
func New(cfg config.DriftWatchConfig, opts Options) *Watcher {
	interval := cfg.Interval
	if interval <= 0 {
		interval = config.DefaultDriftCheckInterval
	}

	check := opts.Check
	if check == nil {
		newDeps := opts.NewDeps
		if newDeps == nil {
			// Static fallback: a fixed Deps rebuilt into a trivial factory so the
			// sample path is uniform. Fine for a stateless Deps (tests); production
			// always passes NewDeps so each sample gets a fresh launchctl cache.
			staticDeps := opts.Deps
			newDeps = func() reconcile.Deps { return staticDeps }
		}
		check = func(m reconcile.Mirror) reconcile.Drift {
			return reconcile.CheckDrift(m, newDeps())
		}
	}

	emit := opts.Emit
	if emit == nil {
		emit = func(e events.Event) { events.Emit(context.Background(), e) }
	}

	return &Watcher{
		enabled:  cfg.Enabled,
		interval: interval,
		mirrors:  opts.Mirrors,
		check:    check,
		mail:     opts.Mail,
		emit:     emit,
	}
}

// Check runs one drift sample for the given wall-clock time, subject to the
// coarse throttle. It is the integration point for the heartbeat OnTick
// callback: the heartbeat ticks every ~30s, and Check is a no-op on all but the
// first tick of each interval.
//
// It is a no-op when the watcher is disabled, has no mirrors, or has no mailer.
// Safe to call concurrently with itself, though pogod only ever calls it from a
// goroutine spawned per heartbeat tick.
func (w *Watcher) Check(now time.Time) {
	if w == nil || !w.enabled || len(w.mirrors) == 0 || w.mail == nil {
		return
	}
	if !w.due(now) {
		return
	}
	w.sample(now)
}

// due reports whether the coarse interval has elapsed since the last sample and,
// if so, records now as the new sample time. Recording here — before the sample
// runs — means a slow or failing sample still consumes its slot, so a persistent
// drift produces one mail per interval, never one per tick. This is the throttle
// the acceptance criteria require: it must NOT run check-drift every 30s tick.
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

// sample runs CheckDrift over every mirror and, if any drifted, mails `human`
// and emits the drift_watch_fired event. It NEVER reconciles.
func (w *Watcher) sample(now time.Time) {
	var drifted []reconcile.Drift
	for _, m := range w.mirrors {
		d := w.check(m)
		if !d.Clean() {
			drifted = append(drifted, d)
		}
	}
	if len(drifted) == 0 {
		return
	}

	// Stable order so the mail body and event details do not shuffle between
	// samples of the same drift.
	sort.Slice(drifted, func(i, j int) bool { return drifted[i].Name < drifted[j].Name })

	names := make([]string, len(drifted))
	var body strings.Builder
	fmt.Fprintf(&body,
		"pogod drift-check found %d host artifact(s) that no longer match the repo — what is RUNNING is not what the repo says.\n\n"+
			"This is the detection backstop for the deploy paths the refinery [deploy] prevention misses (mg-75f9): a merge that skipped deploy, a deploy that failed silently, a service that died after deploy, or an un-enrolled repo.\n\n",
		len(drifted))
	for i, d := range drifted {
		names[i] = d.Name
		body.WriteString(d.Report())
	}
	body.WriteString(
		"\nThis is REPORT-ONLY — pogod did NOT reconcile. Inspect, then fix deliberately with:\n" +
			"  pogo service check-drift   # re-confirm\n" +
			"  pogo service reconcile     # apply the fix (copies source over target, restarts the job)\n")

	subject := fmt.Sprintf("deploy drift: %d host artifact(s) drifted (%s)", len(drifted), strings.Join(names, ", "))

	mailErr := w.mail(mailTo, mailFrom, subject, body.String())

	details := map[string]any{
		"drift_count":  len(drifted),
		"mirror_names": names,
		"interval":     w.interval.String(),
	}
	if mailErr != nil {
		// The drift was detected but could not be reported. Record the failure
		// so the event log shows drift was seen even when the mail channel was
		// down — a drift notice that reaches nobody is the failure this runner
		// exists to prevent.
		details["mail_error"] = mailErr.Error()
	}
	w.emit(events.Event{
		EventType: "drift_watch_fired",
		Agent:     "pogod",
		Details:   details,
	})
}
