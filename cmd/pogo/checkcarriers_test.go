package main

import (
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/carrierdrift"
)

// TestCheckCarriersHelpNamesTheThreeInstances. The long help is where a reader
// arrives after receiving a notice they do not recognise, and the one thing it
// must not do is describe the finding without describing why the obvious
// existing check could not have caught it — that is the question the reader
// arrives with, because `check-intake` was green through all three.
func TestCheckCarriersHelpNamesTheThreeInstances(t *testing.T) {
	var jsonOutput bool
	cmd := newCheckCarriersCmd(&jsonOutput)

	if cmd.Use != "check-carriers" {
		t.Fatalf("Use = %q", cmd.Use)
	}
	for _, want := range []string{
		"drellem2/pogo#159", "drellem2/pogo#156", "drellem2/pogo#127",
		"check-intake", "the AXIS, not the accuracy",
		"gh-closed:", "gh-ack:", "gh-parked:",
		"REPORTS ONLY",
	} {
		if !strings.Contains(cmd.Long, want) {
			t.Errorf("long help is missing %q", want)
		}
	}
	// The exit-code contract is what a schedule keys on, and the blind code is
	// the one that must not be discoverable only by reading the source.
	if !strings.Contains(cmd.Long, "when NO carrier could be re-read at all") {
		t.Error("long help does not document the instrument-failure exit code")
	}
}

// TestCheckCarriersFlagsCarryTheZeroVersusNegativeContract. Every window flag
// defaults to 0 meaning "use the package default", with a negative turning the
// check off. A flag defaulting to the concrete duration would make "I left it
// alone" and "I set it to exactly the default" indistinguishable — and would
// silently freeze the default at whatever it was when the flag was written.
func TestCheckCarriersFlagsCarryTheZeroVersusNegativeContract(t *testing.T) {
	var jsonOutput bool
	cmd := newCheckCarriersCmd(&jsonOutput)

	for _, name := range []string{"ack-window", "stage-window", "closed-grace"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag --%s is missing", name)
		}
		if f.DefValue != "0s" {
			t.Errorf("--%s default = %q, want 0s so the package default is not frozen here", name, f.DefValue)
		}
		if !strings.Contains(f.Usage, "0: default") && !strings.Contains(f.Usage, "0 or less") {
			t.Errorf("--%s usage does not state the zero/negative contract: %q", name, f.Usage)
		}
	}
	for _, name := range []string{"stage", "shelved"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s is missing", name)
		}
	}
}

// TestCheckCarriersDefaultStagesAreTheFilingStages pins the CLI against the
// package, so a default changed in one place cannot leave the other describing
// something that is no longer true.
func TestCheckCarriersDefaultStagesAreTheFilingStages(t *testing.T) {
	got := strings.Join(carrierdrift.DefaultStuckStages, ",")
	if got != ",triage" {
		t.Fatalf("DefaultStuckStages = %q, want the filing stages", got)
	}
	var jsonOutput bool
	if !strings.Contains(newCheckCarriersCmd(&jsonOutput).Long, "filing stages") {
		t.Error("long help does not name the stuck-stage coverage boundary")
	}
}
