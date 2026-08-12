package claude

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/events"
)

// This file covers the SECOND index carrying drellem2/pogo#138's rule.
//
// internal/wedgewatch's stall clock is the one the issue named. This tracker is
// the other: same log, same key (the agent's identity), same absence of any
// exclusion — and it is the one that closes the loop, because the dismissal it
// was ingesting is emitted by fireDismissal a few hundred lines up in this very
// file. Fixing wedgewatch and leaving this alone would have left the defect
// live under a different trigger.

func lineOf(t *testing.T, ev events.Event) []byte {
	t.Helper()
	if ev.Timestamp == "" {
		t.Fatal("fixture guard: an event with no timestamp is dropped before the filter is reached")
	}
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}

func evAt(agent, typ string, at time.Time) events.Event {
	return events.Event{
		SchemaVersion: events.SchemaVersion,
		EventType:     typ,
		Agent:         agent,
		Timestamp:     at.UTC().Format(time.RFC3339Nano),
		Details:       map[string]any{},
	}
}

// TestTheTrackerDoesNotIngestItsOwnDismissal is the loop this file closes.
//
// fireDismissal emits modal_dismissed under deps.AgentID. The tracker follows
// the same log. So pogod's write landed back in the index as if the wedged
// agent had produced it — and both gates that read the index were affected:
// the 20m auto-dismissal gate got its clock reset by its own fire, and the
// usage-limit CLEAR gate saw "the log advanced past the wedge point" and
// declared a suspected limit cleared that had not cleared at all.
func TestTheTrackerDoesNotIngestItsOwnDismissal(t *testing.T) {
	wedgedAt := time.Now().Add(-30 * time.Minute)
	tr := &eventsActivityTracker{lastSeen: make(map[string]time.Time)}
	tr.ingest(lineOf(t, evAt("cat-wedged", "agent_spawned", wedgedAt)))

	for _, typ := range []string{"modal_dismissed", "rating_dialog_dismissed", "rate_limit_modal_dismissed"} {
		tr.ingest(lineOf(t, evAt("cat-wedged", typ, time.Now())))
		if got := tr.LastSeen("cat-wedged"); !got.Equal(wedgedAt) {
			t.Fatalf("%s advanced the tracker to %s; want it left at the agent's own %s line. "+
				"pogod's dismissal is not the agent producing events — it fires BECAUSE the agent "+
				"is behind a modal it did not clear", typ, got, wedgedAt)
		}
	}
}

// TestSyntheticFailureDoesNotAdvanceTheTracker is the same rule via the other
// author. synthwatch records it under the failing agent, which on this host is
// the most frequent crew event there is.
func TestSyntheticFailureDoesNotAdvanceTheTracker(t *testing.T) {
	wedgedAt := time.Now().Add(-30 * time.Minute)
	tr := &eventsActivityTracker{lastSeen: make(map[string]time.Time)}
	tr.ingest(lineOf(t, evAt("cat-failing", "agent_spawned", wedgedAt)))
	tr.ingest(lineOf(t, evAt("cat-failing", "synthetic_failure_detected", time.Now())))

	if got := tr.LastSeen("cat-failing"); !got.Equal(wedgedAt) {
		t.Errorf("synthetic_failure_detected advanced the tracker to %s; want %s", got, wedgedAt)
	}
}

// TestTheTrackerStillIngestsTheAgentsOwnEvents is the control, and it matters
// more here than in wedgewatch: this tracker gates an INTERVENTION rather than
// a report. An over-broad exclusion would leave every agent looking permanently
// stale and hand the 20m gate a standing reason to write into their PTYs.
func TestTheTrackerStillIngestsTheAgentsOwnEvents(t *testing.T) {
	spawn := time.Now().Add(-30 * time.Minute)
	active := time.Now().Add(-time.Minute)
	tr := &eventsActivityTracker{lastSeen: make(map[string]time.Time)}
	tr.ingest(lineOf(t, evAt("cat-live", "agent_spawned", spawn)))
	tr.ingest(lineOf(t, evAt("cat-live", "modal_dismissed", time.Now().Add(-10*time.Minute))))
	tr.ingest(lineOf(t, evAt("cat-live", "work_item_claimed", active)))

	if got := tr.LastSeen("cat-live"); !got.Equal(active) {
		t.Errorf("last seen = %s, want the agent's own work_item_claimed at %s", got, active)
	}
}

// TestEveryTypeFireDismissalEmitsIsExcluded closes the loop by DRIVING the
// emitter rather than by restating its strings.
//
// The three tests above name event types as literals, which pins today's set
// and nothing else — add a fourth alias to fireDismissal and they stay green
// while the new one refreshes every liveness index on the box. This one runs
// the real fireDismissal over the real DefaultModalMatchers table, collects
// whatever it actually emitted, and requires all of it to be excluded. Adding
// an alias without excluding it fails here.
func TestEveryTypeFireDismissalEmitsIsExcluded(t *testing.T) {
	for _, m := range DefaultModalMatchers {
		var emitted []events.Event
		deps := ModalHookDeps{
			AgentName: "alice",
			AgentID:   "cat-alice",
			Dismiss:   func([]byte) error { return nil },
			EmitEvent: func(ev events.Event) { emitted = append(emitted, ev) },
			Now:       time.Now,
		}
		if !fireDismissal(m, deps) {
			t.Fatalf("%s: fireDismissal reported failure with a Dismiss that cannot fail", m.Name)
		}
		if len(emitted) == 0 {
			t.Fatalf("%s: fireDismissal emitted nothing, so this test proves nothing", m.Name)
		}
		for _, ev := range emitted {
			if ev.Agent != deps.AgentID {
				// Not a failure of this rule — an event pogod files under its
				// OWN identity cannot warm an agent's clock and needs no
				// exclusion. Recorded so the distinction stays visible.
				continue
			}
			if events.CountsAsAgentActivity(ev.EventType) {
				t.Errorf("%s: fireDismissal emits %q under the dismissed agent's identity (%s), and it "+
					"counts as agent activity. pogod's dismissal would refresh every last-seen index "+
					"over the event log — drellem2/pogo#138. Add it to notAgentActivity in "+
					"internal/events/activity.go", m.Name, ev.EventType, ev.Agent)
			}
		}
	}
}

// TestTouchIsUnfilteredAndThatIsDeliberate pins the seam the filter does NOT
// cover, so the next reader does not assume it does.
//
// Touch is the adapter entry point — it seeds an observation directly rather
// than parsing a log line, and its callers are stating a fact about the agent
// ("this one just spawned") rather than replaying pogod's own writes. Routing
// it through the filter would mean guessing an event type it was never given.
func TestTouchIsUnfilteredAndThatIsDeliberate(t *testing.T) {
	when := time.Now()
	tr := &eventsActivityTracker{lastSeen: make(map[string]time.Time)}
	tr.Touch("cat-seeded", when)
	if got := tr.LastSeen("cat-seeded"); !got.Equal(when) {
		t.Errorf("Touch did not seed the tracker: got %s, want %s", got, when)
	}
}
