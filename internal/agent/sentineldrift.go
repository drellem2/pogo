package agent

import (
	"context"
	"fmt"
	"log"
	"os/exec"
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
// is invoked outside the lock (it does I/O: an event append and a mail send).
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
	d.mu.Unlock()

	if fire != nil {
		d.alert(*fire)
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
