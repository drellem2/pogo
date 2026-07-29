package deafwatch

import (
	"time"

	"github.com/drellem2/pogo/internal/agent"
)

// This file is the ONLY place deafwatch touches live pogo state. The runner in
// watcher.go takes a Snapshot and nothing else, so every test in this package
// builds fixtures by hand — mg-6092, mg-e8e7 and mg-5336 are three separate
// tickets for tests that read the developer's live ~/.pogo, and this package
// does not add a fourth.

// RegistrySource adapts an agent registry into a SourceFunc.
//
// The adaptation is deliberately thin: the judgement — who is owed a mail loop,
// and who diagnose has no standing to judge — stays inside internal/agent,
// where `pogo agent diagnose` already gets it. Re-deriving it here would give
// the fleet two answers to the same question, and the whole value of this
// detector is that it announces the SAME verdict a human would read out of
// diagnose, without needing them to guess the name first.
//
// A nil registry yields an error rather than an empty snapshot, on the same
// footing as agent.ErrNoMailCheckJudgement: a detector that cannot look has not
// found a clean fleet.
func RegistrySource(reg *agent.Registry) SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		if reg == nil {
			return Snapshot{}, agent.ErrNoMailCheckJudgement
		}
		rep, err := reg.MailLoopReport()
		if err != nil {
			return Snapshot{}, err
		}
		return FromReport(rep, now), nil
	}
}

// FromReport converts a registry mail-loop report into a detector snapshot.
func FromReport(rep agent.MailLoopReport, now time.Time) Snapshot {
	snap := Snapshot{Now: now, Scanned: rep.Scanned, Judged: rep.Judged}
	for _, m := range rep.Missing {
		snap.Missing = append(snap.Missing, Finding{
			Name:     m.Name,
			Identity: m.Identity,
			Type:     string(m.Type),
			Alive:    m.Alive,
		})
	}
	return snap
}
