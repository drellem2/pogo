package main

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
	"github.com/drellem2/pogo/internal/refusalwatch"
)

// drellem2/pogo#238: condition mail can be switched off entirely, per
// condition, or only its out-of-band copy — and a disabled send must never be
// recorded as delivered.

// withConditionEventLog points events.log at a private file for the test.
func withConditionEventLog(t *testing.T) string {
	t.Helper()
	log := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(log)
	t.Cleanup(func() { events.SetLogPathForTesting(testEventLogPath) })
	return log
}

func conditionsCfg(mail, oob bool, disable ...string) config.ConditionsConfig {
	return config.ConditionsConfig{Mail: mail, OutOfBand: oob, Disable: disable}
}

func TestConditionsMailOff_SendsNothingAndRecordsNotNotified(t *testing.T) {
	log := withConditionEventLog(t)
	rec := &condRecorder{}
	wk := &wakeRecorder{}
	a, path := newTestAnnunciator(t, rec.send, wk.nudge)
	applyConditionsConfig(a, conditionsCfg(false, true), "daniel")

	c := testCondition(rowA2SchedulerLoad, "boom")
	c.OutOfBand = true
	c.Wake = true
	t0 := time.Date(2026, 10, 27, 19, 0, 0, 0, time.UTC)
	a.Raise(c, t0)
	a.Raise(c, t0.Add(25*time.Hour))
	a.retryWakes(t0.Add(26 * time.Hour))
	a.report()

	if len(rec.sent) != 0 {
		t.Fatalf("mail = false sent %+v, want nothing", rec.sent)
	}
	if len(wk.woken) != 0 {
		t.Fatalf("mail = false woke %v, want nobody", wk.woken)
	}
	if _, stored := loadConditionNotices(path).Conditions[c.ID]; stored {
		t.Error("a disabled condition was stored as delivered")
	}
	evs := conditionEventsFor(t, log, c.ID)
	if len(evs) != 2 {
		t.Fatalf("got %d pogod_condition events, want one per raise (2): %v", len(evs), evs)
	}
	for _, ev := range evs {
		if ev.Details["notified"] != false || ev.Details["reason"] != "mail_disabled" {
			t.Errorf("event %v, want notified=false reason=mail_disabled", ev.Details)
		}
	}
}

func TestConditionsDisable_PerConditionAndPerSubjectPrefix(t *testing.T) {
	withConditionEventLog(t)
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	applyConditionsConfig(a, conditionsCfg(true, true, rowA2SchedulerLoad, "autostart_failed", "restart_failed:doctor"), "daniel")
	now := time.Now()

	for _, id := range []string{
		rowA2SchedulerLoad,             // exact id
		rowA5AutoStartPrefix + "mayor", // whole per-subject family
		rowA6RestartPrefix + "doctor",  // one subject
	} {
		a.Raise(testCondition(id, "x"), now)
	}
	if len(rec.sent) != 0 {
		t.Fatalf("disabled conditions sent %+v, want nothing", rec.sent)
	}

	// Positive control: the same annunciator still mails what is not listed —
	// including a different subject of a one-subject entry.
	for _, id := range []string{rowA4PromptRefresh, rowA6RestartPrefix + "mayor"} {
		a.Raise(testCondition(id, "x"), now)
	}
	if len(rec.sent) != 2 {
		t.Fatalf("enabled conditions sent %d mails, want 2: %+v", len(rec.sent), rec.sent)
	}
}

func TestConditionsOutOfBandOff_MailsOnlyTheCoordinator(t *testing.T) {
	log := withConditionEventLog(t)
	rec := &condRecorder{}
	a, path := newTestAnnunciator(t, rec.send, nil)
	applyConditionsConfig(a, conditionsCfg(true, false), "daniel")

	c := testCondition(rowA11HeartbeatWrite, "disk full")
	c.OutOfBand = true
	a.Raise(c, time.Now())

	if len(rec.sent) != 1 || rec.sent[0].to != "mayor" {
		t.Fatalf("sent %+v, want mayor only", rec.sent)
	}
	if st := loadConditionNoticesAfterFlush(a, path).Conditions[c.ID]; st.OutOfBand != "" {
		t.Errorf("store records an out-of-band copy to %q that was never sent", st.OutOfBand)
	}
	for _, ev := range conditionEventsFor(t, log, c.ID) {
		if ev.Details["addressee"] == "daniel" {
			t.Errorf("an event claims an out-of-band copy: %v", ev.Details)
		}
	}

	// Positive control: with the key on, the same raise reaches the box.
	rec2 := &condRecorder{}
	b, _ := newTestAnnunciator(t, rec2.send, nil)
	applyConditionsConfig(b, conditionsCfg(true, true), "daniel")
	b.Raise(c, time.Now())
	if len(rec2.sent) != 2 || rec2.sent[1].to != "daniel" {
		t.Fatalf("control: sent %+v, want mayor then daniel", rec2.sent)
	}
}

func loadConditionNoticesAfterFlush(a *conditionAnnunciator, path string) conditionNotices {
	a.flush()
	return loadConditionNotices(path)
}

func TestConditionsDisabledSummaryNamesWhatIsOff(t *testing.T) {
	cases := []struct {
		cfg  config.ConditionsConfig
		want []string
	}{
		{conditionsCfg(true, true), nil},
		{conditionsCfg(false, true), []string{"ALL condition mail is OFF", "no reader but events.log"}},
		{conditionsCfg(true, true, "role_pin_failed", "autostart_failed"), []string{"[autostart_failed role_pin_failed]", "no reader but events.log"}},
		{conditionsCfg(true, false), []string{"out_of_band = false"}},
	}
	for _, tc := range cases {
		a, _ := newTestAnnunciator(t, (&condRecorder{}).send, nil)
		got := applyConditionsConfig(a, tc.cfg, "daniel")
		if tc.want == nil {
			if got != "" {
				t.Errorf("%+v: summary %q, want none", tc.cfg, got)
			}
			continue
		}
		for _, w := range tc.want {
			if !strings.Contains(got, w) {
				t.Errorf("%+v: summary %q does not contain %q", tc.cfg, got, w)
			}
		}
	}
}

// The [conditions] section governs the annunciator only. The other watchers'
// escalation routing reads [agents] escalation_box directly, and every switch
// in this section off must leave it where it was.
func TestConditionsOff_LeavesOtherWatchersEscalationRoutingUnchanged(t *testing.T) {
	cfg := &config.Config{
		Agents:     config.AgentsConfig{EscalationBox: "operator"},
		Conditions: conditionsCfg(false, false, "scheduler_load_failed"),
	}
	a, _ := newTestAnnunciator(t, (&condRecorder{}).send, nil)
	applyConditionsConfig(a, cfg.Conditions, cfg.Agents.EscalationBoxName())

	box := cfg.Agents.EscalationBoxName()
	if box != "operator" {
		t.Fatalf("escalation box = %q after applying [conditions], want operator", box)
	}
	if got := reaperOptions(cfg.Reaper, "mayor", box).EscalateTo; got != "operator" {
		t.Errorf("reaper escalates to %q, want operator", got)
	}
	var sawMail bool
	for _, s := range refusalSinks(box) {
		if m, ok := s.(refusalwatch.MailSink); ok {
			sawMail = true
			if m.To != "operator" {
				t.Errorf("refusal alarm mails %q, want operator", m.To)
			}
		}
	}
	if !sawMail {
		t.Error("refusalSinks has no mail sink — the check above saw nothing")
	}
}
