package config

import "testing"

// drellem2/pogo#244: [blind_watch] mail / out_of_band switch blind-watch's mail
// off entirely or only its escalation-box copy, with the same names and
// meaning as [conditions] (#238).

func TestBlindWatchMailDefaults(t *testing.T) {
	layeredSandbox(t) // no config written

	cfg := Load()

	if !cfg.BlindWatch.Mail {
		t.Error("[blind_watch] mail defaults off; it ships on")
	}
	if !cfg.BlindWatch.OutOfBand {
		t.Error("[blind_watch] out_of_band defaults off; it ships on")
	}
}

func TestBlindWatchMailOverrides(t *testing.T) {
	_, home := layeredSandbox(t)
	write(t, home, "[blind_watch]\nmail = false\nout_of_band = false\n")

	cfg := Load()

	if cfg.BlindWatch.Mail {
		t.Error("mail = true, want false")
	}
	if cfg.BlindWatch.OutOfBand {
		t.Error("out_of_band = true, want false")
	}
	if !cfg.BlindWatch.Enabled {
		t.Error("the mail switches turned the runner off; sampling must continue")
	}
}

// A layer that omits a key keeps the base layer's value: both switches default
// TRUE, so a zero-value merge would silently flip them.
func TestBlindWatchMailLayering(t *testing.T) {
	xdg, home := layeredSandbox(t)
	write(t, xdg, "[blind_watch]\nmail = false\nout_of_band = false\n")
	write(t, home, "[blind_watch]\ninterval = \"5m\"\n")

	cfg := Load()

	if cfg.BlindWatch.Mail {
		t.Error("a layer that omits mail re-enabled it")
	}
	if cfg.BlindWatch.OutOfBand {
		t.Error("a layer that omits out_of_band re-enabled it")
	}
}
