package alerts

import "testing"

func TestRowsResolveRolesAndNotifyTo(t *testing.T) {
	s := Settings{
		Coordinator:   "mayor",
		EscalationBox: "human",
		NotifyTo:      map[string]string{"ack_watch.notify_to": "pm-pogo", "deaf_watch.notify_to": "mayor"},
		Muted:         map[Name]string{BlindWatchFinding: "blind_watch.mail = false"},
	}
	rows := Rows(s, false)
	by := map[Name]Row{}
	for _, r := range rows {
		by[r.Name] = r
		if r.Class != ClassAlert {
			t.Errorf("%s (%s) listed without --all", r.Name, r.Class)
		}
	}
	if len(rows) == 0 || rows[0].Name != BlindWatchFinding || !rows[0].Muted {
		t.Errorf("muted row must sort first; first row is %+v", rows[0])
	}
	if r := by[AckWatchFinding]; r.Effective != "pm-pogo; also human when it escalates" || r.Source != "ack_watch.notify_to" {
		t.Errorf("ack_watch.finding: %+v", r)
	}
	// A notify_to equal to the coordinator changed nothing.
	if r := by[DeafWatchFinding]; r.Source != "default" {
		t.Errorf("deaf_watch.finding source = %q, want default", r.Source)
	}
	if r := by[UsageLimitHit]; r.Effective != "human" || r.Muted {
		t.Errorf("usage_limit.hit: %+v", r)
	}
	if _, ok := by[SchedulerDelivery]; ok {
		t.Error("a delivery mechanic was listed without --all")
	}
	all := Rows(s, true)
	if len(all) != len(registry) {
		t.Errorf("--all lists %d of %d declarations", len(all), len(registry))
	}
}
