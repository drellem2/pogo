package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/config"
)

// An install with no config of its own: every alert reaches its built-in
// default and nothing is muted (drellem2/pogo#262 slice 1 — no behaviour
// change, and the listing says so).
func TestAlertSettings_NoConfigIsAllDefaults(t *testing.T) {
	sandboxHome(t)
	rows := alerts.Rows(alertSettingsFrom(config.Load()), true)
	if len(rows) != len(alerts.All()) {
		t.Fatalf("listed %d of %d declarations", len(rows), len(alerts.All()))
	}
	for _, r := range rows {
		if r.Muted || r.Source != "default" {
			t.Errorf("%s: muted=%v source=%q on an unconfigured install", r.Name, r.Muted, r.Source)
		}
	}
}

// The existing keys are reported, not reinterpreted: a notify_to and the
// legacy mail switches show up on the rows they already govern.
func TestAlertSettings_ExistingKeysAreReported(t *testing.T) {
	state := sandboxHome(t)
	cfg := "[ack_watch]\nnotify_to = \"pm-pogo\"\n\n[conditions]\nmail = false\n\n[blind_watch]\nmail = false\n"
	if err := os.WriteFile(filepath.Join(state, "config.toml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	s := alertSettingsFrom(config.Load())
	by := map[alerts.Name]alerts.Row{}
	for _, r := range alerts.Rows(s, false) {
		by[r.Name] = r
	}
	// Since slice 3 notify_to is the route alias, named as config.toml spells it.
	if r := by[alerts.AckWatchFinding]; r.Source != "[ack_watch] notify_to" || r.Route != "pm-pogo" || r.Muted ||
		r.Effective != "pm-pogo; also human when it escalates" {
		t.Errorf("ack_watch.finding: %+v", r)
	}
	for _, n := range []alerts.Name{alerts.ConditionNotice, alerts.ConditionOutOfBand, alerts.BlindWatchFinding, alerts.BlindWatchCleared} {
		if !by[n].Muted {
			t.Errorf("%s not reported muted: %+v", n, by[n])
		}
	}
}

func TestHandleAlerts(t *testing.T) {
	alertSettings.Store(nil)
	rec := httptest.NewRecorder()
	handleAlerts(rec, httptest.NewRequest(http.MethodGet, "/alerts", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("before config is resolved: %d, want 503", rec.Code)
	}

	s := alerts.Settings{Coordinator: "mayor", EscalationBox: "human"}
	alertSettings.Store(&s)
	t.Cleanup(func() { alertSettings.Store(nil) })
	for _, tc := range []struct {
		url      string
		delivery bool
	}{{"/alerts", false}, {"/alerts?all=1", true}} {
		rec := httptest.NewRecorder()
		handleAlerts(rec, httptest.NewRequest(http.MethodGet, tc.url, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.url, rec.Code, rec.Body)
		}
		var rows []alerts.Row
		if err := json.Unmarshal(rec.Body.Bytes(), &rows); err != nil {
			t.Fatal(err)
		}
		found := false
		for _, r := range rows {
			if r.Name == alerts.SchedulerDelivery {
				found = true
			}
		}
		if found != tc.delivery {
			t.Errorf("%s: scheduler.delivery listed=%v, want %v", tc.url, found, tc.delivery)
		}
	}
}
