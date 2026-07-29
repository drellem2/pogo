package agent

import "github.com/drellem2/pogo/internal/gitgc"

// PolecatOwnerVerdicts maps every witnessed polecat name to what a git-GC sweep
// may conclude about whether anything still owns its worktree. It is the input
// to gitgc.Options.OwnerVerdicts, and it exists because gitgc is deliberately
// free of any dependency on this package (see the gitgc package comment), so
// the verdict has to be handed to it rather than looked up by it.
//
// # Only WitnessDead is evidence of death (gh #97)
//
// The mapping is deliberately lopsided, and the lopsidedness is the whole
// content of this function:
//
//	WitnessDead       -> OwnerGone      the store's ONE positive-evidence verdict:
//	                                    the pid answers no signal, or answers with a
//	                                    start time that is not ours, so the process
//	                                    we recorded is provably gone.
//	WitnessAlive      -> OwnerUnproven  obviously; it is alive.
//	WitnessUnreadable -> OwnerUnproven  something is alive on that pid and we could
//	                                    not confirm whose. "Cannot tell" is not death.
//	WitnessNoRecord   -> OwnerUnproven  no record at all. The caller learns NOTHING
//	                                    here — not life, not death — and a sweep that
//	                                    read it as death would put every unwitnessed
//	                                    name into the destructive arm.
//
// A polecat that is simply ABSENT from this map is likewise OwnerUnproven,
// because gitgc.Options.OwnerVerdicts reads a missing key as the zero value.
// That is the same answer WitnessNoRecord gets, reached the same way and for
// the same reason.
//
// OwnerGone licenses destroying files that git could not read, which is why it
// may only ever be reached from positive evidence. The sweep applies a further
// mtime veto on top of it (see gitgc.quietWindow); this function's job is only
// to make sure nothing weaker than WitnessDead arrives there in the first place.
//
// A read error is returned, not swallowed as an empty map: an unreadable
// witness is not a dead fleet, and callers must decide (mg-13a3). Degrading to
// nil is safe here in a way it is not for the LIVE set — nil yields
// OwnerUnproven everywhere, i.e. more refusals — but the caller should still
// see the error and say so.
func PolecatOwnerVerdicts() (map[string]gitgc.WorktreeOwner, error) {
	verdicts, err := WitnessedPolecatVerdicts()
	if err != nil {
		return nil, err
	}
	out := make(map[string]gitgc.WorktreeOwner, len(verdicts))
	for name, v := range verdicts {
		if v == WitnessDead {
			out[name] = gitgc.OwnerGone
			continue
		}
		// Written explicitly rather than left to the zero value, so the map is
		// a full statement of what the witness said rather than a list of the
		// names that happened to clear the bar.
		out[name] = gitgc.OwnerUnproven
	}
	return out, nil
}
