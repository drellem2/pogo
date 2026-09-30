// Package watchstate is the persisted half of a report-only watcher's memory
// (mg-257a8).
//
// The gh-issue watchers (internal/ghintake, internal/ghteardown,
// internal/carrierdrift) used to live inside pogod, where this state was a few
// fields on a long-lived struct: when the last sample ran, the fingerprint of
// the last mailed finding set and when it was mailed, and the first-seen clock
// of every outstanding finding. They now run from `pogo gh-watch`, a
// short-lived process a launchd job starts every few minutes, so that memory
// has to outlive the process or every run would be the watcher's first — every
// finding set "changed", every fire a mail, and no escalation clock ever older
// than one run.
//
// One type, shared by all three, because the three kept byte-identical state;
// a package of its own so none of them has to import another to name it.
package watchstate

import "time"

// State is everything a watcher carries from one sample to the next.
type State struct {
	// LastRun is when the coarse-interval throttle last let a sample through.
	LastRun time.Time `json:"last_run,omitempty"`
	// Ran reports whether any sample has ever run; with it false the next
	// Check samples regardless of LastRun.
	Ran bool `json:"ran"`
	// LastPrint is the fingerprint of the last MAILED finding set.
	LastPrint string `json:"last_print,omitempty"`
	// LastMailed is when LastPrint was mailed.
	LastMailed time.Time `json:"last_mailed,omitempty"`
	// FirstSeen is each outstanding finding's escalation clock.
	FirstSeen map[string]time.Time `json:"first_seen,omitempty"`
}

// Clone returns a copy that shares no map with s.
func (s State) Clone() State {
	out := s
	if s.FirstSeen != nil {
		out.FirstSeen = make(map[string]time.Time, len(s.FirstSeen))
		for k, v := range s.FirstSeen {
			out.FirstSeen[k] = v
		}
	}
	return out
}
