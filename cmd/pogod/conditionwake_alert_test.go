package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/alerts"
)

// drellem2/pogo#262 slice 4: the wake is condition.wake, an alert of its own.
// Muted, the notice is still mailed and the wake is dropped from the queue
// (not retried, not logged as woken); routed, the wake is typed at the
// route's agent.
func TestConditionWake_MutedAndRouted(t *testing.T) {
	t.Cleanup(func() { alerts.SetMutes(nil); alerts.SetRoutes(nil) })
	t0 := time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC)

	alerts.SetMutes(map[alerts.Name]string{alerts.ConditionWake: `[alerts."condition.wake"] mute = true`})
	rec := &condRecorder{}
	wake := &wakeRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, wake.nudge)
	c := testCondition("sched", "boom")
	c.Wake = true
	a.Raise(c, t0)
	if len(rec.sent) == 0 {
		t.Fatal("muting the wake withheld the notice")
	}
	a.retryWakes(t0.Add(30 * time.Second))
	if len(wake.woken) != 0 {
		t.Fatalf("muted wake typed: %v", wake.woken)
	}
	if len(a.pendingWakes) != 0 {
		t.Error("a muted wake stayed queued for retry")
	}

	alerts.SetMutes(nil)
	alerts.SetRoutes(map[alerts.Name]alerts.Route{alerts.ConditionWake: {To: "ops", Source: "test"}})
	rec2 := &condRecorder{}
	wake2 := &wakeRecorder{}
	a2, _ := newTestAnnunciator(t, rec2.send, wake2.nudge)
	a2.Raise(c, t0)
	a2.retryWakes(t0.Add(30 * time.Second))
	if len(wake2.woken) != 1 || !strings.HasPrefix(wake2.woken[0], "ops: ") {
		t.Fatalf("routed wake: %v, want one at ops", wake2.woken)
	}
	// The wake still says where the notice is: the addressee's mailbox.
	if !strings.Contains(wake2.woken[0], "mg mail list mayor") {
		t.Errorf("routed wake lost its pointer to the notice: %q", wake2.woken[0])
	}
}
