package agent

import (
	"context"
	"fmt"
	"log"
	"os/exec"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// Fleet-wide, in-process detection for prompt-ready sentinel drift (mg-ce4c,
// fast-follow to pogo#76 / PR #77).
//
// The initial-nudge ready gate (nudge.go) and Cursor's trust-dialog hook
// (cursor/trust_hook.go) both key off hardcoded UI-string sentinels scraped
// from harness PTY output. When a harness UI change makes a sentinel stop
// matching, the gate/hook silently degrades: the Claude nudge path burns the
// full InitialNudgeTimeout as a per-spawn ~60s cold-start tax, and Cursor's
// hook loses the readiness confirmation that guards its trust-dialog dismissal.
// pogo#76 was invisible across the WHOLE fleet because the only signal was a
// per-spawn log line — and nobody reads our logs (the watchdog was dead 4.8h,
// recovery inert 6 weeks, both nominally "observable"). A guard test catches
// drift against the PINNED harness in CI; it does NOT catch drift on the
// INSTALLED harness in production, which is exactly where #76 bit.
//
// This detector converts that per-spawn silence into a LOUD aggregate signal.
// pogod is the single process that spawns the entire fleet, so an in-process
// sliding window over ready-gate outcomes IS the fleet-wide aggregate: a single
// missed sentinel is noise, but when the fraction of spawns MISSING their
// sentinel within a rolling window crosses a threshold, the sentinel is stale
// again. On that crossing it (1) mails the coordinator — a place a human/mayor
// actually looks, not launchd's log — and (2) emits a distinctive
// sentinel_drift event on the durable spine so mg and the digest can surface
// it too. The alert is rate-limited per sentinel so it fires once per drift
// episode, not once per spawn.
//
// A pogod restart resets the window. Drift is persistent (every spawn misses),
// so it re-accumulates within a handful of spawns — an acceptable trade for
// zero on-disk state and immunity to the events.log aggregate-contamination
// caveat documented in docs/event-log.md (test runs historically polluted
// counts; this detector never reads the log back, so it cannot be fooled by
// them).
//
// The detector is generalized across BOTH sentinels per the mg-ff2c
// subsumption: RecordInitialNudgeReady feeds the Claude nudge gate and
// RecordTrustDialogReady feeds Cursor's trust-dialog hook, each under its own
// keyed window so one provider's drift can't dilute or mask the other's.

const (
	// driftWindow is the rolling window over which the miss rate is computed.
	// An hour is long enough that a low fleet spawn rate still accumulates a
	// meaningful sample, and short enough that a fixed drift crosses the
	// threshold soon after it starts.
	driftWindow = time.Hour

	// driftMinSamples is the minimum number of spawns that must fall in the
	// window before a rate is trusted. Below this a single stale spawn would
	// read as 100% and alert on noise.
	driftMinSamples = 4

	// driftThreshold is the miss fraction that means "the sentinel is stale
	// again". Half of windowed spawns missing is well clear of the incidental
	// single-spawn miss (a genuinely slow cold start) and squarely in
	// systematic-drift territory — #76 was 12/12.
	driftThreshold = 0.5

	// driftAlertCooldown deduplicates the alert: once a sentinel has fired, it
	// stays quiet for this long even as further misses accrue, so a persistent
	// drift produces one alert per episode rather than one per spawn.
	driftAlertCooldown = time.Hour
)

// DriftAlert is the payload handed to the alert sink when a sentinel's windowed
// miss rate crosses the threshold. It names the likely-stale sentinel and the
// gate it feeds so the recipient can go straight to the provider descriptor.
type DriftAlert struct {
	Provider string        // harness provider id: "claude", "cursor", …
	Gate     string        // which gate degraded: "initial-nudge", "trust-dialog"
	Sentinel string        // the primary sentinel string that is probably stale
	Missed   int           // spawns in the window that missed the sentinel
	Total    int           // spawns in the window
	Fraction float64       // Missed / Total
	Window   time.Duration // the window the rate was computed over
}

// driftMeta carries the labels used only when an alert actually fires, so the
// hot path (record) can pass them unconditionally without allocating a message.
type driftMeta struct {
	Provider string
	Gate     string
	Sentinel string
}

type readyOutcome struct {
	at     time.Time
	missed bool
}

// driftDetector holds a per-key sliding window of ready-gate outcomes and fires
// an alert when a key's windowed miss rate crosses the threshold. It is safe
// for concurrent use: pogod records outcomes from every spawn goroutine.
type driftDetector struct {
	mu         sync.Mutex
	window     time.Duration
	minSamples int
	threshold  float64
	cooldown   time.Duration
	now        func() time.Time
	alert      func(DriftAlert)
	samples    map[string][]readyOutcome
	lastAlert  map[string]time.Time
}

func newDriftDetector() *driftDetector {
	return &driftDetector{
		window:     driftWindow,
		minSamples: driftMinSamples,
		threshold:  driftThreshold,
		cooldown:   driftAlertCooldown,
		now:        time.Now,
		alert:      defaultDriftAlert,
		samples:    map[string][]readyOutcome{},
		lastAlert:  map[string]time.Time{},
	}
}

// record adds one ready-gate outcome under key and, on a miss, evaluates
// whether the windowed miss rate has crossed the threshold. If it has — and the
// key is not within its dedup cooldown — it fires meta's alert. The alert sink
// is invoked outside the lock (it does I/O: an event append and a mail send),
// but is READ under it, so StubDriftSinkForTesting's swap cannot race a
// concurrent recorder (see the note there).
func (d *driftDetector) record(key string, missed bool, meta driftMeta) {
	now := d.now()

	d.mu.Lock()
	cutoff := now.Add(-d.window)
	s := append(d.samples[key], readyOutcome{at: now, missed: missed})
	// Drop samples that fell out of the window. Appends happen in time order,
	// so the stale prefix is contiguous at the front.
	drop := 0
	for drop < len(s) && s[drop].at.Before(cutoff) {
		drop++
	}
	s = s[drop:]
	d.samples[key] = s

	var fire *DriftAlert
	if missed && len(s) >= d.minSamples {
		misses := 0
		for _, o := range s {
			if o.missed {
				misses++
			}
		}
		frac := float64(misses) / float64(len(s))
		if frac >= d.threshold {
			last, seen := d.lastAlert[key]
			if !seen || now.Sub(last) >= d.cooldown {
				d.lastAlert[key] = now
				fire = &DriftAlert{
					Provider: meta.Provider,
					Gate:     meta.Gate,
					Sentinel: meta.Sentinel,
					Missed:   misses,
					Total:    len(s),
					Fraction: frac,
					Window:   d.window,
				}
			}
		}
	}
	sink := d.alert
	d.mu.Unlock()

	if fire != nil {
		sink(*fire)
	}
}

// readyDrift is the process-global detector. There is one pogod per host and it
// spawns the whole fleet, so this single instance is the fleet-wide aggregate.
var readyDrift = newDriftDetector()

// RecordInitialNudgeReady records one initial-nudge ready-gate outcome for
// drift detection. seen is whether the prompt-ready sentinel was observed
// before the (best-effort) delivery; provider is the harness provider id and
// sentinel is the primary sentinel string surfaced in an alert as the
// likely-stale marker. A run of misses across the fleet means the sentinel is
// stale and every spawn is paying the full InitialNudgeTimeout as dead time.
func RecordInitialNudgeReady(provider, sentinel string, seen bool) {
	if provider == "" {
		provider = "default"
	}
	readyDrift.record(provider+"/initial-nudge", !seen, driftMeta{
		Provider: provider,
		Gate:     "initial-nudge",
		Sentinel: sentinel,
	})
}

// RecordTrustDialogReady records one Cursor trust-dialog hook outcome for drift
// detection. confirmed is whether the hook resolved via one of its sentinels —
// either it matched the trust-dialog marker and dismissed the dialog, or it saw
// the composer-ready marker proving the dialog was already gone. A false
// confirmed means the hook watched its whole window and matched NEITHER
// sentinel, which is the drift signature: both the dialog marker and the
// composer placeholder are hardcoded UI strings, and a run of these means one
// (or both) has drifted, leaving trust-dialog dismissal unguarded (mg-ff2c).
func RecordTrustDialogReady(provider, sentinel string, confirmed bool) {
	if provider == "" {
		provider = "cursor"
	}
	readyDrift.record(provider+"/trust-dialog", !confirmed, driftMeta{
		Provider: provider,
		Gate:     "trust-dialog",
		Sentinel: sentinel,
	})
}

// defaultDriftAlert is the production alert sink. It emits a durable
// sentinel_drift event and, because nobody reads our logs, mails the
// coordinator so the drift lands somewhere a human or the mayor looks.
func defaultDriftAlert(a DriftAlert) {
	events.Emit(context.Background(), events.Event{
		EventType: "sentinel_drift",
		Agent:     "pogod",
		Details: map[string]any{
			"provider": a.Provider,
			"gate":     a.Gate,
			"sentinel": a.Sentinel,
			"missed":   a.Missed,
			"total":    a.Total,
			"fraction": a.Fraction,
			"window":   a.Window.String(),
		},
	})
	mailDriftAlert(a)
}

// mailDriftAlert sends the LOUD half of the signal: a mail to the coordinator.
// Best-effort — if mg is not on PATH or the inbox does not exist yet, the
// sentinel_drift event has already been emitted and the daemon must not be
// disturbed. Mirrors service.sendInstallMail's shell-out posture.
func mailDriftAlert(a DriftAlert) {
	coordinator := CoordinatorName()
	subject := fmt.Sprintf("[sentinel-drift] %s %s gate missed its sentinel on %d/%d spawns",
		a.Provider, a.Gate, a.Missed, a.Total)
	body := fmt.Sprintf(
		"Fleet-wide prompt-ready sentinel drift detected.\n\n"+
			"Provider:  %s\n"+
			"Gate:      %s\n"+
			"Sentinel:  %q  (probably stale — the harness UI likely changed)\n"+
			"Miss rate: %d/%d spawns (%.0f%%) in the last %s\n\n"+
			"What this means: the %s gate is falling through to its best-effort\n"+
			"path at a fleet-wide rate. For the Claude initial-nudge gate that is a\n"+
			"~60s cold-start tax on every affected spawn (pogo#76); for the Cursor\n"+
			"trust-dialog gate it means dismissal is running unguarded (mg-ff2c).\n\n"+
			"Fix: re-capture the harness's PTY footer and update the sentinel /\n"+
			"alternates in the provider descriptor (internal/agent/provider.go for\n"+
			"Claude, internal/cursor/provider.go for Cursor), same as PR #77.",
		a.Provider, a.Gate, a.Sentinel, a.Missed, a.Total, a.Fraction*100,
		a.Window.String(), a.Gate)

	cmd := exec.Command("mg", "mail", "send", coordinator,
		"--from", "pogod-sentinel",
		"--subject", subject,
		"--body", body)
	if out, err := cmd.CombinedOutput(); err != nil {
		log.Printf("sentinel-drift: mail to %s failed: %v: %s",
			coordinator, err, strings.TrimSpace(string(out)))
	}
}

// StubDriftSinkForTesting replaces the process-global detector's alert sink with
// one that swallows alerts, and returns a func restoring the previous sink.
// Intended for TestMain in any package whose tests drive a REAL ready gate.
//
// Why this exists as an exported seam (mg-54f8). The record helpers above are
// called from production code in internal/cursor and internal/claude, so the
// tests that drive those hook loops — internal/{cursor,claude}/trust_hook_race_test.go
// (mg-8792 / pogo#91) and this package's own nudge-timeout tests — feed real
// misses into the process-global detector. When a key's windowed miss fraction
// crosses driftThreshold, defaultDriftAlert MAILS THE COORDINATOR. Nothing about
// a `go test` run makes that mail wrong-by-construction from the sink's point of
// view; it is a real threshold crossing, and the blast radius is the fleet rather
// than the package.
//
// Before this seam existed the suites were safe only ARITHMETICALLY: the cursor
// trust-dialog key ran one miss against three confirmations (0.25, threshold
// 0.5) and this package's initial-nudge key ran three misses against
// driftMinSamples of 4 — safe by ONE test, and `-count=2` accumulates in the
// same process because the detector is a process-global. One more deadline-arm
// fixture on either side mails the fleet coordinator from a unit test. A stub
// installed before m.Run makes that structurally impossible instead of
// unlikely.
//
// The swap is written under the detector's mutex and record() reads the sink
// under the same mutex, so this cannot become the mg-d578 defect — a test-only
// global written while a watcher goroutine from a previous test is still
// recording. TestMain installs it once before any test runs and restores after
// m.Run, so in practice there is no concurrent write at all; the locking is
// there so a per-test caller is safe too.
//
// Assertions about alert CONTENT belong on a scoped detector, not on this stub —
// see sentineldrift_test.go, which swaps readyDrift wholesale for one with a
// fake clock and a capturing sink.
func StubDriftSinkForTesting() (restore func()) {
	return readyDrift.setAlert(func(a DriftAlert) {
		// Not silent: a suite that crosses the threshold has said something
		// about its own fixtures, and this is the only trace of it. It goes
		// nowhere near the coordinator or the durable spine.
		log.Printf("sentinel-drift: alert suppressed by the test stub "+
			"(provider=%s gate=%s missed=%d/%d)", a.Provider, a.Gate, a.Missed, a.Total)
	})
}

// DriftSinkIsProductionForTesting reports whether the process-global detector
// would run defaultDriftAlert — the sink that emits sentinel_drift and mails the
// coordinator.
//
// This is what lets each provider package assert its own isolation WITHOUT
// tripping the threshold to find out. A control that proved the point by
// recording four misses and checking nothing was mailed would, on the day
// TestMain's install got dropped, send exactly the mail it exists to prevent.
// Asking about the sink costs nothing and is safe when the answer is bad.
//
// The comparison is on code pointers, which reflect documents as not necessarily
// unique — the linker may merge identical functions. That can only ever make this
// over-report (a different-but-identical sink reading as production), never
// under-report: the same top-level func always yields the same pointer, so an
// actually-installed defaultDriftAlert cannot slip past. Over-reporting fails a
// control loudly; under-reporting would be the silent version, and this predicate
// exists to not be silent.
func DriftSinkIsProductionForTesting() bool {
	readyDrift.mu.Lock()
	defer readyDrift.mu.Unlock()
	return reflect.ValueOf(readyDrift.alert).Pointer() ==
		reflect.ValueOf(defaultDriftAlert).Pointer()
}

// setAlert swaps the detector's alert sink and returns a restore func.
func (d *driftDetector) setAlert(sink func(DriftAlert)) (restore func()) {
	d.mu.Lock()
	prev := d.alert
	d.alert = sink
	d.mu.Unlock()

	return func() {
		d.mu.Lock()
		d.alert = prev
		d.mu.Unlock()
	}
}
