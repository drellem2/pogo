package blindwatch

import (
	"time"

	"github.com/drellem2/pogo/internal/wedgewatch"
)

// WedgeSource adapts internal/wedgewatch's own judgement state into a Snapshot.
//
// This file is the ONLY place blindwatch touches another package's live state.
// The runner in watcher.go takes a Snapshot and nothing else, so every test
// here builds fixtures by hand.
//
// A nil watcher yields a snapshot with a zero SampledAt rather than an error,
// and that is deliberate: "the detector is not armed at all" and "the detector
// is armed and has stopped sampling" are the same fact from a consumer's seat —
// nothing is judging the fleet — and both must produce the STOPPED finding
// rather than a quiet pass. Returning an error instead would put the condition
// in the error channel, which is where this whole ticket's bug lives.
func WedgeSource(w *wedgewatch.Watcher) SourceFunc {
	return func(now time.Time) (Snapshot, error) {
		snap := Snapshot{Detector: "wedge-watch"}
		if w == nil {
			return snap, nil
		}
		blind, sampledAt, examined := w.Judgement()
		snap.SampledAt = sampledAt
		snap.Examined = examined
		for _, b := range blind {
			snap.Blind = append(snap.Blind, Target{Name: b.Name, Why: b.Why, Since: b.Since})
		}
		return snap, nil
	}
}
