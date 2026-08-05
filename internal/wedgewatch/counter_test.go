package wedgewatch

import (
	"testing"
	"time"
)

// TestParseDeclaredWorkReadsTheIncidentCounter is the positive control for the
// half of mg-fc8d that needs no enumeration. The literal string is the one read
// off the wedged terminal on 2026-08-04, rendered through the same ANSI the TUI
// actually emits.
func TestParseDeclaredWorkReadsTheIncidentCounter(t *testing.T) {
	got, ok := ParseDeclaredWork(wedgedLoginPTY("3m 2s"))
	if !ok {
		t.Fatal("could not parse \"Baked for 3m 2s\" out of the 2026-08-04 screen — " +
			"the cross-check is blind without it, and a blind cross-check reports healthy")
	}
	if want := 3*time.Minute + 2*time.Second; got != want {
		t.Errorf("declared = %s, want %s", got, want)
	}
}

// TestParseDeclaredWorkFormVariants covers the renderings the harness uses.
// Each is a positive control for a stem in defaultStems: if a stem stops
// matching, the counter becomes UNREADABLE, which the watcher reports as an
// error rather than swallowing.
func TestParseDeclaredWorkFormVariants(t *testing.T) {
	cases := []struct {
		name string
		pty  []byte
		want time.Duration
	}{
		{"baked-for seconds only", wedgedLoginPTY("45s"), 45 * time.Second},
		{"baked-for hours", wedgedLoginPTY("13h 44m 2s"), 13*time.Hour + 44*time.Minute + 2*time.Second},
		{"baked-for hours and minutes", wedgedLoginPTY("7h 2m"), 7*time.Hour + 2*time.Minute},
		{"spinner parenthetical", workingPTY("2m 56s"), 2*time.Minute + 56*time.Second},
		{"spinner parenthetical seconds", workingPTY("12s"), 12 * time.Second},
		{"rating dialog screen", ratingDialogPTY("1m 4s"), time.Minute + 4*time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseDeclaredWork(tc.pty)
			if !ok {
				t.Fatalf("could not parse a counter out of the %s fixture", tc.name)
			}
			if got != tc.want {
				t.Errorf("declared = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestParseDeclaredWorkReportsUnreadableRatherThanZero is the absence-as-
// evidence guard, inherited from internal/credexpiry. A harness that renames
// its status line must make this check BLIND and say so — never make it read a
// comfortable zero.
func TestParseDeclaredWorkReportsUnreadableRatherThanZero(t *testing.T) {
	if d, ok := ParseDeclaredWork(noCounterPTY()); ok {
		t.Fatalf("parsed %s out of a buffer with no counter — a fabricated number here would "+
			"drive a fabricated verdict", d)
	}
	if _, ok := ParseDeclaredWork(nil); ok {
		t.Error("parsed a counter out of an empty buffer")
	}
}

// TestParseDeclaredWorkIgnoresDistantNumbers pins the adjacency requirement.
// An agent that ran `pogo agent list` has "13h44m" in its own PTY; that is not
// its work counter and must not be read as one.
func TestParseDeclaredWorkIgnoresDistantNumbers(t *testing.T) {
	pty := []byte("teaa9   status=running   uptime=13h44m   last-activity=just now\r\n" +
		"● Bash(pogo agent list)\r\n")
	if d, ok := ParseDeclaredWork(pty); ok {
		t.Errorf("parsed %s from an unrelated uptime column — the counter must be adjacent to a stem", d)
	}
}

// --- the cross-check itself -------------------------------------------------

// TestDiscrepancyFiresOnBothIncidents is the positive control for the
// counter/uptime cross-check, using the two live signatures mg-fc8d recorded.
func TestDiscrepancyFiresOnBothIncidents(t *testing.T) {
	cases := []struct {
		name   string
		uptime time.Duration
		declar time.Duration
	}{
		{"2026-08-04, 13h44m beside 3m 2s", 13*time.Hour + 44*time.Minute, 3*time.Minute + 2*time.Second},
		{"2026-08-05, 7h beside 2m 56s", 7 * time.Hour, 2*time.Minute + 56*time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fire, why := Discrepancy(DiscrepancyInput{
				Uptime:       tc.uptime,
				Declared:     tc.declar,
				DeclaredRead: true,
				FrozenFor:    45 * time.Minute,
			}, Thresholds{})
			if !fire {
				t.Fatalf("the cross-check did NOT fire on the live signature: %s", why)
			}
		})
	}
}

// TestDiscrepancyDoesNotFireOnAHealthyAgentMidTurn is the negative control that
// justifies the whole freeze design, and it is the one to read if you are
// tempted to simplify this rule down to a ratio.
//
// A perfectly healthy agent seven hours into its life and three seconds into a
// new turn has a ratio of 8400 — far past any threshold. The declared counter
// measures ONE TURN, not cumulative work, so on a ratio-only rule EVERY agent
// in the fleet reports constantly and the detector is muted inside a day. What
// made 13h44m beside "2m 56s" damning was that the counter did not move.
func TestDiscrepancyDoesNotFireOnAHealthyAgentMidTurn(t *testing.T) {
	fire, why := Discrepancy(DiscrepancyInput{
		Uptime:       7 * time.Hour,
		Declared:     3 * time.Second,
		DeclaredRead: true,
		FrozenFor:    0, // the counter changed at this very sample
	}, Thresholds{})
	if fire {
		t.Fatalf("fired on a healthy agent three seconds into a new turn (%s) — "+
			"this rule must gate on the counter being FROZEN, not on the ratio", why)
	}
}

// TestDiscrepancyGuards pins each guard independently, so a future edit that
// removes one fails here rather than in production.
func TestDiscrepancyGuards(t *testing.T) {
	base := DiscrepancyInput{
		Uptime:       13 * time.Hour,
		Declared:     3 * time.Minute,
		DeclaredRead: true,
		FrozenFor:    45 * time.Minute,
	}
	t.Run("young process", func(t *testing.T) {
		in := base
		in.Uptime = 20 * time.Minute
		if fire, _ := Discrepancy(in, Thresholds{}); fire {
			t.Error("fired on a process younger than MinUptime; spawn is the noisiest part of an agent's life")
		}
	})
	t.Run("counter still moving", func(t *testing.T) {
		in := base
		in.FrozenFor = 4 * time.Minute
		if fire, _ := Discrepancy(in, Thresholds{}); fire {
			t.Error("fired before the freeze hold-down; a merely-idle agent still runs a turn on every mail-check fire")
		}
	})
	t.Run("ratio too small", func(t *testing.T) {
		in := base
		in.Declared = 2 * time.Hour // 13h/2h = 6.5x, below the 20x threshold
		if fire, _ := Discrepancy(in, Thresholds{}); fire {
			t.Error("fired on a counter only slightly behind uptime")
		}
	})
	t.Run("counter unreadable", func(t *testing.T) {
		in := base
		in.DeclaredRead = false
		fire, why := Discrepancy(in, Thresholds{})
		if fire {
			t.Error("fired on an unreadable counter — a verdict with no evidence behind it")
		}
		if why == "" {
			t.Error("an unreadable counter must SAY it was unreadable, not fall silent")
		}
	})
	t.Run("zero declared beside hours of uptime", func(t *testing.T) {
		in := base
		in.Declared = 0
		if fire, why := Discrepancy(in, Thresholds{}); !fire {
			t.Errorf("a counter frozen at zero for 45m beside 13h of uptime is the same fault "+
				"in its most extreme form and must fire: %s", why)
		}
	})
}
