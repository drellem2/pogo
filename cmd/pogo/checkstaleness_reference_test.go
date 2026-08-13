package main

// The reference row must carry its own limit (mg-afd0).
//
// The defect was not in the comparison — that was correct file by file. It was
// in the ROW: `reference: ~/.pogo/deploy-src @ origin/main = 082ec38b0159`, read
// by every reader as the live remote head, when it is a mirror's copy from the
// last deploy's fetch. The qualifying caveat existed, in `--help`, and a caveat
// that does not travel with the output does not exist.
//
// So these are output tests rather than logic tests. The logic controls live in
// internal/staleness/reference_test.go; what is asserted here is that the number
// and its limit are printed in the same breath, on every run, including the runs
// where nothing is wrong.

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/staleness"
)

func fetchedRef(age time.Duration) staleness.Reference {
	return staleness.Reference{
		Repo: "/Users/daniel/.pogo/deploy-src", Ref: "origin/main",
		Commit: "082ec38b0159db6ae552202c626fa2d5955a37f0", CommitTime: "2026-08-13T02:41:35+01:00",
		Fetch: staleness.FetchState{
			At:         "2026-08-13T03:00:47+01:00",
			AgeSeconds: int(age.Seconds()),
			Source:     "/Users/daniel/.pogo/deploy-src/.git/FETCH_HEAD",
		},
	}
}

// TestReferenceRowCarriesItsFetchAge is the defect, inverted.
func TestReferenceRowCarriesItsFetchAge(t *testing.T) {
	out := captureStdout(t, func() {
		printReferenceLimit(fetchedRef(5*time.Hour+29*time.Minute), false)
	})

	for _, want := range []string{"LAST FETCHED", "5h29m", "not the live", "FETCH_HEAD"} {
		if !strings.Contains(out, want) {
			t.Errorf("reference row is missing %q — a reader takes `origin/main` for the remote head "+
				"unless the row says otherwise:\n%s", want, out)
		}
	}
}

// TestRefreshedReferenceRowRetiresTheLimitRatherThanRestatingIt.
//
// This is the fix checked against the defect it fixes. --fetch makes the
// reference current; printing "anything pushed since is invisible" underneath
// it would be a caveat that no longer holds travelling with the output that
// disproves it — the same shape as the label this ticket removed, committed by
// its own remedy.
func TestRefreshedReferenceRowRetiresTheLimitRatherThanRestatingIt(t *testing.T) {
	out := captureStdout(t, func() {
		printReferenceLimit(fetchedRef(5*time.Hour), true)
	})

	if !strings.Contains(out, "REFRESHED BY THIS RUN") {
		t.Errorf("a refreshed reference does not say so:\n%s", out)
	}
	if strings.Contains(out, "invisible to the comparison") {
		t.Errorf("kept the stale-reference caveat over a reference this run refreshed:\n%s", out)
	}
	// The deploy's own fetch time is still the useful datum and must survive.
	if !strings.Contains(out, "deploy's own last fetch") || !strings.Contains(out, "03:00:47") {
		t.Errorf("the deployed fetch time was dropped after --fetch:\n%s", out)
	}

	// And with no prior fetch to date — a fresh clone, the shape a --fetch run
	// on a never-fetched reference hits — the refreshed claim still wins. The
	// UNKNOWN branch says "how much this reference can have seen is unknown",
	// which is exactly as unsupported as the label this ticket removed, only
	// pessimistic instead of optimistic.
	undated := fetchedRef(0)
	undated.Fetch = staleness.FetchState{Why: "no FETCH_HEAD — this repo has never fetched"}
	out = captureStdout(t, func() { printReferenceLimit(undated, true) })

	if !strings.Contains(out, "REFRESHED BY THIS RUN") {
		t.Errorf("a refreshed reference with no prior fetch does not say it was refreshed:\n%s", out)
	}
	if strings.Contains(out, "can have seen is unknown") {
		t.Errorf("claimed the comparison is limited by a fetch this run performed:\n%s", out)
	}
}

// TestUnknownFetchAgeIsSaidNotOmitted. An absent timestamp rendered as nothing
// leaves the same unqualified row the ticket is about; rendered as a zero it
// would read as "just fetched", which is worse.
func TestUnknownFetchAgeIsSaidNotOmitted(t *testing.T) {
	ref := fetchedRef(0)
	ref.Fetch = staleness.FetchState{Why: "no FETCH_HEAD — this repo has never fetched"}

	out := captureStdout(t, func() { printReferenceLimit(ref, false) })

	if !strings.Contains(out, "FETCH TIME UNKNOWN") {
		t.Errorf("an unknown fetch time printed no limit at all:\n%s", out)
	}
	if !strings.Contains(out, "never fetched") {
		t.Errorf("unknown fetch time carries no reason:\n%s", out)
	}
}

// TestRemoteWitnessDeclaresItsSilence. Same rule as the did-not-run half: this
// witness's finding is an absence, so a run that printed nothing and a fleet
// that is up to date look identical.
func TestRemoteWitnessDeclaresItsSilence(t *testing.T) {
	disarmed := captureStdout(t, func() {
		printRemoteWitness(staleness.RemoteState{Armed: false, Err: "main tracks no remote"})
	})
	if !strings.Contains(disarmed, "NOT CONSULTED") || !strings.Contains(disarmed, "not an all-clear") {
		t.Errorf("an unarmed remote check does not declare itself:\n%s", disarmed)
	}

	unreachable := captureStdout(t, func() {
		printRemoteWitness(staleness.RemoteState{
			Armed:  true,
			Target: staleness.RemoteTarget{Name: "origin", URL: "git@github.com:drellem2/pogo.git", Branch: "main"},
			Err:    "querying git@github.com:drellem2/pogo.git gave no answer within 15s",
		})
	})
	if !strings.Contains(unreachable, "COULD NOT CONSULT") {
		t.Errorf("an unreachable remote does not say so:\n%s", unreachable)
	}
	if !strings.Contains(unreachable, "not an all-clear") {
		t.Errorf("an unreachable remote reads as health:\n%s", unreachable)
	}
	// And it says why it is not a finding, because the exit status will be 0 and
	// a reader is entitled to know that was a decision.
	if !strings.Contains(unreachable, "offline host") {
		t.Errorf("does not explain why an unreachable remote is not counted:\n%s", unreachable)
	}
}

// TestUncountableGapIsReportedAsUnknownNotZero — the DEFAULT state on the live
// box, and the one the whole ticket turns on. deploy-src has not fetched the
// commits that shipped since 03:00, so it cannot count them; a "0 commits since"
// there would be the original defect with a new number attached.
func TestUncountableGapIsReportedAsUnknownNotZero(t *testing.T) {
	out := captureStdout(t, func() {
		printRemoteWitness(staleness.RemoteState{
			Armed:  true,
			Target: staleness.RemoteTarget{Name: "origin", URL: "git@github.com:drellem2/pogo.git", Branch: "main"},
			Head:   "49dbe4bac94f268d6fed2b0e48fd3020f3c57e4f",
			Behind: true, Counted: false,
		})
	})

	if !strings.Contains(out, "BEHIND THE REMOTE") || !strings.Contains(out, "49dbe4bac94f") {
		t.Errorf("does not name the live head it is behind:\n%s", out)
	}
	if !strings.Contains(out, "CANNOT") {
		t.Errorf("an uncountable gap was not reported as uncountable:\n%s", out)
	}
	if !strings.Contains(out, "DEPLOYED") {
		t.Errorf("does not say which question the verdict below actually answers:\n%s", out)
	}
	if !strings.Contains(out, "--fetch") {
		t.Errorf("names no way to get the answer:\n%s", out)
	}
}

// TestCorpusCommitsAreNamedNotJustCounted. A count says how far behind; only a
// subject says behind on WHAT, which is what a reader deciding whether to
// redeploy tonight is actually asking.
func TestCorpusCommitsAreNamedNotJustCounted(t *testing.T) {
	out := captureStdout(t, func() {
		printRemoteWitness(staleness.RemoteState{
			Armed:  true,
			Target: staleness.RemoteTarget{Name: "origin", URL: "git@github.com:drellem2/pogo.git", Branch: "main"},
			Head:   "49dbe4bac94f", Behind: true, Counted: true,
			Commits: 17, CorpusCommits: 5,
			Corpus: []staleness.AheadCommit{
				{SHA: "d27ecc1", Subject: "the DOCTOR's refinery-history advice names its window"},
			},
			Truncated: 4,
		})
	})

	for _, want := range []string{"17 commit(s)", "5 of them", "d27ecc1", "refinery-history", "4 more"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q from the gap report:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "Fix:") {
		t.Errorf("reports the gap and no remedy:\n%s", out)
	}
}

// TestGapWithNoCorpusCommitsIsNotDressedAsAFinding. The scope correction on this
// ticket's own body applies to its output too: this witness judges the prompt
// corpus, and commits that touch nothing under it leave the corpus verdict
// exactly right.
func TestGapWithNoCorpusCommitsIsNotDressedAsAFinding(t *testing.T) {
	out := captureStdout(t, func() {
		printRemoteWitness(staleness.RemoteState{
			Armed:  true,
			Target: staleness.RemoteTarget{Name: "origin", URL: "git@github.com:drellem2/pogo.git", Branch: "main"},
			Head:   "49dbe4bac94f", Behind: true, Counted: true,
			Commits: 9, CorpusCommits: 0,
		})
	})

	if !strings.Contains(out, "9 commit(s)") || !strings.Contains(out, "NONE") {
		t.Errorf("does not report the gap it measured:\n%s", out)
	}
	if !strings.Contains(out, "Not a finding") {
		t.Errorf("does not say the corpus verdict below stands:\n%s", out)
	}
	if strings.Contains(out, "Fix:") {
		t.Errorf("prescribes a redeploy for commits this witness does not judge:\n%s", out)
	}
}

// TestFetchedReferenceStillNamesTheDeployedRevision. After --fetch the ref has
// moved, and the pre-fetch revision is the only thing that says where the
// deploy left the fleet. The fetch is also the only thing that erases the local
// record of it, which is why it is captured before rather than read after.
func TestFetchedReferenceStillNamesTheDeployedRevision(t *testing.T) {
	out := captureStdout(t, func() {
		printRemoteWitness(staleness.RemoteState{
			Armed:   true,
			Target:  staleness.RemoteTarget{Name: "origin", URL: "git@github.com:drellem2/pogo.git", Branch: "main"},
			Fetched: true, Was: "082ec38b0159db6ae552202c626fa2d5955a37f0",
			Head: "49dbe4bac94f268d6fed2b0e48fd3020f3c57e4f", Behind: true, Counted: true,
			Commits: 17, CorpusCommits: 5,
		})
	})

	if !strings.Contains(out, "082ec38b0159") || !strings.Contains(out, "DEPLOYED revision") {
		t.Errorf("a fetched reference no longer names where the deploy left it:\n%s", out)
	}
	if !strings.Contains(out, "17 commit(s)") {
		t.Errorf("does not say what shipped between the two:\n%s", out)
	}
}

// TestOlderGitFetchStampMoveIsReported. --no-write-fetch-head is what keeps the
// remedy from erasing the evidence the fault is read from. A git too old to
// know the flag does erase it, and the run that did it is the only place that
// can say so.
func TestOlderGitFetchStampMoveIsReported(t *testing.T) {
	out := captureStdout(t, func() {
		printRemoteWitness(staleness.RemoteState{
			Armed:   true,
			Target:  staleness.RemoteTarget{Name: "origin", URL: "git@github.com:drellem2/pogo.git", Branch: "main"},
			Fetched: true, Was: "082ec38b", Head: "082ec38b",
			FetchStampMoved:  true,
			FetchStampReason: "this git does not know --no-write-fetch-head",
		})
	})

	if !strings.Contains(out, "FETCH_HEAD now dates THIS run") {
		t.Errorf("moved the deploy's fetch timestamp without saying so:\n%s", out)
	}
}
