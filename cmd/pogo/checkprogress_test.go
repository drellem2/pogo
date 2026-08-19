package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/progresswatch"
)

func progressReading(t *testing.T, s progresswatch.Snapshot) progresswatch.Reading {
	t.Helper()
	return progresswatch.Evaluate(s, progresswatch.Thresholds{})
}

// theIncident is the 2026-08-14 05:17Z state: seven workers PTY-active within
// four minutes, none having written a file in fifteen, 0.10 of 10 cores, and
// nothing landed in half an hour.
func theIncident(now time.Time) progresswatch.Snapshot {
	s := progresswatch.Snapshot{
		Now:              now,
		HostCores:        10,
		WorkerCores:      0.10,
		CoresKnown:       true,
		LastProgress:     now.Add(-31 * time.Minute),
		LastProgressWhat: "merge mr-0f2a (polecat-p27c0) landed",
		ProgressKnown:    true,
		ProgressSince:    now.Add(-6 * time.Hour),
	}
	for _, n := range []string{"p1", "p2", "p3", "p4", "p5", "p6", "p7"} {
		s.Workers = append(s.Workers, progresswatch.Worker{
			Name: n, Age: 40 * time.Minute,
			PTYIdle: 4 * time.Minute, HasOutput: true,
			WriteIdle: 15 * time.Minute, HasWrites: true, WritesKnown: true,
		})
	}
	return s
}

// TestRenderNamesEveryMeasurement. The whole point of the command is that the
// four readings appear together — mayor had to take them from three separate
// commands and hold them in their head.
func TestRenderNamesEveryMeasurement(t *testing.T) {
	now := time.Date(2026, 8, 14, 5, 18, 0, 0, time.UTC)
	out := renderProgressReading(progressReading(t, theIncident(now)))
	for _, want := range []string{
		"7 of 7 judged worker(s) PTY-active and writing nothing",
		"p1, p2, p3",
		"worker subtrees at 0.10 of 10 cores",
		"nothing in 31m0s",
		"merge mr-0f2a",
		"FLEET IS ALIVE AND LANDING NOTHING",
		"waiting on the same remote",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("rendered reading missing %q:\n%s", want, out)
		}
	}
}

// TestCleanRenderStillPrintsTheInputs. A check that shows its inputs only when
// it fires cannot be used to chase a hunch, which is the only reason this state
// was ever found.
func TestCleanRenderStillPrintsTheInputs(t *testing.T) {
	now := time.Date(2026, 8, 14, 5, 18, 0, 0, time.UTC)
	s := theIncident(now)
	s.WorkerCores = 6.2
	out := renderProgressReading(progressReading(t, s))
	if strings.Contains(out, "FLEET IS ALIVE AND LANDING NOTHING") {
		t.Fatalf("a fleet at 6.2 cores must not read as a finding:\n%s", out)
	}
	for _, want := range []string{
		"worker subtrees at 6.20 of 10 cores",
		"nothing in 31m0s",
		"No finding. What rules it out:",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("clean render missing %q:\n%s", want, out)
		}
	}
}

// TestBlindRenderDoesNotReadAsClean is the rule this command must not break: a
// run that could not measure a member of the conjunction has NOT found a
// healthy fleet, and must not look like one on a terminal.
func TestBlindRenderDoesNotReadAsClean(t *testing.T) {
	now := time.Date(2026, 8, 14, 5, 18, 0, 0, time.UTC)
	s := theIncident(now)
	s.CoresKnown = false
	s.CoresError = "ps resolution cannot resolve a 1s window"
	out := renderProgressReading(progressReading(t, s))
	if strings.Contains(out, "No finding") {
		t.Fatalf("a blind run rendered as a clean one:\n%s", out)
	}
	for _, want := range []string{"NOT MEASURED", "cannot resolve", "That is not a clean fleet"} {
		if !strings.Contains(out, want) {
			t.Errorf("blind render missing %q:\n%s", want, out)
		}
	}
}

// TestRenderStatesWhoItDeclinedToJudge: a young worker's silence is not
// evidence, and a reading that silently dropped it would understate its own
// population.
func TestRenderStatesWhoItDeclinedToJudge(t *testing.T) {
	now := time.Date(2026, 8, 14, 5, 18, 0, 0, time.UTC)
	s := theIncident(now)
	s.Workers[0].Age = 2 * time.Minute
	out := renderProgressReading(progressReading(t, s))
	if !strings.Contains(out, "7 live, 1 too young to judge") {
		t.Errorf("render did not state the excluded worker:\n%s", out)
	}
}

// TestTheThreeRendersAreGreppableApart is the general form of the rule
// TestBlindRenderDoesNotReadAsClean pins for one phrase, and it is here because
// the rescued renderer broke that rule by writing the blind paragraph as the
// CLEAN headline plus a qualifier ("No finding is possible from this run").
// That reads as a near-miss and greps as a hit: the three states are
// distinguished for a reader who reads the whole paragraph, and not at all for
// one who skims the first two words or runs `grep "No finding"` over a log.
//
// A detector whose healthy state and whose I-COULD-NOT-TELL state share a lead
// phrase has reintroduced, in its own output, the false-green it was built to
// make impossible. So each state's headline must appear in that state's render
// and in NEITHER of the other two.
func TestTheThreeRendersAreGreppableApart(t *testing.T) {
	now := time.Date(2026, 8, 14, 5, 18, 0, 0, time.UTC)

	finding := theIncident(now)

	clean := theIncident(now)
	clean.WorkerCores = 6.2

	blind := theIncident(now)
	blind.CoresKnown = false
	blind.CoresError = "ps resolution cannot resolve a 1s window"

	states := []struct {
		name     string
		headline string
		snap     progresswatch.Snapshot
	}{
		{"finding", "FLEET IS ALIVE AND LANDING NOTHING", finding},
		{"clean", "No finding", clean},
		{"blind", "NOT MEASURED", blind},
	}

	for _, self := range states {
		out := renderProgressReading(progressReading(t, self.snap))
		for _, other := range states {
			got := strings.Contains(out, other.headline)
			want := self.name == other.name
			if got == want {
				continue
			}
			if want {
				t.Errorf("the %s render does not carry its own headline %q:\n%s",
					self.name, other.headline, out)
			} else {
				t.Errorf("the %s render carries the %s headline %q — the two states are not greppable apart:\n%s",
					self.name, other.name, other.headline, out)
			}
		}
	}
}
