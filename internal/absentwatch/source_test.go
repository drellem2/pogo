package absentwatch

import (
	"errors"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// TestFromReport_CarriesTheDenominatorAndTheClass pins the conversion. Both
// halves are load-bearing: the denominator is what stops a reader mistaking one
// absence out of eleven for the whole fleet being gone, and the class is what
// decides whether the watcher is patient or not.
func TestFromReport_CarriesTheDenominatorAndTheClass(t *testing.T) {
	now := time.Date(2026, 8, 10, 17, 14, 23, 0, time.UTC)
	rep := agent.RosterReport{
		Configured: 11, Present: 9, Parked: 1,
		Absent: []agent.RosterMember{{
			Name: "doctor", Identity: "crew-doctor", Category: "crew",
			State: agent.RosterAbsent, Class: agent.RosterOnDemand, RestartOnCrash: false,
		}},
	}
	snap := FromReport(rep, now)

	if snap.Configured != 11 || snap.Present != 9 || snap.Parked != 1 {
		t.Errorf("configured/present/parked = %d/%d/%d, want 11/9/1",
			snap.Configured, snap.Present, snap.Parked)
	}
	if len(snap.Absent) != 1 {
		t.Fatalf("Absent = %+v, want one finding", snap.Absent)
	}
	got := snap.Absent[0]
	if got.Name != "doctor" || got.Identity != "crew-doctor" {
		t.Errorf("Finding = %+v, want name and identity carried through intact", got)
	}
	if got.Class != ClassOnDemand || got.RestartOnCrash {
		t.Errorf("Finding = %+v, want the on-demand class and restart_on_crash=false", got)
	}
}

// TestClassOf_UnknownIsUnclassifiableNotOnDemand: a class this binary does not
// recognise is an unknown, and an unknown must not buy the quieter (24h)
// threshold. Rounding toward silence is what this whole lineage exists to stop.
func TestClassOf_UnknownIsUnclassifiableNotOnDemand(t *testing.T) {
	if got := classOf(agent.RosterClass("something-new")); got != ClassUnclassifiable {
		t.Errorf("classOf(unknown) = %q, want %q", got, ClassUnclassifiable)
	}
	if got := classOf(agent.RosterUnclassifiable); got != ClassUnclassifiable {
		t.Errorf("classOf(unclassifiable) = %q, want %q", got, ClassUnclassifiable)
	}
	if got := classOf(agent.RosterSupervised); got != ClassSupervised {
		t.Errorf("classOf(supervised) = %q, want %q", got, ClassSupervised)
	}
}

// TestRegistrySource_NilRegistryIsAnError: a source that cannot look has not
// found a complete roster. Returning an empty Snapshot here would make pogod
// announce nothing and log nothing, which is precisely the silence this package
// exists to end.
func TestRegistrySource_NilRegistryIsAnError(t *testing.T) {
	_, err := RegistrySource(nil)(time.Now())
	if !errors.Is(err, agent.ErrNoRosterJudgement) {
		t.Fatalf("err = %v, want ErrNoRosterJudgement", err)
	}
}

// TestPatience_ClassDecidesTheThreshold is the anti-wolf rule in one assertion.
func TestPatience_ClassDecidesTheThreshold(t *testing.T) {
	hold, dormant := 15*time.Minute, 24*time.Hour
	cases := map[Class]time.Duration{
		ClassSupervised:     hold,
		ClassOnDemand:       dormant,
		ClassUnclassifiable: hold,
	}
	for class, want := range cases {
		if got := (Finding{Class: class}).patience(hold, dormant); got != want {
			t.Errorf("patience(%q) = %s, want %s", class, got, want)
		}
	}
}
