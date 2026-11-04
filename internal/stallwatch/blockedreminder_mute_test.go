package stallwatch

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/events"
)

// drellem2/pogo#262 slice 2: stall_watch.blocked_reminder muted (the alias of
// `blocked_reminder_enabled = false`) withholds the nudge, and nothing else —
// the check still runs and still records its stall_watch_fired event, marked
// muted, and the seam records alert_muted.
func TestBlockedReminderMutedWithholdsOnlyTheNudge(t *testing.T) {
	log := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(log)
	t.Cleanup(func() { events.SetLogPathForTesting("") })
	alerts.SetMutes(map[alerts.Name]string{alerts.StallWatchBlockedReminder: "[stall_watch] blocked_reminder_enabled = false"})
	t.Cleanup(func() { alerts.SetMutes(nil) })

	w, rec, workRoot, mailRoot := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot, "pm-pogo")
	now := time.Now()
	writeItem(t, workRoot, "mg-e084", "blocked:pm-pogo", now)
	w.Check(now)

	if got := blockedNudges(rec); len(got) != 0 {
		t.Fatalf("a muted blocked-reminder nudged %+v", got)
	}
	var fired []events.Event
	rec.mu.Lock()
	for _, e := range rec.events {
		if e.EventType == "stall_watch_fired" && e.Details["category"] == categoryBlockedReminder {
			fired = append(fired, e)
		}
	}
	rec.mu.Unlock()
	if len(fired) != 1 || fired[0].Details["nudge_delivery"] != "muted" ||
		fired[0].Details["muted"] != "[stall_watch] blocked_reminder_enabled = false" {
		t.Fatalf("stall_watch_fired events %+v, want one marked muted", fired)
	}
	muted := 0
	if err := events.ScanFile(log, func(e events.Event) {
		if e.EventType == alerts.EventMuted && e.Details["alert"] == string(alerts.StallWatchBlockedReminder) && e.Details["to"] == "pm-pogo" {
			muted++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if muted != 1 {
		t.Errorf("%d alert_muted events, want 1", muted)
	}

	// Positive control: unmuted, the same environment nudges.
	alerts.SetMutes(nil)
	w2, rec2, workRoot2, mailRoot2 := testEnv(t, blockedCfg())
	makeMailbox(t, mailRoot2, "pm-pogo")
	writeItem(t, workRoot2, "mg-e084", "blocked:pm-pogo", now)
	w2.Check(now)
	if got := blockedNudges(rec2); len(got) != 1 {
		t.Errorf("control: %d nudges, want 1", len(got))
	}
}
