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
		"[alerts.\"refinery.merged\"]\nroute = [\"x\"]\n\n"+ // no mute key: not an entry for slice 2
		"[ack_watch]\nnotify_to = \"pm-pogo\"\n")

	cfg := Load()

	want := map[string]AlertConfig{
		"ack_watch.finding":     {Mute: true, MuteSet: true},
		"condition.out_of_band": {Mute: false, MuteSet: true},
	}
	got := map[string]AlertConfig{}
	for k, v := range cfg.Alerts {
		if v.MuteSet {
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
