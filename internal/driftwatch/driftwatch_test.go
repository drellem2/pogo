package driftwatch

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/heartbeat"
	"github.com/drellem2/pogo/internal/reconcile"
)

// sentMail records one mail delivery for assertions.
type sentMail struct {
	to, from, subject, body string
}

// recorder collects mail and events from a Watcher run.
type recorder struct {
	mu     sync.Mutex
	mails  []sentMail
	events []events.Event
	// mailErr, when set, is returned by the injected MailFunc so the
	// mail-failure path can be exercised.
	mailErr error
}

func (r *recorder) mail(to, from, subject, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.mails = append(r.mails, sentMail{to, from, subject, body})
	return r.mailErr
}

func (r *recorder) emit(e events.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

func (r *recorder) mailCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.mails)
}

// driftFor builds a CheckFunc that reports the named mirror as drifted (a file
// MODIFIED drift) and counts how many times it is invoked, so the coarse
// throttle can be proven by counting samples.
func driftFor(drifted map[string]bool, calls *int) (CheckFunc, *sync.Mutex) {
	var mu sync.Mutex
	return func(m reconcile.Mirror) reconcile.Drift {
		mu.Lock()
		*calls++
		mu.Unlock()
		d := reconcile.Drift{Name: m.Name, Target: m.Target, Label: m.Label}
		if drifted[m.Name] {
			d.FileDrift = "MODIFIED: " + m.Target + " differs from source " + m.Source
		}
		return d
	}, &mu
}

func baseCfg() config.DriftWatchConfig {
	return config.DriftWatchConfig{Enabled: true, Interval: 15 * time.Minute}
}

// TestDetectsDriftAndMailsHuman is the positive control mg-5701's own residual
// asked for: prove the runner FIRES on real drift, not merely that it is quiet
// when clean. It constructs drift and asserts the runner mails `human` naming
// the drifted repo and emits the drift_watch_fired event.
func TestDetectsDriftAndMailsHuman(t *testing.T) {
	rec := &recorder{}
	calls := 0
	check, _ := driftFor(map[string]bool{"pogod": true}, &calls)

	w := New(baseCfg(), Options{
		Mirrors: []reconcile.Mirror{
			{Name: "pogod", Source: "/src/pogod", Target: "/host/pogod", Label: "com.pogo.pogod"},
		},
		Check: check,
		Mail:  rec.mail,
		Emit:  rec.emit,
	})

	w.Check(time.Now())

	if rec.mailCount() != 1 {
		t.Fatalf("expected exactly 1 mail on drift, got %d", rec.mailCount())
	}
	m := rec.mails[0]
	if m.to != "human" {
		t.Errorf("drift mail must go to human, went to %q", m.to)
	}
	if m.from != mailFrom {
		t.Errorf("drift mail from = %q, want %q", m.from, mailFrom)
	}
	if !strings.Contains(m.subject, "pogod") {
		t.Errorf("subject must name the drifted mirror, got %q", m.subject)
	}
	if !strings.Contains(m.body, "/host/pogod") {
		t.Errorf("body must name the drifted target, got:\n%s", m.body)
	}
	if len(rec.events) != 1 || rec.events[0].EventType != "drift_watch_fired" {
		t.Fatalf("expected one drift_watch_fired event, got %+v", rec.events)
	}
	if got := rec.events[0].Details["drift_count"]; got != 1 {
		t.Errorf("event drift_count = %v, want 1", got)
	}
}

// TestNoMailWhenClean confirms the runner is silent when nothing drifted — it
// still SAMPLES every mirror, but a clean sample produces no mail and no event.
func TestNoMailWhenClean(t *testing.T) {
	rec := &recorder{}
	calls := 0
	check, _ := driftFor(map[string]bool{}, &calls) // nothing drifted

	w := New(baseCfg(), Options{
		Mirrors: []reconcile.Mirror{{Name: "pogod", Source: "/src", Target: "/host"}},
		Check:   check,
		Mail:    rec.mail,
		Emit:    rec.emit,
	})

	w.Check(time.Now())

	if calls != 1 {
		t.Errorf("expected the mirror to be sampled once, got %d", calls)
	}
	if rec.mailCount() != 0 {
		t.Errorf("clean host must not mail, got %d mail(s)", rec.mailCount())
	}
	if len(rec.events) != 0 {
		t.Errorf("clean host must not emit a fire event, got %+v", rec.events)
	}
}

// TestCoarseThrottle proves the acceptance requirement that the runner does NOT
// sample on every ~30s heartbeat tick: within one interval, only the first tick
// samples; a tick past the interval samples again.
func TestCoarseThrottle(t *testing.T) {
	rec := &recorder{}
	calls := 0
	check, _ := driftFor(map[string]bool{}, &calls)

	interval := 15 * time.Minute
	w := New(config.DriftWatchConfig{Enabled: true, Interval: interval}, Options{
		Mirrors: []reconcile.Mirror{{Name: "pogod", Source: "/src", Target: "/host"}},
		Check:   check,
		Mail:    rec.mail,
		Emit:    rec.emit,
	})

	start := time.Now()
	// Simulate ~30 heartbeat ticks over 15 minutes (one every 30s). Only the
	// very first should sample; the rest are inside the coarse interval.
	for i := 0; i < 30; i++ {
		w.Check(start.Add(time.Duration(i) * 30 * time.Second))
	}
	if calls != 1 {
		t.Fatalf("coarse throttle broken: %d samples across one interval of 30s ticks, want 1", calls)
	}

	// A tick just past the interval boundary samples again.
	w.Check(start.Add(interval + time.Second))
	if calls != 2 {
		t.Fatalf("expected a second sample past the interval, got %d total", calls)
	}

	// And the ticks right after that second sample are throttled again.
	w.Check(start.Add(interval + 31*time.Second))
	if calls != 2 {
		t.Fatalf("throttle did not re-arm after the second sample, got %d", calls)
	}
}

// TestReportOnlyDoesNotReconcile proves the runner never mutates the host: on
// real file drift (a target whose bytes differ from its source) it mails but
// LEAVES THE TARGET FILE UNCHANGED. A reconcile would have overwritten the
// target with the source; report-only must not. This exercises the real
// reconcile.CheckDrift (no Check override) so the guarantee is proven against
// the production detector, not a fake.
func TestReportOnlyDoesNotReconcile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "source.sh")
	target := filepath.Join(dir, "target.sh")
	if err := os.WriteFile(source, []byte("NEW deployed bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("OLD running bytes\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	rec := &recorder{}
	// No Check override — use the real reconcile.CheckDrift via Deps. No label,
	// so only the file-drift dimension is exercised (no launchctl needed).
	w := New(baseCfg(), Options{
		Mirrors: []reconcile.Mirror{{Name: "watchdog", Source: source, Target: target}},
		Deps:    reconcile.Deps{},
		Mail:    rec.mail,
		Emit:    rec.emit,
	})

	w.Check(time.Now())

	// It must have detected and reported the file drift.
	if rec.mailCount() != 1 {
		t.Fatalf("expected 1 drift mail from real file drift, got %d", rec.mailCount())
	}

	// The critical report-only assertion: the target is UNTOUCHED. A reconcile
	// would have made it equal to the source.
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "OLD running bytes\n" {
		t.Fatalf("report-only violated: target was modified to %q — the runner must NEVER reconcile", string(got))
	}
}

// TestMailFailureStillEmits confirms a drift that could not be reported (mail
// send failed) is still recorded in the event log, so drift-was-seen is never
// lost to a down mail channel.
func TestMailFailureStillEmits(t *testing.T) {
	rec := &recorder{mailErr: errFake}
	calls := 0
	check, _ := driftFor(map[string]bool{"pogod": true}, &calls)

	w := New(baseCfg(), Options{
		Mirrors: []reconcile.Mirror{{Name: "pogod", Source: "/s", Target: "/t"}},
		Check:   check,
		Mail:    rec.mail,
		Emit:    rec.emit,
	})

	w.Check(time.Now())

	if len(rec.events) != 1 {
		t.Fatalf("expected the fire event even when mail failed, got %d", len(rec.events))
	}
	if _, ok := rec.events[0].Details["mail_error"]; !ok {
		t.Errorf("event must record mail_error when delivery failed, details=%+v", rec.events[0].Details)
	}
}

// TestDisabledAndEmptyAreNoOps confirms the two off-paths never sample or mail.
func TestDisabledAndEmptyAreNoOps(t *testing.T) {
	t.Run("disabled", func(t *testing.T) {
		rec := &recorder{}
		calls := 0
		check, _ := driftFor(map[string]bool{"pogod": true}, &calls)
		w := New(config.DriftWatchConfig{Enabled: false, Interval: time.Minute}, Options{
			Mirrors: []reconcile.Mirror{{Name: "pogod", Source: "/s", Target: "/t"}},
			Check:   check, Mail: rec.mail, Emit: rec.emit,
		})
		w.Check(time.Now())
		if calls != 0 || rec.mailCount() != 0 {
			t.Errorf("disabled watcher must be inert: calls=%d mails=%d", calls, rec.mailCount())
		}
	})
	t.Run("no mirrors", func(t *testing.T) {
		rec := &recorder{}
		calls := 0
		check, _ := driftFor(map[string]bool{}, &calls)
		w := New(baseCfg(), Options{Mirrors: nil, Check: check, Mail: rec.mail, Emit: rec.emit})
		w.Check(time.Now())
		if calls != 0 || rec.mailCount() != 0 {
			t.Errorf("no-mirror watcher must be inert: calls=%d mails=%d", calls, rec.mailCount())
		}
	})
}

// TestNewDepsCalledFreshPerSample guards the stale-cache regression: production
// passes reconcile.HostDeps (a FACTORY) because HostDeps carries a per-sample
// launchctl cache. If the runner built Deps once and reused it, drift could never
// open or close after the first sample. This asserts the factory is invoked on
// every sample (not memoized), and that drift newly appearing on the second
// sample is actually seen.
func TestNewDepsCalledFreshPerSample(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "src.sh")
	target := filepath.Join(dir, "tgt.sh")
	if err := os.WriteFile(source, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("same\n"), 0o644); err != nil {
		t.Fatal(err) // clean to start — no file drift
	}

	rec := &recorder{}
	depsCalls := 0
	w := New(config.DriftWatchConfig{Enabled: true, Interval: time.Minute}, Options{
		Mirrors: []reconcile.Mirror{{Name: "m", Source: source, Target: target}},
		NewDeps: func() reconcile.Deps {
			depsCalls++
			return reconcile.Deps{}
		},
		Mail: rec.mail,
		Emit: rec.emit,
	})

	// Sample 1: clean, so no mail. The factory must have been consulted.
	base := time.Now()
	w.Check(base)
	if depsCalls == 0 {
		t.Fatal("NewDeps was never called — the runner is not building deps per sample")
	}
	if rec.mailCount() != 0 {
		t.Fatalf("clean first sample must not mail, got %d", rec.mailCount())
	}
	firstCalls := depsCalls

	// Introduce drift, then sample again past the interval. A frozen deps/cache
	// would still read the old world; a fresh read sees the new file drift.
	if err := os.WriteFile(target, []byte("DRIFTED\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	w.Check(base.Add(2 * time.Minute))
	if depsCalls <= firstCalls {
		t.Fatalf("NewDeps not re-invoked on the second sample (%d then %d) — deps are memoized", firstCalls, depsCalls)
	}
	if rec.mailCount() != 1 {
		t.Fatalf("drift introduced between samples must be detected on the next sample, got %d mail(s)", rec.mailCount())
	}
}

// TestIntervalDefaultApplied confirms a zero interval falls back to the coarse
// default rather than degenerating into every-tick sampling.
func TestIntervalDefaultApplied(t *testing.T) {
	w := New(config.DriftWatchConfig{Enabled: true}, Options{
		Mirrors: []reconcile.Mirror{{Name: "x", Source: "/s", Target: "/t"}},
		Mail:    func(_, _, _, _ string) error { return nil },
	})
	if w.interval != config.DefaultDriftCheckInterval {
		t.Errorf("zero interval must default to %s, got %s", config.DefaultDriftCheckInterval, w.interval)
	}
}

// hbClock is a heartbeat.Clock whose wall/monotonic readings advance only via
// advance(), so the integration test drives the real heartbeat loop
// deterministically (no wall-clock sleeps).
type hbClock struct {
	mu   sync.Mutex
	wall time.Time
	mono time.Duration
}

func (c *hbClock) Wall() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.wall
}

func (c *hbClock) Mono() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.mono
}

func (c *hbClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.wall = c.wall.Add(d)
	c.mono += d
}

// TestHeartbeatOnTickDrivesRunner is the integration proof mg-345b's acceptance
// requires: wired as a heartbeat OnTick callback (exactly as cmd/pogod wires it),
// the runner FIRES on drift and mails `human` — and honors the coarse throttle
// across the flurry of ~30s ticks, sampling once per interval, not once per
// tick. This exercises the real internal/heartbeat.Detector.Tick loop, not a
// hand-rolled stand-in.
func TestHeartbeatOnTickDrivesRunner(t *testing.T) {
	rec := &recorder{}
	calls := 0
	check, _ := driftFor(map[string]bool{"pogod": true}, &calls)

	interval := 15 * time.Minute
	w := New(config.DriftWatchConfig{Enabled: true, Interval: interval}, Options{
		Mirrors: []reconcile.Mirror{{Name: "pogod", Source: "/src/pogod", Target: "/host/pogod"}},
		Check:   check,
		Mail:    rec.mail,
		Emit:    rec.emit,
	})

	clk := &hbClock{wall: time.Date(2026, 7, 17, 12, 0, 0, 0, time.UTC)}
	det := &heartbeat.Detector{
		Interval: 30 * time.Second,
		Clock:    clk,
		// Emit is required for a usable detector; drift events go through the
		// runner's own emitter, so this can discard.
		Emitter: func(events.Event) {},
		// Wire the runner EXACTLY as cmd/pogod does — OnTick calls Check. Called
		// synchronously here (production spawns a goroutine) so the test observes
		// each tick's effect deterministically.
		OnTick: func(now time.Time) { w.Check(now) },
	}

	// Seed tick: heartbeat's first Tick sets the baseline and does NOT invoke
	// OnTick, so no sample yet.
	det.Tick()
	if calls != 0 {
		t.Fatalf("seed tick must not sample, got %d", calls)
	}

	// 30 ticks at 30s each = 15 minutes of heartbeats. The first OnTick sample
	// fires and mails; the rest fall inside the coarse interval.
	for i := 0; i < 30; i++ {
		clk.advance(30 * time.Second)
		det.Tick()
	}
	if calls != 1 {
		t.Fatalf("coarse throttle broken under real heartbeat: %d samples across 15m of 30s ticks, want 1", calls)
	}
	if rec.mailCount() != 1 {
		t.Fatalf("OnTick runner must mail human once on drift, got %d", rec.mailCount())
	}
	if rec.mails[0].to != "human" {
		t.Errorf("OnTick drift mail went to %q, want human", rec.mails[0].to)
	}
	if !strings.Contains(rec.mails[0].subject, "pogod") {
		t.Errorf("OnTick drift mail must name the drifted repo, subject=%q", rec.mails[0].subject)
	}

	// Cross the interval boundary: the next tick samples again (persistent drift
	// re-reports once per coarse interval — report-only never silences it).
	clk.advance(30 * time.Second)
	det.Tick()
	if calls != 2 {
		t.Fatalf("expected a second sample after the interval elapsed, got %d", calls)
	}
	if rec.mailCount() != 2 {
		t.Fatalf("persistent drift must re-mail once per interval, got %d mails", rec.mailCount())
	}
}

var errFake = fakeErr("mail is down")

type fakeErr string

func (e fakeErr) Error() string { return string(e) }
