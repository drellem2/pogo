package absentwatch

import (
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// This file is the ONLY place absentwatch touches live pogo state. The runner in
// watcher.go takes a Snapshot and nothing else, so every test in this package
// builds fixtures by hand — mg-6092, mg-e8e7 and mg-5336 are three separate
// tickets for tests that read the developer's live ~/.pogo, and this package
// does not add a fourth.

// RegistrySource adapts an agent registry into a SourceFunc.
//
// The adaptation is deliberately thin: what counts as configured, what counts as
// parked, and which class a prompt declares all stay inside internal/agent,
// where `pogo agent roster` already gets them. Re-deriving them here would give
// the fleet two answers to the same question, and the value of this detector is
// that it announces the SAME roster a human would read out of the CLI, without
// needing them to run it.
//
// A nil registry yields an error rather than an empty snapshot, on the same
// footing as agent.ErrNoRosterJudgement: a detector that cannot look has not
// found a complete roster.
func RegistrySource(reg *agent.Registry) SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		if reg == nil {
			return Snapshot{}, agent.ErrNoRosterJudgement
		}
		rep, err := reg.RosterReport()
		if err != nil {
			return Snapshot{}, err
		}
		return FromReport(rep, now), nil
	}
}

// FromReport converts a registry roster report into a detector snapshot.
func FromReport(rep agent.RosterReport, now time.Time) Snapshot {
	snap := Snapshot{
		Now:        now,
		Configured: rep.Configured,
		Present:    rep.Present,
		Parked:     rep.Parked,
	}
	for _, m := range rep.Absent {
		snap.Absent = append(snap.Absent, Finding{
			Name:           m.Name,
			Identity:       m.Identity,
			Class:          classOf(m.Class),
			RestartOnCrash: m.RestartOnCrash,
			Reason:         m.Error,
		})
	}
	return snap
}

// classOf maps the roster's class onto this package's. The two enums are
// spelled separately so the detector stays a pure struct consumer, and an
// unrecognised value maps to UNCLASSIFIABLE rather than to the quieter
// on-demand: a class this binary does not know about is an unknown, and an
// unknown must not buy silence.
func classOf(c agent.RosterClass) Class {
	switch c {
	case agent.RosterSupervised:
		return ClassSupervised
	case agent.RosterOnDemand:
		return ClassOnDemand
	default:
		return ClassUnclassifiable
	}
}
