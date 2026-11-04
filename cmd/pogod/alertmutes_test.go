package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/config"
	"github.com/drellem2/pogo/internal/events"
)

// drellem2/pogo#262 slice 2: one mute table, [alerts] entries plus the older
// keys that are now its aliases.

func defaultsCfg() *config.Config {
	return &config.Config{
		Conditions: config.ConditionsConfig{Mail: true, OutOfBand: true},
		BlindWatch: config.BlindWatchConfig{Mail: true, OutOfBand: true},
		StallWatch: config.StallWatchConfig{BlockedReminderEnabled: true},
	}
}

func TestAlertMutes_DefaultMutesNothing(t *testing.T) {
	sandboxHome(t)
	tbl := resolveAlertMutes(config.Load())
	if len(tbl.Muted) != 0 || len(tbl.Warnings) != 0 {
		t.Errorf("an unconfigured install: muted=%v warnings=%v", tbl.Muted, tbl.Warnings)
	}
	if line := mutedAlertsLine(tbl); line != "" {
		t.Errorf("startup line %q on an install that mutes nothing", line)
	}
}

func TestAlertMutes_AliasKeys(t *testing.T) {
	cases := []struct {
		name string
		set  func(c *config.Config)
		want map[alerts.Name]string
	}{
		{"conditions.mail", func(c *config.Config) { c.Conditions.Mail = false }, map[alerts.Name]string{
			alerts.ConditionNotice:    "[conditions] mail = false",
			alerts.ConditionOutOfBand: "[conditions] mail = false",
		}},
		{"conditions.out_of_band", func(c *config.Config) { c.Conditions.OutOfBand = false }, map[alerts.Name]string{
			alerts.ConditionOutOfBand: "[conditions] out_of_band = false",
		}},
		{"conditions.disable", func(c *config.Config) { c.Conditions.Disable = []string{"scheduler_load_failed", " "} }, map[alerts.Name]string{
			"condition.scheduler_load_failed": "[conditions] disable lists scheduler_load_failed",
		}},
		{"blind_watch.mail", func(c *config.Config) { c.BlindWatch.Mail = false }, map[alerts.Name]string{
			alerts.BlindWatchFinding:   "[blind_watch] mail = false",
			alerts.BlindWatchCleared:   "[blind_watch] mail = false",
			alerts.BlindWatchOutOfBand: "[blind_watch] mail = false",
		}},
		{"blind_watch.out_of_band", func(c *config.Config) { c.BlindWatch.OutOfBand = false }, map[alerts.Name]string{
			alerts.BlindWatchOutOfBand: "[blind_watch] out_of_band = false",
		}},
		{"blocked_reminder_enabled", func(c *config.Config) { c.StallWatch.BlockedReminderEnabled = false }, map[alerts.Name]string{
			alerts.StallWatchBlockedReminder: "[stall_watch] blocked_reminder_enabled = false",
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := defaultsCfg()
			tc.set(cfg)
			got := resolveAlertMutes(cfg).Muted
			if len(got) != len(tc.want) {
				t.Errorf("muted %v, want %v", got, tc.want)
			}
			for n, why := range tc.want {
				if got[n] != why {
					t.Errorf("%s: %q, want %q", n, got[n], why)
				}
			}
		})
	}
}

// [alerts] wins over an alias, both ways, and says so when it unmutes; names
// that are not muteable alerts are refused loudly rather than silently kept.
func TestAlertMutes_AlertsTableWinsAndWarns(t *testing.T) {
	cfg := defaultsCfg()
	cfg.Conditions.Mail = false
	cfg.Alerts = map[string]config.AlertConfig{
		"condition.notice":          {Mute: false, MuteSet: true},
		"ack_watch.finding":         {Mute: true, MuteSet: true},
		"condition.role_pin_failed": {Mute: true, MuteSet: true},
		"deaf_watch.finding":        {Mute: false, MuteSet: true}, // unmuting what nothing muted: no warning
		"nope.never":                {Mute: true, MuteSet: true},
		"scheduler.delivery":        {Mute: true, MuteSet: true},
		"refinery.merged":           {}, // no mute key
	}
	tbl := resolveAlertMutes(cfg)

	if _, muted := tbl.Muted[alerts.ConditionNotice]; muted {
		t.Error("[alerts] mute = false did not override [conditions] mail = false")
	}
	if tbl.Muted[alerts.ConditionOutOfBand] != "[conditions] mail = false" {
		t.Errorf("condition.out_of_band = %q; the override named only condition.notice", tbl.Muted[alerts.ConditionOutOfBand])
	}
	if tbl.Muted[alerts.AckWatchFinding] != `[alerts."ack_watch.finding"] mute = true` {
		t.Errorf("ack_watch.finding = %q", tbl.Muted[alerts.AckWatchFinding])
	}
	if tbl.Muted["condition.role_pin_failed"] == "" {
		t.Error("a per-row condition name was not accepted")
	}
	for _, n := range []alerts.Name{"nope.never", alerts.SchedulerDelivery, alerts.DeafWatchFinding, alerts.RefineryMerged} {
		if _, muted := tbl.Muted[n]; muted {
			t.Errorf("%s muted", n)
		}
	}
	joined := strings.Join(tbl.Warnings, "\n")
	for _, w := range []string{
		`[alerts."condition.notice"] mute = false overrides [conditions] mail = false`,
		`[alerts."nope.never"] names no alert`,
		`[alerts."scheduler.delivery"] is a delivery mechanic`,
	} {
		if !strings.Contains(joined, w) {
			t.Errorf("warnings do not contain %q:\n%s", w, joined)
		}
	}
	if len(tbl.Warnings) != 3 {
		t.Errorf("%d warnings, want 3:\n%s", len(tbl.Warnings), joined)
	}

	line := mutedAlertsLine(tbl)
	for _, w := range []string{"3 ALERT(S) MUTED", "ack_watch.finding (", "condition.out_of_band (", "condition.role_pin_failed (", "alert_muted"} {
		if !strings.Contains(line, w) {
			t.Errorf("startup line %q does not contain %q", line, w)
		}
	}
}

// The listing reads the same table the seam enforces.
func TestAlertSettings_ListsAlertsTableMutes(t *testing.T) {
	state := sandboxHome(t)
	cfg := "[alerts.\"stranded_work.branch\"]\nmute = true\n\n[stall_watch]\nblocked_reminder_enabled = false\n"
	if err := os.WriteFile(filepath.Join(state, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	rows := alerts.Rows(alertSettingsFrom(config.Load()), false)
	if len(rows) < 2 || !rows[0].Muted || !rows[1].Muted || rows[2].Muted {
		t.Fatalf("muted rows not first: %+v", rows[:3])
	}
	if rows[0].Name != alerts.StallWatchBlockedReminder || rows[1].Name != alerts.StrandedWorkBranch {
		t.Errorf("muted rows %s, %s", rows[0].Name, rows[1].Name)
	}
	if rows[1].Source != `[alerts."stranded_work.branch"] mute = true` {
		t.Errorf("source %q", rows[1].Source)
	}
}

// condition.notice muted alone (only [alerts] can say that): the coordinator's
// copy is withheld and recorded as not delivered, the out-of-band copy still
// goes.
func TestConditionMutes_NoticeAloneKeepsOutOfBand(t *testing.T) {
	log := withConditionEventLog(t)
	cfg := defaultsCfg()
	cfg.Alerts = map[string]config.AlertConfig{"condition.notice": {Mute: true, MuteSet: true}}
	tbl := resolveAlertMutes(cfg)
	alerts.SetMutes(tbl.Muted)
	t.Cleanup(func() { alerts.SetMutes(nil) })

	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	applyConditionMutes(a, tbl, "daniel")
	c := testCondition(rowA11HeartbeatWrite, "disk full")
	c.OutOfBand = true
	a.Raise(c, time.Now())

	if len(rec.sent) != 1 || rec.sent[0].to != "daniel" {
		t.Fatalf("sent %+v, want the out-of-band copy only", rec.sent)
	}
	var sawMayor bool
	for _, ev := range conditionEventsFor(t, log, c.ID) {
		if ev.Details["addressee"] == "mayor" {
			sawMayor = true
			if ev.Details["notified"] != false || ev.Details["reason"] != "mail_disabled" {
				t.Errorf("the withheld coordinator copy is recorded as %v", ev.Details)
			}
		}
	}
	if !sawMayor {
		t.Error("no event for the coordinator's copy")
	}
	if n := mutedEvents(t, log, alerts.ConditionNotice); n != 1 {
		t.Errorf("%d alert_muted events for condition.notice, want 1", n)
	}
}

// condition.out_of_band muted through [alerts]: the coordinator still hears,
// the escalation box does not, and the withheld copy is on the spine.
func TestConditionMutes_OutOfBandMutedIsRecorded(t *testing.T) {
	log := withConditionEventLog(t)
	cfg := defaultsCfg()
	cfg.Alerts = map[string]config.AlertConfig{"condition.out_of_band": {Mute: true, MuteSet: true}}
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	line := applyConditionMutes(a, resolveAlertMutes(cfg), "daniel")
	if !strings.Contains(line, `no out-of-band copy`) || !strings.Contains(line, `[alerts."condition.out_of_band"] mute = true`) {
		t.Errorf("summary %q", line)
	}
	c := testCondition(rowA11HeartbeatWrite, "disk full")
	c.OutOfBand = true
	a.Raise(c, time.Now())

	if len(rec.sent) != 1 || rec.sent[0].to != "mayor" {
		t.Fatalf("sent %+v, want mayor only", rec.sent)
	}
	if n := mutedEvents(t, log, alerts.ConditionOutOfBand); n != 1 {
		t.Errorf("%d alert_muted events for condition.out_of_band, want 1", n)
	}
}

// A row muted by [alerts."condition.<id>"] behaves as `disable` listing it,
// records the reason it was given, and leaves other rows alone.
func TestConditionMutes_RowByAlertsName(t *testing.T) {
	log := withConditionEventLog(t)
	cfg := defaultsCfg()
	cfg.Alerts = map[string]config.AlertConfig{"condition." + rowA2SchedulerLoad: {Mute: true, MuteSet: true}}
	rec := &condRecorder{}
	a, _ := newTestAnnunciator(t, rec.send, nil)
	applyConditionMutes(a, resolveAlertMutes(cfg), "daniel")

	a.Raise(testCondition(rowA2SchedulerLoad, "x"), time.Now())
	if len(rec.sent) != 0 {
		t.Fatalf("muted row sent %+v", rec.sent)
	}
	evs := conditionEventsFor(t, log, rowA2SchedulerLoad)
	if len(evs) != 1 || evs[0].Details["reason"] != "mail_disabled" ||
		!strings.Contains(evs[0].Details["mail_error"].(string), "mute = true") {
		t.Errorf("events %v", evs)
	}
	if n := mutedEvents(t, log, alerts.ConditionNotice); n != 1 {
		t.Errorf("%d alert_muted events, want 1", n)
	}
	a.Raise(testCondition(rowA4PromptRefresh, "x"), time.Now())
	if len(rec.sent) != 1 {
		t.Errorf("control row sent %d mails, want 1", len(rec.sent))
	}
}

func TestStallWatchConfigForMutesKeepsTheDetectorRunning(t *testing.T) {
	if !stallWatchConfigForMutes(config.StallWatchConfig{BlockedReminderEnabled: false}).BlockedReminderEnabled {
		t.Error("blocked_reminder_enabled = false still switches the detector off; it is a mute alias")
	}
}

func TestEffectiveBlindWatchFollowsTheTable(t *testing.T) {
	cfg := defaultsCfg()
	cfg.Alerts = map[string]config.AlertConfig{"blind_watch.out_of_band": {Mute: true, MuteSet: true}}
	got := effectiveBlindWatch(cfg.BlindWatch, resolveAlertMutes(cfg))
	if !got.Mail || got.OutOfBand {
		t.Errorf("got mail=%v out_of_band=%v, want true/false", got.Mail, got.OutOfBand)
	}
	cfg = defaultsCfg()
	cfg.BlindWatch.Mail = false
	cfg.Alerts = map[string]config.AlertConfig{"blind_watch.finding": {Mute: false, MuteSet: true}}
	if got := effectiveBlindWatch(cfg.BlindWatch, resolveAlertMutes(cfg)); !got.Mail {
		t.Error("an [alerts] unmute of blind_watch.finding left the startup line saying ALL mail is off")
	}
}

func mutedEvents(t *testing.T, log string, name alerts.Name) int {
	t.Helper()
	n := 0
	if err := events.ScanFile(log, func(ev events.Event) {
		if ev.EventType == alerts.EventMuted && ev.Details["alert"] == string(name) {
			n++
		}
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

// condition.notice muted for a row with no out-of-band copy is the whole raise
// muted: nothing remembered as delivered, and no wake for a notice never sent.
func TestConditionMutes_NoticeMutedWithoutOutOfBandIsWhollyDisabled(t *testing.T) {
	log := withConditionEventLog(t)
	cfg := defaultsCfg()
	cfg.Alerts = map[string]config.AlertConfig{"condition.notice": {Mute: true, MuteSet: true}}
	tbl := resolveAlertMutes(cfg)
	alerts.SetMutes(tbl.Muted)
	t.Cleanup(func() { alerts.SetMutes(nil) })

	rec := &condRecorder{}
	wk := &wakeRecorder{}
	a, path := newTestAnnunciator(t, rec.send, wk.nudge)
	applyConditionMutes(a, tbl, "daniel")
	c := testCondition(rowA2SchedulerLoad, "boom")
	c.Wake = true
	t0 := time.Now()
	a.Raise(c, t0)
	a.retryWakes(t0.Add(time.Minute))

	if len(rec.sent) != 0 || len(wk.woken) != 0 {
		t.Fatalf("sent %+v woke %v, want nothing", rec.sent, wk.woken)
	}
	if _, stored := loadConditionNoticesAfterFlush(a, path).Conditions[c.ID]; stored {
		t.Error("a muted condition was stored as delivered")
	}
	evs := conditionEventsFor(t, log, c.ID)
	if len(evs) != 1 || evs[0].Details["reason"] != "mail_disabled" {
		t.Errorf("events %v", evs)
	}
	if n := mutedEvents(t, log, alerts.ConditionNotice); n != 1 {
		t.Errorf("%d alert_muted events, want 1", n)
	}
}
