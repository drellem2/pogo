package turnlog

import (
	"os"
	"testing"
)

func writeFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0644)
}

// TestProbeIsThePositiveControl runs the failing arm of this check on every
// merge.
//
// mg-a270 made this an acceptance requirement rather than a nicety: "a liveness
// check that has never been observed failing is a presence check until proven
// otherwise". Every signal that read green through the 22-hour outage had that
// property — correct about what it measured, and never once seen to go red, so
// "working" and "incapable of failing" were indistinguishable from outside for
// the whole window. This test is what makes them distinguishable here.
func TestProbeIsThePositiveControl(t *testing.T) {
	res, err := Probe(t.TempDir())
	if err != nil {
		t.Fatalf("probe could not be built — that is an instrument failure, not a pass: %v", err)
	}
	if !res.WentRed {
		t.Errorf("THE CHECK DID NOT GO RED: %s", res.Detail)
	}
	if !res.StayedGreen {
		t.Errorf("the check reddened an agent that completed a turn: %s", res.Detail)
	}
	if !res.Passed {
		t.Errorf("probe failed: %s", res.Detail)
	}
	if res.Findings != 2 {
		t.Errorf("findings = %d, want 2 (the stale agent and the silent one)", res.Findings)
	}
}

// TestProbeDistinguishesNeverFromStale. Both are red and they are different
// conditions: "has not written a line since it started" and "stopped writing
// them" take different responses, and collapsing them would put the mg-a270
// state (no artifact at all) behind a staleness threshold that can be tuned
// until it never fires.
func TestProbeDistinguishesNeverFromStale(t *testing.T) {
	res, err := Probe(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if res.SilentVerdict != VerdictSilent {
		t.Errorf("agent that never completed a turn read %s, want %s", res.SilentVerdict, VerdictSilent)
	}
	if res.StaleVerdict != VerdictStale {
		t.Errorf("agent that stopped completing turns read %s, want %s", res.StaleVerdict, VerdictStale)
	}
}
