package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// drellem2/pogo#244: every blind-watch state prints a startup line, so "off"
// is no longer indistinguishable from "no such switch".
func TestBlindWatchStartupLine(t *testing.T) {
	on := config.BlindWatchConfig{Enabled: true, Mail: true, OutOfBand: true}

	cases := []struct {
		name    string
		cfg     config.BlindWatchConfig
		want    []string
		notWant []string
	}{
		{"disabled", config.BlindWatchConfig{Enabled: false, Mail: true, OutOfBand: true},
			[]string{"blind-watch NOT armed (disabled by config", "enabled = false"}, []string{"blind-watch enabled"}},
		{"armed", on, []string{"blind-watch enabled", "routes to daniel"}, []string{"OFF", "out_of_band"}},
		{"mail off", func() config.BlindWatchConfig { c := on; c.Mail = false; return c }(),
			[]string{"blind-watch enabled", "ALL blind-watch mail is OFF ([blind_watch] mail = false)", "reason=mail_disabled"},
			[]string{"out_of_band = false"}},
		{"out of band off", func() config.BlindWatchConfig { c := on; c.OutOfBand = false; return c }(),
			[]string{"blind-watch enabled", "no mail to daniel ([blind_watch] out_of_band = false)", "mail_suppressed"},
			[]string{"mail = false"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			line := blindWatchStartupLine(tc.cfg, "daniel")
			for _, w := range tc.want {
				if !strings.Contains(line, w) {
					t.Errorf("line %q lacks %q", line, w)
				}
			}
			for _, w := range tc.notWant {
				if strings.Contains(line, w) {
					t.Errorf("line %q should not contain %q", line, w)
				}
			}
		})
	}
}
