package config

import (
	"reflect"
	"testing"
)

// drellem2/pogo#262 slice 2: [alerts."<family>.<kind>"] mute.

func TestAlertsTableParsesQuotedDottedNames(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[alerts.\"ack_watch.finding\"]\nmute = true\n\n"+
		"[alerts.\"condition.out_of_band\"]\nmute = false\n\n"+
		"[alerts.\"refinery.merged\"]\nroute = \"x\"\n\n"+ // route only: no mute
		"[ack_watch]\nnotify_to = \"pm-pogo\"\n")

	cfg := Load()

	want := map[string]AlertConfig{
		"ack_watch.finding":     {Mute: true, MuteSet: true},
		"condition.out_of_band": {Mute: false, MuteSet: true},
		"refinery.merged":       {Route: "x", RouteSet: true},
	}
	got := map[string]AlertConfig{}
	for k, v := range cfg.Alerts {
		if v.MuteSet || v.RouteSet {
			got[k] = v
		}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("alerts = %+v, want %+v", cfg.Alerts, want)
	}
	// The section after an [alerts] table is parsed as itself again.
	if cfg.AckWatch.NotifyTo != "pm-pogo" {
		t.Errorf("ack_watch.notify_to = %q after an [alerts] table", cfg.AckWatch.NotifyTo)
	}
}

func TestAlertsDefaultIsEmpty(t *testing.T) {
	layeredSandbox(t)
	for k, v := range Load().Alerts {
		if v.MuteSet {
			t.Errorf("unconfigured install mutes %s", k)
		}
	}
}

// A later layer overrides only the names it states, and can unmute what the
// base layer muted.
func TestAlertsLayering(t *testing.T) {
	xdg, home := layeredSandbox(t)
	write(t, xdg, "[alerts.\"ack_watch.finding\"]\nmute = true\n[alerts.\"deaf_watch.finding\"]\nmute = true\n")
	write(t, home, "[alerts.\"deaf_watch.finding\"]\nmute = false\n")

	cfg := Load()

	if a := cfg.Alerts["ack_watch.finding"]; !a.Mute || !a.MuteSet {
		t.Errorf("base layer's mute lost: %+v", a)
	}
	if a := cfg.Alerts["deaf_watch.finding"]; a.Mute || !a.MuteSet {
		t.Errorf("override layer's mute = false lost: %+v", a)
	}
}

// drellem2/pogo#262 slice 3: route. The value is kept as written (pogod
// validates it), and mute and route layer independently.
func TestAlertsRouteParsesAndLayers(t *testing.T) {
	xdg, home := layeredSandbox(t)
	write(t, xdg, "[alerts.\"refinery.merged\"]\nmute = true\nroute = \"pm-pogo\"\n"+
		"[alerts.\"stranded_work.branch\"]\nroute = \"$escalation_box\"\n")
	write(t, home, "[alerts.\"refinery.merged\"]\nroute = \"\"\n"+
		"[alerts.\"stranded_work.branch\"]\nmute = false\n")

	cfg := Load()

	if a := cfg.Alerts["refinery.merged"]; !a.Mute || !a.MuteSet || a.Route != "" || !a.RouteSet {
		t.Errorf("refinery.merged: base mute kept, override clears the route: %+v", a)
	}
	if a := cfg.Alerts["stranded_work.branch"]; a.Route != "$escalation_box" || !a.RouteSet || a.Mute || !a.MuteSet {
		t.Errorf("stranded_work.branch: base route kept under an override's mute: %+v", a)
	}
}
