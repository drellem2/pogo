package alerts

import (
	"os/exec"
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// withMutes installs m for one test and captures what the seam records.
func withMutes(t *testing.T, m map[Name]string) *[]events.Event {
	t.Helper()
	SetMutes(m)
	var got []events.Event
	old := emitEvent
	emitEvent = func(e events.Event) { got = append(got, e) }
	t.Cleanup(func() {
		SetMutes(nil)
		emitEvent = old
	})
	return &got
}

// A muted alert is not sent, Mail reports success so the caller's detector
// carries on, and the withheld send is recorded as alert_muted with the key
// that muted it (drellem2/pogo#262 slice 2).
func TestMailMutedIsWithheldAndRecorded(t *testing.T) {
	evs := withMutes(t, map[Name]string{AckWatchFinding: `[alerts."ack_watch.finding"] mute = true`})

	called := false
	via := func(string, string, string, string) error { called = true; return errSentinel{} }
	if err := Mail(AckWatchFinding, via, "mayor", "ack-watch", "subj", "body"); err != nil {
		t.Errorf("muted Mail returned %v, want nil", err)
	}
	if called {
		t.Error("a muted alert reached its transport")
	}
	if len(*evs) != 1 {
		t.Fatalf("recorded %d events, want 1 alert_muted: %+v", len(*evs), *evs)
	}
	e := (*evs)[0]
	if e.EventType != EventMuted || e.Details["alert"] != "ack_watch.finding" || e.Details["to"] != "mayor" ||
		e.Details["subject"] != "subj" || e.Details["reason"] != `[alerts."ack_watch.finding"] mute = true` {
		t.Errorf("event %+v", e)
	}

	// Positive control: the same table leaves every other alert untouched.
	called = false
	if err := Mail(DeafWatchFinding, via, "mayor", "deaf-watch", "s", "b"); err != (errSentinel{}) {
		t.Errorf("unmuted Mail returned %v, want the transport's error", err)
	}
	if !called {
		t.Error("an unmuted alert was not sent")
	}
	if len(*evs) != 1 {
		t.Errorf("an unmuted send was recorded as muted: %+v", *evs)
	}
}

func TestMailOutputMutedRunsNothing(t *testing.T) {
	evs := withMutes(t, map[Name]string{OrphanPolecat: "k"})
	ran := false
	old := execCommand
	execCommand = func(name string, args ...string) *exec.Cmd { ran = true; return exec.Command("true") }
	defer func() { execCommand = old }()

	out, err := MailOutput(OrphanPolecat, "mayor", "pogod", "s", "b")
	if out != nil || err != nil || ran {
		t.Errorf("muted MailOutput: out=%q err=%v ran=%v, want nothing run", out, err, ran)
	}
	if len(*evs) != 1 || (*evs)[0].EventType != EventMuted {
		t.Errorf("events %+v, want one alert_muted", *evs)
	}
	if _, err := MailOutput(WorkspaceStale, "mayor", "pogod", "s", "b"); err != nil || !ran {
		t.Errorf("control: unmuted MailOutput err=%v ran=%v", err, ran)
	}
}

func TestWithholdAndSetMutesCopies(t *testing.T) {
	m := map[Name]string{StallWatchBlockedReminder: "[stall_watch] blocked_reminder_enabled = false"}
	evs := withMutes(t, m)
	m[AckWatchFinding] = "added after SetMutes"
	if MuteReason(AckWatchFinding) != "" {
		t.Error("SetMutes kept a reference to the caller's map")
	}
	if why, ok := Withhold(StallWatchBlockedReminder, "pm-pogo", "s"); !ok || why == "" {
		t.Errorf("Withhold = %q, %v; want muted", why, ok)
	}
	if why, ok := Withhold(AckWatchFinding, "mayor", "s"); ok || why != "" {
		t.Errorf("Withhold of an unmuted alert = %q, %v", why, ok)
	}
	if len(*evs) != 1 {
		t.Errorf("events %+v, want exactly the one withheld send", *evs)
	}
}

// Muted rows list first, and a muted per-row condition name — not in the
// registry — gets a row of its own rather than vanishing from the listing.
func TestRowsListMutedFirstIncludingConditionRows(t *testing.T) {
	s := Settings{
		Coordinator: "mayor", EscalationBox: "human",
		Muted: map[Name]string{
			StallWatchBlockedReminder:               "[stall_watch] blocked_reminder_enabled = false",
			Name("condition.scheduler_load_failed"): "[conditions] disable lists scheduler_load_failed",
		},
	}
	rows := Rows(s, false)
	deliveries := 0
	for _, d := range registry {
		if d.Class == ClassDelivery {
			deliveries++
		}
	}
	if len(rows) != len(registry)-deliveries+1 { // minus the deliveries, plus the condition row
		t.Errorf("listed %d rows", len(rows))
	}
	if !rows[0].Muted || !rows[1].Muted || rows[2].Muted {
		t.Fatalf("muted rows must sort first: %+v %+v %+v", rows[0], rows[1], rows[2])
	}
	if rows[0].Name != "condition.scheduler_load_failed" || rows[0].Source != "[conditions] disable lists scheduler_load_failed" ||
		rows[0].Effective != "the condition's addressee (usually mayor)" {
		t.Errorf("condition row: %+v", rows[0])
	}
}
