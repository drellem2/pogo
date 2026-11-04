package main

import (
	"encoding/json"
	"net/http"
	"sync/atomic"

	"github.com/drellem2/pogo/internal/alerts"
	"github.com/drellem2/pogo/internal/config"
)

// alertSettings is the config the running daemon resolved at startup, as the
// alert listing needs it (drellem2/pogo#262). Set once in main; read by
// GET /alerts. Nil until then — the handler answers 503 rather than listing
// against a config the daemon is not running with.
var alertSettings atomic.Pointer[alerts.Settings]

// alertSettingsFrom extracts the keys that decide who an alert reaches, or
// whether it is sent. The notify_to map is keyed as alerts.Def.Key spells it;
// Muted and Routes are the resolved mute and route tables, [alerts] entries
// and their aliases.
func alertSettingsFrom(cfg *config.Config) alerts.Settings {
	s := alerts.Settings{
		Coordinator:   cfg.Agents.CoordinatorName(),
		EscalationBox: cfg.Agents.EscalationBoxName(),
		NotifyTo:      watcherNotifyTo(cfg),
		// The same table the send seam enforces (alertmutes.go), so the
		// listing cannot say an alert is live while the seam withholds it.
		Muted: resolveAlertMutes(cfg).Muted,
	}
	// Likewise the routes (alertroutes.go). This does not probe mg: main
	// fills RouteInvalid from the startup check, whose result the startup
	// warning printed.
	rt := resolveAlertRoutes(cfg, nil)
	s.Routes = rt.Routes
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
