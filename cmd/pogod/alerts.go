package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/config"
)

// alertSettings is the config the running daemon resolved at startup, as the
// alert listing needs it (drellem2/pogo#262). Set once in main; read by
// GET /alerts. Nil until then — the handler answers 503 rather than listing
// against a config the daemon is not running with.
var alertSettings atomic.Pointer[alerts.Settings]

// alertSettingsFrom extracts the existing keys that already decide who an
// alert reaches, or whether it is sent. Slice 1 reports them; it changes no
// send. The notify_to map is keyed as alerts.Def.Key spells it.
func alertSettingsFrom(cfg *config.Config) alerts.Settings {
	s := alerts.Settings{
		Coordinator:   cfg.Agents.CoordinatorName(),
		EscalationBox: cfg.Agents.EscalationBoxName(),
		NotifyTo: map[string]string{
			"gh_teardown.notify_to":       cfg.GHTeardown.NotifyTo,
			"gh_intake.notify_to":         cfg.GHIntake.NotifyTo,
			"carrier_drift.notify_to":     cfg.CarrierDrift.NotifyTo,
			"review_decl.notify_to":       cfg.ReviewDecl.NotifyTo,
			"ack_watch.notify_to":         cfg.AckWatch.NotifyTo,
			"deaf_watch.notify_to":        cfg.DeafWatch.NotifyTo,
			"absent_watch.notify_to":      cfg.AbsentWatch.NotifyTo,
			"progress_watch.notify_to":    cfg.ProgressWatch.NotifyTo,
			"first_turn.notify_to":        cfg.FirstTurn.NotifyTo,
			"mid_session_wedge.notify_to": cfg.MidSessionWedge.NotifyTo,
		},
		Muted: map[alerts.Name]string{},
	}
	if !cfg.Conditions.Mail {
		s.Muted[alerts.ConditionNotice] = "conditions.mail = false"
		s.Muted[alerts.ConditionOutOfBand] = "conditions.mail = false"
	} else if !cfg.Conditions.OutOfBand {
		s.Muted[alerts.ConditionOutOfBand] = "conditions.out_of_band = false"
	}
	if len(cfg.Conditions.Disable) > 0 {
		// Per-row, so the alert as a whole is not muted; say which rows are.
		if _, muted := s.Muted[alerts.ConditionNotice]; !muted {
			s.Muted[alerts.ConditionNotice] = "conditions.disable = [" + strings.Join(cfg.Conditions.Disable, ", ") + "] (those rows only)"
		}
	}
	if cfg.BlindWatch.Enabled && !cfg.BlindWatch.Mail {
		s.Muted[alerts.BlindWatchFinding] = "blind_watch.mail = false"
		s.Muted[alerts.BlindWatchCleared] = "blind_watch.mail = false"
	}
	return s
}

// handleAlerts answers GET /alerts: one row per declared alert with its
// default and effective recipient and its mute state. ?all=1 adds the
// delivery mechanics (scheduler deliveries) that send mail but are not alerts.
func handleAlerts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s := alertSettings.Load()
	if s == nil {
		http.Error(w, "pogod has not resolved its config yet", http.StatusServiceUnavailable)
		return
	}
	all := r.URL.Query().Get("all") != ""
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(alerts.Rows(*s, all))
}
