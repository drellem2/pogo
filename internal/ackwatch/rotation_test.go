package ackwatch

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// writeRotated lays down a rotated log: rotated[0] becomes path.1, rotated[1]
// path.2, and so on (older with each index), and live becomes path itself. It
// returns path. Hand-written chunks rather than a real rotation because the
// readers under test only care which files exist, and a real rotation needs
// 100MB of padding per chunk.
func writeRotated(t *testing.T, live []events.Event, rotated ...[]events.Event) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events.log")
	write := func(p string, evs []events.Event) {
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		enc := json.NewEncoder(f)
		for _, ev := range evs {
			if err := enc.Encode(ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	for i, evs := range rotated {
		write(fmt.Sprintf("%s.%d", path, i+1), evs)
	}
	write(path, live)
	return path
}

func wake(at time.Time) events.Event {
	return events.Event{EventType: "system_wake", Agent: "pogod", Timestamp: at.Format(time.RFC3339Nano)}
}

func synth(typ, target string, at time.Time) events.Event {
	return events.Event{EventType: typ, Agent: "pogod", Timestamp: at.Format(time.RFC3339Nano),
		Details: map[string]any{"target": target}}
}

// TestReadFireTimelineCrossesRotation is mg-a6c0: a populations window that
// crosses a rotation must count the fires in the rotated chunk. The live-file
// control is asserted too, so the test cannot pass by the rotated file being
// ignored for some other reason.
func TestReadFireTimelineCrossesRotation(t *testing.T) {
	path := writeRotated(t,
		[]events.Event{
			fireEvent("scheduler_fire_delivered", "pa", "pa", base.Add(-30*time.Minute)),
			fireEvent("scheduler_fire_completed", "pa", "pa", base.Add(-29*time.Minute)),
		},
		[]events.Event{
			fireEvent("scheduler_fire_delivered", "pa", "pa", base.Add(-5*time.Hour)),
			fireEvent("scheduler_fire_delivered", "mayor", "mayor", base.Add(-4*time.Hour)),
			fireEvent("scheduler_fire_completed", "mayor", "mayor", base.Add(-4*time.Hour).Add(time.Minute)),
		},
	)
	since := base.Add(-24 * time.Hour)

	live, err := events.ReadFiltered(path, events.Filter{SinceMin: since, Type: "scheduler_fire_delivered"})
	if err != nil {
		t.Fatal(err)
	}
	if len(live) != 1 {
		t.Fatalf("control: the live file alone should hold 1 delivery, got %d", len(live))
	}

	evs, cov, err := ReadFireTimeline(path, since, time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var delivered, completed int
	for _, ev := range evs {
		switch ev.Kind {
		case FireDelivered:
			delivered++
		case FireCompleted:
			completed++
		}
	}
	if delivered != 3 || completed != 2 {
		t.Errorf("window crossing the rotation: got %d delivered / %d completed, want 3 / 2", delivered, completed)
	}
	if cov.Truncated {
		t.Error("nothing was discarded (no .5), so the window is complete, not truncated")
	}
	for i := 1; i < len(evs); i++ {
		if evs[i].At.Before(evs[i-1].At) {
			t.Errorf("timeline out of order at %d", i)
		}
	}
}

// TestReadFireTimelineReportsDiscardedHistory: once rotation has discarded a
// chunk, a window reaching past the oldest retained record must come back
// flagged, with the floor — and RecentFires must read that as blind, not as a
// window with no fires in it.
func TestReadFireTimelineReportsDiscardedHistory(t *testing.T) {
	floor := base.Add(-50 * time.Minute)
	chunks := make([][]events.Event, 5)
	for i := range chunks {
		// .5 is the oldest; each chunk holds one delivery, 10m apart.
		chunks[i] = []events.Event{fireEvent("scheduler_fire_delivered", "pa", "pa",
			base.Add(-time.Duration(i+1)*10*time.Minute))}
	}
	path := writeRotated(t, []events.Event{
		fireEvent("scheduler_fire_delivered", "pa", "pa", base.Add(-time.Minute)),
	}, chunks...)
	if !events.LogSpilled(path) {
		t.Fatal("precondition: .5 is filled, so the log should read as spilled")
	}

	evs, cov, err := ReadFireTimeline(path, base.Add(-3*time.Hour), base)
	if err != nil {
		t.Fatal(err)
	}
	if len(evs) != 6 {
		t.Errorf("want all 6 retained deliveries, got %d", len(evs))
	}
	if !cov.Truncated || !cov.Floor.Equal(floor) {
		t.Errorf("coverage = %+v, want truncated with floor %s", cov, floor)
	}

	got := RecentFires(path, base, 3*time.Hour)
	if got.Err == "" || !strings.Contains(got.Err, floor.UTC().Format(time.RFC3339)) {
		t.Errorf("RecentFires over a window past discarded history: Err = %q, want it blind and naming the floor", got.Err)
	}

	// A window inside retained history is neither truncated nor blind.
	if got := RecentFires(path, base, 30*time.Minute); got.Err != "" || got.Delivered != 3 {
		t.Errorf("30m window: Err=%q Delivered=%d, want clean and 3", got.Err, got.Delivered)
	}
}

// TestPopulationReportWarnsOnTruncatedHistory: the populations text output
// must say its window starts before retained history — especially when it
// found nothing at all.
func TestPopulationReportWarnsOnTruncatedHistory(t *testing.T) {
	rep := SplitWithEpisodes(nil, nil)
	rep.HistoryTruncated = true
	rep.HistoryFloor = base
	rep.RequestedFrom = base.Add(-7 * 24 * time.Hour)
	out := rep.Render()
	if !strings.Contains(out, "window starts before retained history at "+base.Format(time.RFC3339)) {
		t.Errorf("truncation warning missing:\n%s", out)
	}
	if !strings.Contains(SplitWithEpisodes(nil, nil).Render(), "No fires") ||
		strings.Contains(SplitWithEpisodes(nil, nil).Render(), "retained history") {
		t.Error("an untruncated report must not carry the warning")
	}
}

// TestReadFailureEpisodesCrossesRotation: an episode detected before the last
// rotation and cleared after it is one closed episode. Reading the live file
// alone saw only the clear and dropped it as unmatched.
func TestReadFailureEpisodesCrossesRotation(t *testing.T) {
	path := writeRotated(t,
		[]events.Event{synth("synthetic_failure_cleared", "pa", base.Add(-time.Hour))},
		[]events.Event{synth("synthetic_failure_detected", "pa", base.Add(-5*time.Hour))},
	)
	eps, err := ReadFailureEpisodes(path, base.Add(-24*time.Hour), time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	if len(eps) != 1 || !eps[0].From.Equal(base.Add(-5*time.Hour)) || !eps[0].Until.Equal(base.Add(-time.Hour)) {
		t.Errorf("episodes = %+v, want one pa episode from -5h to -1h", eps)
	}
}

// TestLastDisruptionCrossesRotation: a wake inside DisruptionWindow that
// rotated out of the live file is still a wake.
func TestLastDisruptionCrossesRotation(t *testing.T) {
	woke := base.Add(-30 * time.Minute)
	path := writeRotated(t,
		[]events.Event{{EventType: "nudge_sent", Agent: "pogod", Timestamp: base.Add(-10 * time.Minute).Format(time.RFC3339Nano)}},
		[]events.Event{wake(woke)},
	)
	if live, _ := events.ReadFiltered(path, events.Filter{Type: DisruptionEventType}); len(live) != 0 {
		t.Fatalf("control: the live file should hold no wake, got %d", len(live))
	}
	at, reason := LastDisruption(path, base)
	if !at.Equal(woke) || reason != DisruptionEventType {
		t.Errorf("LastDisruption = %s %q, want the rotated wake at %s", at, reason, woke)
	}
}
