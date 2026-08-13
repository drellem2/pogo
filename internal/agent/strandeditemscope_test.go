package agent

import (
	"net/http"
	"strings"
	"testing"
)

// The item-scope question, answered (mg-5ec6).
//
// A polecat working mg-687f recorded a question against mg-be37 while mg-be37 was
// in flight; mg-be37 finished first, so the question reached nobody and sat for
// three days. It was this: `pogo check-stranded` is item-driven over OPEN items
// (strandwatch.OpenStatuses), so a branch whose work item was closed by a
// SIBLING's merge is outside its domain by construction — does the same hole
// exist in the spawn-time refusal and the release-time reporter, which are
// different code paths and were explicitly not to be assumed?
//
// It does not. Neither reads a work item's status to decide anything, and the
// tests below are here so that stays true. The property is exactly the kind that
// gets lost to a plausible optimisation — "skip the git work if the item is
// already done" is one line, reads as obviously safe, and would rebuild the hole
// the question was about.
//
// THE SHAPE THAT PROMPTED IT, for whoever reads these tests without the ticket:
// polecat x8af0's submit failed terminally on `Could not resolve host: github.com`
// with its branch already pushed; its claim was released; z8af0 was dispatched
// onto the same item four seconds later and re-derived the ticket for 43 minutes;
// x8af0's branch eventually merged, closing the item, and z8af0 was stopped two
// seconds after that — still holding pushed, unmerged work of its own, on an item
// that was now `done`.

// TestSpawnRefusedForASiblingsPushedBranchOnTheSameItem is the first half of that
// shape: the re-dispatch four seconds after a sibling's claim was released.
//
// The branch belongs to x8af0 and the spawn is for z8af0 — different agent
// letters, same item — so the refusal cannot come from a name match on the
// incoming agent. It comes from BranchMatchesItem matching the branch's suffix
// against the item's, which is why the gate covers this at all. In 2026-08-05
// this dispatch succeeded and cost 43 minutes of duplicated work.
func TestSpawnRefusedForASiblingsPushedBranchOnTheSameItem(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-x8af0", "derivation.md", "feat: the whole derivation (mg-8af0)")

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "z8af0", Id: "mg-8af0", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn onto an item whose SIBLING polecat has pushed unmerged work: status = %d, "+
			"want 409 — this is the 2026-08-05 double dispatch, and the second worker spent 43 "+
			"minutes re-deriving the first one's branch", rr.Code)
	}
	if body := rr.Body.String(); !strings.Contains(body, "polecat-x8af0") {
		t.Errorf("the refusal does not name the sibling's branch, so a reader cannot find the work "+
			"that already exists; got: %s", body)
	}
}

// TestSpawnStillRefusedWhenTheWorkItemIsAlreadyDone pins that the dispatch gate
// reads no work-item status.
//
// The status probe is wired to answer `done` for everything and to record every
// call. The refusal must still fire, and the probe must not be consulted at all:
// this gate's inputs are the spawn request's id, the repo and the target, full
// stop. `pogo check-stranded`'s open-item scoping is documented behaviour over
// there and must not leak into here.
func TestSpawnStillRefusedWhenTheWorkItemIsAlreadyDone(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	var asked []string
	SetWorkItemStatusProbe(func(id string) string {
		asked = append(asked, id)
		return "done"
	})
	t.Cleanup(func() { SetWorkItemStatusProbe(nil) })

	reg := newDrainTestRegistry(t)
	refusal := reg.strandedWorkRefusal("mg-9a19", repo, "main")
	if refusal == "" {
		t.Fatal("the dispatch gate went quiet for a `done` work item. It must not: it decides on a " +
			"branch read from disk, and an item's status is not one of its inputs (mg-5ec6)")
	}
	if len(asked) != 0 {
		t.Errorf("the dispatch gate consulted the work-item status probe (%v). It has no business "+
			"asking: a status it could read is a status it could act on", asked)
	}
}

// TestReleaseReportsStrandedWorkWhenASiblingAlreadyClosedTheItem is the second
// half of the shape, and the one the question was really about: z8af0 stopped two
// seconds after a sibling's merge closed its item, still holding pushed work.
//
// The alert must fire. It is allowed — required, since mg-1af2 — to say something
// DIFFERENT for a closed item, because "do NOT dispatch" is not true of an item
// nothing will be dispatched at; what it may not do is go quiet. A branch whose
// item is closed is the harder case to notice, not the easier one: nothing on the
// board will ever point at it again.
func TestReleaseReportsStrandedWorkWhenASiblingAlreadyClosedTheItem(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	// The sibling that won: its branch landed on main, which is what closed the
	// item. The loser's branch is still pushed and unmerged.
	pushBranch(t, repo, "polecat-x8af0", "derivation.md", "feat: the derivation (mg-8af0)")
	gitRun(t, repo, "merge", "-q", "--ff-only", "polecat-x8af0")
	gitRun(t, repo, "push", "-q", "origin", "main")
	pushBranch(t, repo, "polecat-z8af0", "rederivation.md", "feat: the same derivation again (mg-8af0)")

	SetWorkItemStatusProbe(func(string) string { return "done" })
	t.Cleanup(func() { SetWorkItemStatusProbe(nil) })

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: false}) // nothing to release: the item is closed
	a := livePolecat("z8af0", "mg-8af0")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}

	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("releasing a polecat whose item a SIBLING had already closed sent %d mails, want 1. "+
			"The release reporter must not key on an open item — a closed item's unmerged branch is "+
			"the one nothing else will ever look at (mg-5ec6)", len(sent))
	}
	m := sent[0]
	if m.Finding.Branch != "polecat-z8af0" {
		t.Errorf("the alert names branch %q, want polecat-z8af0", m.Finding.Branch)
	}
	if m.ItemStatus != "done" {
		t.Errorf("ItemStatus = %q, want done — the status is read to WORD the alert, and this is the "+
			"wording that must fire", m.ItemStatus)
	}
	subject, body := m.Message()
	if !strings.Contains(subject, "never merged") {
		t.Errorf("the subject does not say what is actually wrong with a closed item: %q", subject)
	}
	if !strings.Contains(body, "NOT A RE-DISPATCH RISK") {
		t.Errorf("the body kept the re-dispatch framing for an item nothing will be dispatched at:\n%s", body)
	}
}

// TestReleaseDoesNotConsultTheStatusProbeToDecide is the polarity control for the
// test above. A probe that FAILS — the ordinary case when mg is missing or the
// store is unreachable — must change nothing about whether the alert is sent.
//
// Without this, an implementation that gates the report on `status != done` would
// still pass the sibling test above whenever the probe answered, and fail only in
// production the first time mg was slow.
func TestReleaseDoesNotConsultTheStatusProbeToDecide(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished (mg-9a19)")

	SetWorkItemStatusProbe(func(string) string { return "" }) // the probe could not read it
	t.Cleanup(func() { SetWorkItemStatusProbe(nil) })

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})
	a := livePolecat("9a19", "mg-9a19")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
	if sent := mail(); len(sent) != 1 {
		t.Fatalf("an unreadable work-item status suppressed the stranded-work alert (%d sent, want 1); "+
			"nothing about whether to report may depend on that probe", len(sent))
	}
}
