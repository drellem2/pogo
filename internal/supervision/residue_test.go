package supervision

import (
	"strings"
	"testing"
)

// The residue of the 2026-08 wedge is not a demonstration that this check
// fires, and these two tests are what stops it becoming one (mg-bb68).
//
// WHAT THE RESIDUE IS. The ownership/wedge condition ended 2026-08-07
// 17:37:28Z. What survives it on this box is launchd bookkeeping that never
// clears: runs=24991 and `last exit reason = OS_REASON_CODESIGNING` read on a
// healthy daemon, and 19,274 "Cannot acquire pogod lock … held by pid 4368"
// lines in a rotated log. That is evidence the wedge EXISTED. It is not
// evidence that a detector fires on it, and the ticket that recorded the
// control as spent asked explicitly that the two not be confused.
//
// WHERE THE CONFUSION WOULD ENTER THE CODE. Reconstruct the box from residue
// alone and you get: a loaded job, no live process for it, no live lock
// holder, an exit reason still set. Check must call that UNKNOWN, because
// UNSUPERVISED requires a live rival owning this POGO_HOME — the displacement
// is the second live process, not the wreckage it left. Check gets this right
// today only because `JobLoaded && !LockPIDOK` is evaluated BEFORE
// `JobLoaded && !JobPIDOK`, and nothing pinned that ordering.
//
// MEASURED, BOTH ARMS, 2026-08-14. Swapping those two cases — a plausible edit,
// since the second is the case the package was written for — leaves this
// package's entire pre-existing suite green (`ok
// github.com/drellem2/pogo/internal/supervision`) and makes the residue shape
// report:
//
//	UNSUPERVISED: com.pogo.daemon is loaded but has NO live process, while
//	pid 0 owns this POGO_HOME and is serving … a wedged pid 0 would never be
//	restarted
//
// A confident verdict, naming a process that does not exist, produced from a
// job that is merely idle. That is precisely the residue being written up as
// the demonstration, emitted by the detector itself. Both tests below fail on
// that swap and pass on the code as it stands.
//
// AND NOTHING OUTSIDE THIS PACKAGE WOULD HAVE CAUGHT IT EITHER. Check has two
// call sites, both in cmd/pogo/main.go, and neither has a Go test.
// scripts/pogo-self-deploy_test.sh does assert on an UNSUPERVISED verdict, but
// against a STUB CLI that echoes a canned line — it never reaches this code. So
// this file is the only thing standing between that edit and a green tree.
//
// WHAT THESE TESTS DO NOT DO. They do not demonstrate that Observe produces the
// displaced reading from a real host; nothing does, and the control that would
// have is spent. See docs/investigations/ownership-wedge-control-spent-2026-08-07.md.

// TestResidueAloneIsNeverUnsupervised pins the case ordering as behaviour. The
// observation is the box as it reads once the wedge is over and the orphan is
// gone — which is the box today, and the box every later reader will meet.
func TestResidueAloneIsNeverUnsupervised(t *testing.T) {
	residue := Observation{
		Label:     "com.pogo.daemon",
		JobLoaded: true, JobPIDOK: false, // launchd holds the job, no live process for it
		LockPIDOK:      false,                   // pid 4368 is long gone; nothing owns POGO_HOME
		LastExitReason: "OS_REASON_CODESIGNING", // a lifetime field, still set on a healthy box
	}

	got := Check(residue)
	if got.Verdict == Unsupervised {
		t.Fatalf("the wreckage of a finished wedge reported %s — a displacement needs a LIVE rival "+
			"owning this POGO_HOME, and there is none in this reading.\nreason: %s", got.Verdict, got.Reason)
	}
	if got.Verdict != Unknown {
		t.Fatalf("verdict = %q, want %q — an idle loaded job with no holder is unreadable, not healthy "+
			"and not displaced.\nreason: %s", got.Verdict, Unknown, got.Reason)
	}
}

// TestNoVerdictNamesAHolderThatIsNotLive is the general form, and it is the
// arm that would have caught the swap even had the verdict been renamed. When
// LockPIDOK is false the check has no owner to name; a reason line that
// asserts one is fabricating the very party the residue cannot supply. "pid 0
// owns this POGO_HOME and is serving" is what that fabrication looks like.
func TestNoVerdictNamesAHolderThatIsNotLive(t *testing.T) {
	for _, obs := range []Observation{
		{Label: "com.pogo.daemon", JobLoaded: true, JobPIDOK: false, LockPIDOK: false},
		{Label: "com.pogo.daemon", JobLoaded: true, JobPID: 77880, JobPIDOK: true, LockPIDOK: false},
		{Label: "com.pogo.daemon", JobLoaded: false, LockPIDOK: false},
	} {
		res := Check(obs)
		for _, claim := range []string{"owns this POGO_HOME", "holds the pogod lockfile — launchd"} {
			if strings.Contains(res.Reason, claim) {
				t.Errorf("reason claims an owner while LockPIDOK is false: %q\nobservation: %+v", res.Reason, obs)
			}
		}
		if strings.Contains(res.Reason, "pid 0") {
			t.Errorf("reason names pid 0 as a process: %q\nobservation: %+v", res.Reason, obs)
		}
	}
}
