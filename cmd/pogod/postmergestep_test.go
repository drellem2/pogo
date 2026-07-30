package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/refinery"
)

// releaseCutMR is a plain default-branch merge with a post-merge tag declared —
// the release-cut shape. Note what it does NOT set: no DeferDone, no PRFlow, no
// declared post-merge work on the item. Under every prior fix in this area that
// combination is "merged, therefore done", which is why the release path fell
// through mg-7746's guard.
func releaseCutMR(tag, postMergeErr string) *refinery.MergeRequest {
	return &refinery.MergeRequest{
		ID:             "mr-88",
		Branch:         "polecat-mg-e084",
		Author:         "mg-e084",
		TargetRef:      "main",
		Status:         refinery.StatusMerged,
		MergedSHA:      "21de0b1f00000000000000000000000000000000",
		PostMergeTag:   tag,
		PostMergeError: postMergeErr,
	}
}

func e084Registry() *fakeReaper {
	return &fakeReaper{agents: map[string]*agent.Agent{
		"e084": {Name: "e084", WorkItemID: "mg-e084", Type: agent.TypePolecat},
	}}
}

// TestPostMergeStepFailure_BlocksAutoDone is the mg-6879 acceptance control on
// the silence, which was the defect's worst property.
//
// The merge landed, so Status is "merged" and every status-keyed reader sees
// success. The tag did not land. Marking the item done here is exactly what
// produced `mg-e084` reading done with exit_code=0 while no v0.8.0 tag existed:
// no failure event, no escalation, and no stall — because a done item is in a
// terminal state and the 15-minute backstop does not fire on terminal states.
//
// So a failed post-merge step must not be able to resolve as completion.
func TestPostMergeStepFailure_BlocksAutoDone(t *testing.T) {
	reg := e084Registry()
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	mr := releaseCutMR("v0.8.0", "push tag v0.8.0 to origin: remote rejected: exit status 1")
	verdict := resolvePostMergeWork(reg, mr, func(string) (bool, error) { return false, nil })
	if !verdict.Declared {
		t.Fatal("a failed post-merge step must declare that this merge is not completion")
	}
	if !strings.Contains(verdict.Reason, "v0.8.0") {
		t.Errorf("the reason must carry the underlying failure, got: %s", verdict.Reason)
	}

	completeCalled := false
	reapMergedPolecat(reg, mr, func(string, string) error { completeCalled = true; return nil }, verdict, backstop)

	if completeCalled {
		t.Error("mg done must NOT be called when the declared post-merge step failed — the deliverable does not exist")
	}
	if len(reg.stopped) != 0 {
		t.Errorf("the polecat must not be stopped while its deliverable is missing, got %v", reg.stopped)
	}
	backstop.mu.Lock()
	_, armed := backstop.timers["e084"]
	backstop.mu.Unlock()
	if !armed {
		t.Error("the backstop must still be armed so the polecat cannot hold its slot forever")
	}
}

// TestPostMergeStepFailure_NeedsNoProbe pins that the failure signal is
// self-sufficient. resolvePostMergeWork used to return "completes it" whenever
// the declares probe was nil, and the post-merge failure must not be
// suppressible by a missing probe — the whole point is that this verdict does
// not depend on anyone having remembered to configure anything.
func TestPostMergeStepFailure_NeedsNoProbe(t *testing.T) {
	reg := e084Registry()
	mr := releaseCutMR("v0.8.0", "create tag v0.8.0: exit status 128")

	if v := resolvePostMergeWork(reg, mr, nil); !v.Declared {
		t.Error("a failed post-merge step must be honoured with no declares probe available")
	}
	// Also true when the probe is present but answers "no post-merge work" —
	// the item never declared any, and the failure still stands on its own.
	if v := resolvePostMergeWork(reg, mr, func(string) (bool, error) { return false, nil }); !v.Declared {
		t.Error("a failed post-merge step must outrank a probe that says the item declares nothing")
	}
	// And when the probe errors, which already declared post-merge work for its
	// own reason (mg-d86e) — the answer must not regress to completion.
	if v := resolvePostMergeWork(reg, mr, func(string) (bool, error) { return false, errors.New("mg unavailable") }); !v.Declared {
		t.Error("a failed post-merge step must be honoured when the probe errors")
	}
}

// TestPostMergeStepFailure_ReasonOutranksOtherLanes: an MR can be in the
// PR-flow or --defer-done lane AND have a failed post-merge step. Whatever lane
// it was in, the failure is the most important thing about it and must be what
// the log line and the mayor's mail say — otherwise the report reads "PR still
// pending" for a broken release tag.
func TestPostMergeStepFailure_ReasonOutranksOtherLanes(t *testing.T) {
	reg := e084Registry()
	mr := releaseCutMR("v0.8.0", "remote rejected")
	mr.PRFlow = true
	mr.TargetRef = "daed-integration"
	mr.DeferDone = true

	v := resolvePostMergeWork(reg, mr, func(string) (bool, error) { return false, nil })
	if !v.Declared {
		t.Fatal("expected deferral")
	}
	if !strings.Contains(v.Reason, "post-merge step FAILED") {
		t.Errorf("the post-merge failure must win the reason over PR-flow/defer-done, got: %s", v.Reason)
	}
}

// TestPostMergeStepSuccess_CompletesAndRecordsWhatLanded is the other half, and
// it is what makes the design a fix rather than just a wider veto: when the
// refinery HAS performed the post-merge step, the polecat has nothing left to
// do after its merge, so the ordinary reap is correct and must still happen.
//
// Nothing is deferred here — no backstop armed, no slot held, no 15-minute
// wait. That is the difference between moving the work to an actor that
// outlives the merge and merely protecting the worker while it does the work
// itself.
func TestPostMergeStepSuccess_CompletesAndRecordsWhatLanded(t *testing.T) {
	reg := e084Registry()
	var escalations []string
	backstop, _ := newTestBackstop(reg, &escalations)

	mr := releaseCutMR("v0.8.0", "")
	verdict := resolvePostMergeWork(reg, mr, func(string) (bool, error) { return false, nil })
	if verdict.Declared {
		t.Fatalf("a successful post-merge step leaves nothing pending, got: %s", verdict.Reason)
	}

	var sidecar string
	reapMergedPolecat(reg, mr, func(_, resultJSON string) error { sidecar = resultJSON; return nil }, verdict, backstop)

	if len(reg.stopped) != 1 || reg.stopped[0] != "e084" {
		t.Errorf("the polecat must be reaped normally once its post-merge step has been performed for it, got %v", reg.stopped)
	}
	backstop.mu.Lock()
	_, armed := backstop.timers["e084"]
	backstop.mu.Unlock()
	if armed {
		t.Error("nothing is pending, so no backstop should be armed — the work is already done, not merely deferred")
	}

	var result map[string]any
	if err := json.Unmarshal([]byte(sidecar), &result); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v (%q)", err, sidecar)
	}
	// The closed item must name what landed. Reading it back was how the
	// missing v0.8.0 tag was eventually noticed, by hand.
	if result["merged_sha"] != mr.MergedSHA {
		t.Errorf("sidecar must record merged_sha=%s, got %q", mr.MergedSHA, sidecar)
	}
	if result["post_merge_tag"] != "v0.8.0" {
		t.Errorf("sidecar must record the tag that was pushed, got %q", sidecar)
	}
}

// TestPostMergeStepSuccess_SidecarOmitsUndeclaredTag keeps the sidecar honest:
// an ordinary merge with no post-merge step must not grow a post_merge_tag key,
// so the key's presence always means a tag was actually pushed.
func TestPostMergeStepSuccess_SidecarOmitsUndeclaredTag(t *testing.T) {
	reg := e084Registry()
	mr := releaseCutMR("", "")

	var sidecar string
	reapMergedPolecat(reg, mr, func(_, resultJSON string) error { sidecar = resultJSON; return nil }, postMergeVerdict{}, nil)

	var result map[string]any
	if err := json.Unmarshal([]byte(sidecar), &result); err != nil {
		t.Fatalf("sidecar is not valid JSON: %v (%q)", err, sidecar)
	}
	if _, ok := result["post_merge_tag"]; ok {
		t.Errorf("post_merge_tag must be absent when none was declared, got %q", sidecar)
	}
	if result["merged_sha"] != mr.MergedSHA {
		t.Errorf("merged_sha should still be recorded, got %q", sidecar)
	}
}

// TestPostMergeStepFailure_IgnoredForNonPolecatAuthors keeps the reap's
// existing scope: crew agents and humans author MRs too, and their lifecycle is
// not tied to a single work item. The refinery still reports the failure via
// mail and events; there is just no polecat to hold back.
func TestPostMergeStepFailure_IgnoredForNonPolecatAuthors(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"pm-pogo": {Name: "pm-pogo", WorkItemID: "", Type: agent.TypeCrew},
	}}
	mr := releaseCutMR("v0.8.0", "remote rejected")
	mr.Author = "pm-pogo"

	if v := resolvePostMergeWork(reg, mr, func(string) (bool, error) { return false, nil }); v.Declared {
		t.Errorf("a crew-authored merge has no polecat lifecycle to defer, got: %s", v.Reason)
	}
}
