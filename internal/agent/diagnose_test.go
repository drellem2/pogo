package agent

import (
	"testing"
	"time"
)

func TestStallThresholdFor(t *testing.T) {
	if got := StallThresholdFor(TypeCrew); got != StallThresholdCrew {
		t.Errorf("StallThresholdFor(crew) = %v, want %v", got, StallThresholdCrew)
	}
	if got := StallThresholdFor(TypePolecat); got != StallThresholdPolecat {
		t.Errorf("StallThresholdFor(polecat) = %v, want %v", got, StallThresholdPolecat)
	}
}

func TestDiagnoseHealthy(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "diag-healthy",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatalf("Spawn: %v", err)
	}

	// Write some output so lastWrite is recent
	a.Nudge("hello")
	time.Sleep(200 * time.Millisecond)

	diag := diagnoseAgent(a)
	if diag.Health != "healthy" {
		t.Errorf("Health = %q, want %q", diag.Health, "healthy")
	}
	if diag.Stalled {
		t.Error("should not be stalled")
	}
	if !diag.ProcessAlive {
		t.Error("process should be alive")
	}
	if diag.LastActivity.IsZero() {
		t.Error("LastActivity should be set after output")
	}
	if diag.Status != StatusRunning {
		t.Errorf("Status = %q, want %q", diag.Status, StatusRunning)
	}
}

func TestDiagnoseExited(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	a, err := reg.Spawn(SpawnRequest{
		Name:    "diag-exited",
		Type:    TypePolecat,
		Command: []string{"true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-a.Done()

	diag := diagnoseAgent(a)
	if diag.Health != "exited" {
		t.Errorf("Health = %q, want %q", diag.Health, "exited")
	}
	if diag.ProcessAlive {
		t.Error("process should not be alive after exit")
	}
}

func TestDiagnoseNoOutput(t *testing.T) {
	// An agent with no output yet should be healthy (not stalled).
	buf := NewRingBuffer(1024)
	a := &Agent{
		Name:      "diag-no-output",
		Type:      TypePolecat,
		PID:       0,
		Status:    StatusRunning,
		StartTime: time.Now(),
		outputBuf: buf,
		done:      make(chan struct{}),
	}
	diag := diagnoseAgent(a)
	if diag.Stalled {
		t.Error("agent with no output should not be stalled")
	}
	if diag.LastActivity != (time.Time{}) {
		t.Error("LastActivity should be zero when no output written")
	}
}

func TestDiagnoseStalled(t *testing.T) {
	// Simulate a stalled agent by setting lastWrite far in the past.
	buf := NewRingBuffer(1024)
	buf.Write([]byte("old data"))
	// Manually override lastWrite to simulate stall
	buf.mu.Lock()
	buf.lastWrite = time.Now().Add(-10 * time.Minute)
	buf.mu.Unlock()

	a := &Agent{
		Name:      "diag-stalled",
		Type:      TypePolecat,
		PID:       0,
		Status:    StatusRunning,
		StartTime: time.Now().Add(-15 * time.Minute),
		outputBuf: buf,
		done:      make(chan struct{}),
	}

	diag := diagnoseAgent(a)
	if !diag.Stalled {
		t.Error("agent should be stalled (idle > 5min threshold)")
	}
	if diag.Health != "stalled" {
		t.Errorf("Health = %q, want %q", diag.Health, "stalled")
	}
}

// A rate-limited agent looks stalled by output-idle but must report the
// distinct "rate_limited" health so operators don't mistake a usage-limit wait
// for a genuine wedge (gh #45). rate_limited outranks stalled.
func TestDiagnoseRateLimited(t *testing.T) {
	buf := NewRingBuffer(1024)
	buf.Write([]byte("old data"))
	buf.mu.Lock()
	buf.lastWrite = time.Now().Add(-10 * time.Minute) // would otherwise be "stalled"
	buf.mu.Unlock()

	a := &Agent{
		Name:      "diag-ratelimited",
		Type:      TypePolecat,
		PID:       0,
		Status:    StatusRunning,
		StartTime: time.Now().Add(-15 * time.Minute),
		outputBuf: buf,
		done:      make(chan struct{}),
	}
	a.SetRateLimited(true)

	diag := diagnoseAgent(a)
	if diag.Health != "rate_limited" {
		t.Errorf("Health = %q, want %q", diag.Health, "rate_limited")
	}
	if !diag.RateLimited {
		t.Error("RateLimited should be true")
	}
	if diag.RateLimitedSince.IsZero() {
		t.Error("RateLimitedSince should be set")
	}

	// Clearing it drops back to the underlying condition (stalled here).
	a.SetRateLimited(false)
	diag = diagnoseAgent(a)
	if diag.Health != "stalled" {
		t.Errorf("after clear, Health = %q, want %q", diag.Health, "stalled")
	}
	if diag.RateLimited {
		t.Error("RateLimited should be false after clear")
	}
}

func TestDiagnoseIdle(t *testing.T) {
	// Simulate an idle agent — past the active-recency window but not stalled.
	buf := NewRingBuffer(1024)
	buf.Write([]byte("some data"))
	buf.mu.Lock()
	buf.lastWrite = time.Now().Add(-3 * time.Minute) // > 30s window, < 5min threshold
	buf.mu.Unlock()

	a := &Agent{
		Name:      "diag-idle",
		Type:      TypePolecat,
		PID:       0,
		Status:    StatusRunning,
		StartTime: time.Now().Add(-10 * time.Minute),
		outputBuf: buf,
		done:      make(chan struct{}),
	}

	diag := diagnoseAgent(a)
	if diag.Stalled {
		t.Error("agent should not be stalled yet")
	}
	if diag.Health != "idle" {
		t.Errorf("Health = %q, want %q", diag.Health, "idle")
	}
}

// TestDiagnoseBusyIdleBoundary pins the healthy→idle boundary to
// ActiveRecencyWindow rather than the (much wider) stall threshold: an agent
// quiet for just over the window must report "idle", while one that wrote
// within the window stays "healthy". This is what lets the bridget agents-view
// tell a busy agent from a quiet-but-alive one (gh #16).
func TestDiagnoseBusyIdleBoundary(t *testing.T) {
	newAgentIdle := func(idleAgo time.Duration) *Agent {
		buf := NewRingBuffer(1024)
		buf.Write([]byte("some data"))
		buf.mu.Lock()
		buf.lastWrite = time.Now().Add(-idleAgo)
		buf.mu.Unlock()
		return &Agent{
			Name:      "diag-boundary",
			Type:      TypePolecat,
			PID:       0,
			Status:    StatusRunning,
			StartTime: time.Now().Add(-10 * time.Minute),
			outputBuf: buf,
			done:      make(chan struct{}),
		}
	}

	// Just inside the window — still actively working.
	if diag := diagnoseAgent(newAgentIdle(10 * time.Second)); diag.Health != "healthy" {
		t.Errorf("idle 10s: Health = %q, want %q", diag.Health, "healthy")
	}
	// Just past the window but well within the stall threshold — idle, not
	// healthy. Under the old threshold/2 boundary this would have read "healthy".
	if diag := diagnoseAgent(newAgentIdle(45 * time.Second)); diag.Health != "idle" {
		t.Errorf("idle 45s: Health = %q, want %q", diag.Health, "idle")
	}
}

// stalledCrewAgent returns a crew agent whose last PTY write is idleAgo in the
// past — past the crew stall threshold so the cron-aware path is what decides
// stalled vs. idle.
func stalledCrewAgent(now time.Time, idleAgo time.Duration) *Agent {
	buf := NewRingBuffer(1024)
	buf.Write([]byte("old data"))
	buf.mu.Lock()
	buf.lastWrite = now.Add(-idleAgo)
	buf.mu.Unlock()
	return &Agent{
		Name:      "diag-crew",
		Type:      TypeCrew,
		PID:       0,
		Status:    StatusRunning,
		StartTime: now.Add(-time.Hour),
		outputBuf: buf,
		done:      make(chan struct{}),
	}
}

func TestDiagnoseCronCoveredNotStalled(t *testing.T) {
	now := time.Date(2026, 5, 21, 22, 0, 0, 0, time.UTC)
	// Idle 25 min — past the 10-min crew threshold — but the */30 mail-check
	// fired 25 min ago, so we are still within one cron interval of the last
	// firing. This is doctor's 2026-05-21 false positive (mg-5b23).
	a := stalledCrewAgent(now, 25*time.Minute)
	windows := []CronWindow{{
		LastFire: now.Add(-25 * time.Minute),
		NextFire: now.Add(5 * time.Minute),
		Interval: 30 * time.Minute,
	}}

	diag := diagnoseAgentAt(a, now, windows, mailLoopUnknown, nil)
	if diag.Stalled {
		t.Error("cron-covered agent must not be flagged stalled")
	}
	if !diag.CronCovered {
		t.Error("CronCovered should be true for between-cron idle")
	}
	if diag.Health != "idle" {
		t.Errorf("Health = %q, want %q", diag.Health, "idle")
	}
}

func TestDiagnoseGenuineWedgeStillStalled(t *testing.T) {
	now := time.Date(2026, 5, 21, 22, 0, 0, 0, time.UTC)
	// Idle past threshold with no cron schedule at all — a genuine wedge that
	// must still be flagged.
	a := stalledCrewAgent(now, 25*time.Minute)

	diag := diagnoseAgentAt(a, now, nil, mailLoopUnknown, nil)
	if !diag.Stalled {
		t.Error("agent with no cron schedule should still be flagged stalled")
	}
	if diag.CronCovered {
		t.Error("CronCovered should be false with no cron windows")
	}
	if diag.Health != "stalled" {
		t.Errorf("Health = %q, want %q", diag.Health, "stalled")
	}
}

func TestDiagnoseCronStaleStillStalled(t *testing.T) {
	now := time.Date(2026, 5, 21, 22, 0, 0, 0, time.UTC)
	// The agent has a */30 schedule, but the last firing was 40 min ago — more
	// than one interval. The cron is no longer explaining the idle, so a stall
	// past the threshold is genuine and must surface.
	a := stalledCrewAgent(now, 40*time.Minute)
	windows := []CronWindow{{
		LastFire: now.Add(-40 * time.Minute),
		NextFire: now.Add(-10 * time.Minute),
		Interval: 30 * time.Minute,
	}}

	diag := diagnoseAgentAt(a, now, windows, mailLoopUnknown, nil)
	if !diag.Stalled {
		t.Error("idle beyond one cron interval of last firing should be stalled")
	}
	if diag.CronCovered {
		t.Error("CronCovered should be false when idle exceeds the cron interval")
	}
}

func TestDiagnoseCronNeverFiredAnchorsToNextFire(t *testing.T) {
	now := time.Date(2026, 5, 21, 22, 0, 0, 0, time.UTC)
	// A schedule loaded after a pogod restart may have a zero LastFire. We
	// anchor coverage to NextFire - Interval; with NextFire 5 min out and a
	// 30-min interval, the implied last firing was 25 min ago — still covered.
	a := stalledCrewAgent(now, 25*time.Minute)
	windows := []CronWindow{{
		NextFire: now.Add(5 * time.Minute),
		Interval: 30 * time.Minute,
	}}

	diag := diagnoseAgentAt(a, now, windows, mailLoopUnknown, nil)
	if diag.Stalled {
		t.Error("never-fired schedule should still cover an in-window idle")
	}
	if !diag.CronCovered {
		t.Error("CronCovered should be true when anchored to NextFire")
	}
}

// fakeScheduleProvider records the identity it was queried with and returns a
// canned window set.
type fakeScheduleProvider struct {
	windows []CronWindow
	queried string
}

func (f *fakeScheduleProvider) CronWindowsForAgent(agentIdentity string) []CronWindow {
	f.queried = agentIdentity
	return f.windows
}

func TestRegistryDiagnoseUsesScheduleProvider(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}

	now := time.Now()
	fake := &fakeScheduleProvider{windows: []CronWindow{{
		LastFire: now.Add(-25 * time.Minute),
		NextFire: now.Add(5 * time.Minute),
		Interval: 30 * time.Minute,
	}}}
	reg.SetStallScheduleProvider(fake)

	a := stalledCrewAgent(now, 25*time.Minute)

	diag := reg.diagnose(a)
	if fake.queried != a.EventAgent() {
		t.Errorf("provider queried with %q, want %q", fake.queried, a.EventAgent())
	}
	if diag.Stalled {
		t.Error("registry diagnose should suppress stall via the schedule provider")
	}
	if !diag.CronCovered {
		t.Error("CronCovered should be true through the registry path")
	}
}

func TestDiagnoseRecentOutputTail(t *testing.T) {
	reg, err := NewRegistry(shortSocketDir(t))
	if err != nil {
		t.Fatalf("NewRegistry: %v", err)
	}
	defer reg.StopAll(2 * time.Second)

	a, err := reg.Spawn(SpawnRequest{
		Name:    "diag-tail",
		Type:    TypePolecat,
		Command: []string{"cat"},
	})
	if err != nil {
		t.Fatal(err)
	}

	a.Nudge("tail-test-data")
	time.Sleep(200 * time.Millisecond)

	diag := diagnoseAgent(a)
	if diag.RecentOutputTail == "" {
		t.Error("RecentOutputTail should not be empty after output")
	}
}
