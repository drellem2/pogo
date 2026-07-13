package agent

import (
	"testing"
	"time"
)

// newTestDetector returns a detector with a controllable clock and a capturing
// alert sink, so drift evaluation is exercised without touching the event log
// or shelling out to mg.
func newTestDetector() (*driftDetector, *[]DriftAlert, *time.Time) {
	var fired []DriftAlert
	clock := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	d := &driftDetector{
		window:     time.Hour,
		minSamples: 4,
		threshold:  0.5,
		cooldown:   time.Hour,
		now:        func() time.Time { return clock },
		alert:      func(a DriftAlert) { fired = append(fired, a) },
		samples:    map[string][]readyOutcome{},
		lastAlert:  map[string]time.Time{},
	}
	return d, &fired, &clock
}

func recordN(d *driftDetector, key string, n int, missed bool) {
	for i := 0; i < n; i++ {
		d.record(key, missed, driftMeta{Provider: "claude", Gate: "initial-nudge", Sentinel: "s"})
	}
}

func TestDriftBelowMinSamplesNeverAlerts(t *testing.T) {
	d, fired, _ := newTestDetector()
	// Three straight misses: 100% rate, but below the 4-sample floor.
	recordN(d, "k", 3, true)
	if len(*fired) != 0 {
		t.Fatalf("alerted below minSamples: %+v", *fired)
	}
}

func TestDriftFiresOnceAtThreshold(t *testing.T) {
	d, fired, _ := newTestDetector()
	recordN(d, "k", 4, true) // 4/4 misses, ≥ threshold, ≥ minSamples
	if len(*fired) != 1 {
		t.Fatalf("want exactly 1 alert, got %d: %+v", len(*fired), *fired)
	}
	a := (*fired)[0]
	if a.Missed != 4 || a.Total != 4 || a.Fraction != 1.0 {
		t.Fatalf("unexpected alert payload: %+v", a)
	}
	// Further misses inside the cooldown must not re-alert (dedup / rate-limit).
	recordN(d, "k", 4, true)
	if len(*fired) != 1 {
		t.Fatalf("re-alerted within cooldown: %d alerts", len(*fired))
	}
}

func TestDriftBelowThresholdNoAlert(t *testing.T) {
	d, fired, _ := newTestDetector()
	// 2 misses out of 6 = 33%, below the 50% threshold.
	recordN(d, "k", 2, true)
	recordN(d, "k", 4, false)
	if len(*fired) != 0 {
		t.Fatalf("alerted below threshold: %+v", *fired)
	}
}

func TestDriftEvaluatedOnlyOnMiss(t *testing.T) {
	d, fired, _ := newTestDetector()
	// Prime three misses (still below minSamples), then a SEEN outcome pushes
	// the total to 4. Since the crossing check runs only on a miss, the seen
	// record must not trigger an alert even though 3/4 ≥ threshold.
	recordN(d, "k", 3, true)
	recordN(d, "k", 1, false)
	if len(*fired) != 0 {
		t.Fatalf("alerted on a seen outcome: %+v", *fired)
	}
	// The very next miss (4/5 = 80%) is what should trip it.
	recordN(d, "k", 1, true)
	if len(*fired) != 1 {
		t.Fatalf("want 1 alert after the tripping miss, got %d", len(*fired))
	}
}

func TestDriftWindowPrunesOldMisses(t *testing.T) {
	d, fired, clock := newTestDetector()
	// Four misses, then advance past the window before the fifth sample so the
	// old misses fall out. A later run of seen outcomes should never alert.
	recordN(d, "k", 3, true) // below minSamples: no alert yet
	if len(*fired) != 0 {
		t.Fatalf("premature alert: %+v", *fired)
	}
	*clock = clock.Add(2 * time.Hour) // everything above is now out of window
	recordN(d, "k", 4, false)         // 0/4 misses in-window
	if len(*fired) != 0 {
		t.Fatalf("alerted after old misses aged out: %+v", *fired)
	}
}

func TestDriftCooldownExpiryReArms(t *testing.T) {
	d, fired, clock := newTestDetector()
	recordN(d, "k", 4, true)
	if len(*fired) != 1 {
		t.Fatalf("want first alert, got %d", len(*fired))
	}
	// Advance past the cooldown AND the window, then drift again from scratch.
	*clock = clock.Add(2 * time.Hour)
	recordN(d, "k", 4, true)
	if len(*fired) != 2 {
		t.Fatalf("cooldown did not re-arm: got %d alerts", len(*fired))
	}
}

func TestDriftKeysAreIndependent(t *testing.T) {
	d, fired, _ := newTestDetector()
	// One key drifts; a second key with clean spawns must not be alerted, and
	// the drifting key's samples must not dilute the clean one.
	recordN(d, "claude/initial-nudge", 4, true)
	recordN(d, "cursor/trust-dialog", 6, false)
	if len(*fired) != 1 {
		t.Fatalf("want 1 alert (only the drifting key), got %d: %+v", len(*fired), *fired)
	}
	if (*fired)[0].Gate != "initial-nudge" {
		t.Fatalf("alert fired for wrong key: %+v", (*fired)[0])
	}
}

func TestRecordHelpersMapSeenToMiss(t *testing.T) {
	// Guard the seen→missed inversion at the public entry points, using a
	// scoped detector swapped in for the process-global one.
	var fired []DriftAlert
	clock := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	prev := readyDrift
	readyDrift = &driftDetector{
		window: time.Hour, minSamples: 4, threshold: 0.5, cooldown: time.Hour,
		now:       func() time.Time { return clock },
		alert:     func(a DriftAlert) { fired = append(fired, a) },
		samples:   map[string][]readyOutcome{},
		lastAlert: map[string]time.Time{},
	}
	defer func() { readyDrift = prev }()

	// seen=false is a miss; four of them trips the initial-nudge key.
	for i := 0; i < 4; i++ {
		RecordInitialNudgeReady("claude", "? for shortcuts", false)
	}
	if len(fired) != 1 || fired[0].Provider != "claude" || fired[0].Gate != "initial-nudge" {
		t.Fatalf("RecordInitialNudgeReady did not fire as expected: %+v", fired)
	}

	// confirmed=false is a miss on the trust-dialog key.
	for i := 0; i < 4; i++ {
		RecordTrustDialogReady("cursor", "Plan, search, build anything", false)
	}
	if len(fired) != 2 || fired[1].Gate != "trust-dialog" {
		t.Fatalf("RecordTrustDialogReady did not fire as expected: %+v", fired)
	}
}

func TestRecordHelpersDefaultEmptyProvider(t *testing.T) {
	var fired []DriftAlert
	clock := time.Date(2026, 7, 13, 12, 0, 0, 0, time.UTC)
	prev := readyDrift
	readyDrift = &driftDetector{
		window: time.Hour, minSamples: 4, threshold: 0.5, cooldown: time.Hour,
		now:       func() time.Time { return clock },
		alert:     func(a DriftAlert) { fired = append(fired, a) },
		samples:   map[string][]readyOutcome{},
		lastAlert: map[string]time.Time{},
	}
	defer func() { readyDrift = prev }()

	for i := 0; i < 4; i++ {
		RecordInitialNudgeReady("", "sentinel", false)
	}
	if len(fired) != 1 || fired[0].Provider != "default" {
		t.Fatalf("empty provider not defaulted: %+v", fired)
	}
}
