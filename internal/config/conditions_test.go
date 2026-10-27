package config

import (
	"reflect"
	"testing"
)

// drellem2/pogo#238: [conditions] switches the condition annunciator's mail off
// entirely, per condition, or only its out-of-band copy.

func TestConditionsDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.Conditions.Mail {
		t.Error("[conditions] mail defaults off; it ships on")
	}
	if !cfg.Conditions.OutOfBand {
		t.Error("[conditions] out_of_band defaults off; it ships on (mg-875d)")
	}
	if len(cfg.Conditions.Disable) != 0 {
		t.Errorf("disable = %v, want empty", cfg.Conditions.Disable)
	}
}

func TestConditionsOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[agents]\nescalation_box = \"operator\"\n"+
		"[conditions]\nmail = false\nout_of_band = false\n"+
		"disable = [\"scheduler_load_failed\", \"autostart_failed\"]\n")

	cfg := Load()

	if cfg.Conditions.Mail {
		t.Error("mail = true, want false")
	}
	if cfg.Conditions.OutOfBand {
		t.Error("out_of_band = true, want false")
	}
	if want := []string{"scheduler_load_failed", "autostart_failed"}; !reflect.DeepEqual(cfg.Conditions.Disable, want) {
		t.Errorf("disable = %v, want %v", cfg.Conditions.Disable, want)
	}
	// The section is about condition mail only: the escalation box every other
	// watcher escalates to is untouched by it.
	if got := cfg.Agents.EscalationBoxName(); got != "operator" {
		t.Errorf("escalation_box = %q, want operator — [conditions] must not move it", got)
	}
}

// A layer that omits a key keeps the base layer's value (both switches default
// TRUE, so a zero-value merge would silently flip them), and an explicit
// `disable = []` clears the base layer's list.
func TestConditionsLayering(t *testing.T) {
	xdg, home := layeredSandbox(t)
	write(t, xdg, "[conditions]\nmail = false\nout_of_band = false\ndisable = [\"role_pin_failed\"]\n")
	write(t, home, "[conditions]\ndisable = []\n")

	cfg := Load()

	if cfg.Conditions.Mail {
		t.Error("a layer that omits mail re-enabled it")
	}
	if cfg.Conditions.OutOfBand {
		t.Error("a layer that omits out_of_band re-enabled it")
	}
	if len(cfg.Conditions.Disable) != 0 {
		t.Errorf("disable = %v, want the override layer's empty list", cfg.Conditions.Disable)
	}
}
