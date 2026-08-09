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

// --- the 2026-08-09 harness (mg-20eb) ---------------------------------------

// TestParseDeclaredWorkReadsTheCurrentHarness is the positive control for the
// renderings that made this detector blind on 100% of agents from its first
// pass. Each case is a stem that did not exist when all four originals missed
// at once.
func TestParseDeclaredWorkReadsTheCurrentHarness(t *testing.T) {
	cases := []struct {
		name string
		pty  []byte
		want time.Duration
	}{
		{"in-flight, token parenthetical", currentSpinnerPTY("11m53s", "cerebrating"), 11*time.Minute + 53*time.Second},
		{"in-flight, verb is randomized", currentSpinnerPTY("2m 56s", "crystallizing"), 2*time.Minute + 56*time.Second},
		{"in-flight, another verb", currentSpinnerPTY("45s", "slithering"), 45 * time.Second},
		{"completed turn, worked for", currentWorkedForPTY("55s"), 55 * time.Second},
		{"completed turn, hours", currentWorkedForPTY("13h 44m 2s"), 13*time.Hour + 44*time.Minute + 2*time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ParseDeclaredWork(tc.pty)
			if !ok {
				t.Fatalf("could not parse a counter out of the %s fixture — this is mg-20eb: "+
					"every stem missing at once takes the cross-check, the load-bearing half of "+
					"this detector, off the air entirely", tc.name)
			}
			if got != tc.want {
				t.Errorf("declared = %s, want %s", got, tc.want)
			}
		})
	}
}

// TestTheHintBarIsNotAnAnchor is the negative control for the change that
// caused mg-20eb, and the one to read before adding a stem.
//
// "esc to interrupt" is now rendered permanently, on every agent, attached to
// no counter. A stem on a permanently-rendered string cannot go quiet the way
// markers.go's can — it matches forever and reports whatever number drifts into
// its window. Here the window holds the spinner's repaint digits, measured off
// a live agent: bare numbers with no unit. They must not become a verdict.
func TestTheHintBarIsNotAnAnchor(t *testing.T) {
	if d, ok := ParseDeclaredWork(hintBarOnlyPTY()); ok {
		t.Errorf("parsed %s from a screen whose only anchor is the permanent hint bar — the number "+
			"came from the spinner's repaint traffic, and a fabricated counter drives a "+
			"fabricated verdict", d)
	}
}

// TestTheLiveCounterOutranksThePreviousTurnsTotal pins the stem ORDER, which is
// load-bearing rather than cosmetic.
//
// On the current harness a mid-turn screen carries both: "(30m · ↓ tokens)" for
// the turn in flight, and "worked for 55s" left over from the turn before. The
// second is frozen for the whole of the current turn, so reading it beside a
// large uptime is indistinguishable from a wedge — a 30-minute honest turn on a
// 9-hour-old agent would report. The live counter must win.
func TestTheLiveCounterOutranksThePreviousTurnsTotal(t *testing.T) {
	pty := append(currentWorkedForPTY("55s"), currentSpinnerPTY("30m", "cerebrating")...)
	got, ok := ParseDeclaredWork(pty)
	if !ok {
		t.Fatal("could not parse a counter from a mid-turn screen")
	}
	if got != 30*time.Minute {
		t.Errorf("declared = %s, want 30m — the PREVIOUS turn's frozen total was preferred over the "+
			"live counter, which reads a long honest turn as a wedge", got)
	}
}

// TestTheLegacyStemsStillRead keeps the older renderings working. The daemon on
// this box ran ten days behind main; a stem table that only knows today's
// harness is one deploy lag away from the same blindness in the other
// direction.
func TestTheLegacyStemsStillRead(t *testing.T) {
	if d, ok := ParseDeclaredWork(wedgedLoginPTY("3m 2s")); !ok || d != 3*time.Minute+2*time.Second {
		t.Errorf("the 2026-08-04 incident string no longer parses: %s ok=%v", d, ok)
	}
	if d, ok := ParseDeclaredWork(workingPTY("2m 56s")); !ok || d != 2*time.Minute+56*time.Second {
		t.Errorf("the older esc-to-interrupt parenthetical no longer parses: %s ok=%v", d, ok)
	}
}

// TestAQuotedLegacyCounterCannotOutrankTheLiveOne is this fix checked against
// the fault it repairs.
//
// The remedy for mg-20eb is more stems, and the failure mode of a stem is that
// it anchors on something that is not a counter. lastDurationNear's
// last-occurrence rule guards that WITHIN a stem — the live status line
// repaints at the tail and beats an earlier quotation of the same phrase — but
// not ACROSS stems, where priority decides and position does not.
//
// The fixture is this branch's own author: a polecat editing counter.go, with
// "Baked for 3m 2s" in its scrollback from reading the file and a live turn
// counter at the tail. Reading the quotation would report a counter frozen at
// 3m2s for as long as the file stayed on screen.
func TestAQuotedLegacyCounterCannotOutrankTheLiveOne(t *testing.T) {
	var b []byte
	b = append(b, []byte("● Read(internal/wedgewatch/counter.go)\r\n"+
		"  ⎿  // \"Baked for 3m 2s\" — the completed-turn form\r\n"+
		"     {text: \"bakedfor\", after: 24},\r\n")...)
	b = append(b, currentSpinnerPTY("22m 8s", "cerebrating")...)

	got, ok := ParseDeclaredWork(b)
	if !ok {
		t.Fatal("could not parse a counter from a screen quoting one")
	}
	if got != 22*time.Minute+8*time.Second {
		t.Errorf("declared = %s, want 22m8s — a legacy counter QUOTED in the transcript outranked the "+
			"live one at the tail. Stem priority beats buffer position, so a higher-priority stem "+
			"mentioned once anywhere wins; that is why the current harness's stems must come first.", got)
	}
}
