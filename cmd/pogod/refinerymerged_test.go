package main

import (
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/refinery"
)

func mergedMR(id, author string, status refinery.MergeStatus) refinery.MergeRequest {
	return refinery.MergeRequest{
		ID:        id,
		RepoPath:  "/Users/daniel/dev/pogo",
		Branch:    "polecat-" + strings.TrimPrefix(author, "mg-"),
		TargetRef: "main",
		Author:    author,
		Status:    status,
		MergedSHA: "abc123def4567890",
		DoneTime:  time.Now().Add(-5 * time.Minute),
	}
}

// The two instances of 2026-08-12, as the refinery recorded them: an item whose
// branch merged and whose work item stayed available.
func TestMergedWorkForFindsALandedMerge(t *testing.T) {
	history := []refinery.MergeRequest{
		mergedMR("mr-1", "mg-0e8c", refinery.StatusMerged),
		mergedMR("mr-2", "mg-ac0c", refinery.StatusMerged),
	}
	got, ok := mergedWorkFor(history, "mg-ac0c")
	if !ok {
		t.Fatal("a merged MR authored by this item was not found")
	}
	if got.MR != "mr-2" || got.Branch != "polecat-ac0c" || got.Target != "main" {
		t.Errorf("wrong record returned: %+v", got)
	}
	if got.MergedSHA == "" || got.MergedAt.IsZero() {
		t.Errorf("the sha and merge time are what make the refusal checkable; got %+v", got)
	}
}

// Author matching is case-folded and trimmed, because `--author` is a free
// string typed by whoever submitted the branch.
func TestMergedWorkForMatchesAuthorLoosely(t *testing.T) {
	history := []refinery.MergeRequest{mergedMR("mr-1", "  MG-AC0C ", refinery.StatusMerged)}
	if _, ok := mergedWorkFor(history, "mg-ac0c"); !ok {
		t.Error("a merged MR whose author differs only by case and whitespace was not matched")
	}
}

// Every exclusion is a case where refusing the dispatch would be WRONG, not
// merely noisy — a failed merge is precisely the item that should be dispatched
// again, and a PR-flow merge landed on an integration branch whose deliverable
// does not exist yet.
func TestMergedWorkForExcludesWhatIsNotLandedCompletion(t *testing.T) {
	prFlow := mergedMR("mr-pr", "mg-ac0c", refinery.StatusMerged)
	prFlow.PRFlow = true
	prFlow.TargetRef = "integration/foo"

	cases := []struct {
		name string
		mr   refinery.MergeRequest
	}{
		{"still queued", mergedMR("mr-q", "mg-ac0c", refinery.StatusQueued)},
		{"in flight", mergedMR("mr-p", "mg-ac0c", refinery.StatusProcessing)},
		{"failed", mergedMR("mr-f", "mg-ac0c", refinery.StatusFailed)},
		{"cancelled", mergedMR("mr-c", "mg-ac0c", refinery.StatusCancelled)},
		{"lost across a restart", mergedMR("mr-l", "mg-ac0c", refinery.StatusLost)},
		{"PR-flow merge onto an integration branch", prFlow},
		{"another item's merge", mergedMR("mr-o", "mg-0e8c", refinery.StatusMerged)},
		{"a crew agent's merge", mergedMR("mr-m", "mayor", refinery.StatusMerged)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, ok := mergedWorkFor([]refinery.MergeRequest{tc.mr}, "mg-ac0c"); ok {
				t.Errorf("dispatch would be refused for %s, which is not landed completion for this item", tc.name)
			}
		})
	}
}

// A --defer-done merge IS refused. The polecat owns its own post-merge flow, but
// the code is on the target either way, so a fresh worker sent at the item
// re-derives merged work.
func TestMergedWorkForRefusesADeferDoneMerge(t *testing.T) {
	mr := mergedMR("mr-d", "mg-ac0c", refinery.StatusMerged)
	mr.DeferDone = true
	if _, ok := mergedWorkFor([]refinery.MergeRequest{mr}, "mg-ac0c"); !ok {
		t.Error("a --defer-done merge onto the default branch was not treated as landed work")
	}
}

// History is oldest-first, so the newest match is the one reported. Which one is
// reported matters for the message and not for the verdict.
func TestMergedWorkForReportsTheMostRecentMerge(t *testing.T) {
	history := []refinery.MergeRequest{
		mergedMR("mr-old", "mg-ac0c", refinery.StatusMerged),
		mergedMR("mr-new", "mg-ac0c", refinery.StatusMerged),
	}
	got, ok := mergedWorkFor(history, "mg-ac0c")
	if !ok || got.MR != "mr-new" {
		t.Errorf("wanted the newest merge mr-new, got %+v (found=%t)", got, ok)
	}
}

// nil is "cannot tell", never "nothing merged": a daemon with no refinery
// performs no merges, so dispatching is the right direction.
func TestRefineryMergedWorkFailsOpenWithoutARefinery(t *testing.T) {
	if _, ok := refineryMergedWork(nil).MergedWork("mg-ac0c"); ok {
		t.Error("a nil thunk reported merged work")
	}
	if _, ok := refineryMergedWork(func() *refinery.Refinery { return nil }).MergedWork("mg-ac0c"); ok {
		t.Error("a nil refinery reported merged work")
	}
	if _, ok := mergedWorkFor(nil, ""); ok {
		t.Error("a blank work item id reported merged work")
	}
}

// A gate that is constructed and never installed is the defect it exists to
// close, one layer up: spawn-polecat would go on accepting a dispatch onto
// merged work and the only evidence would be an absence. The gate's own default
// is DISARMED — it has to be, since internal/agent may not import the refinery —
// so this wiring is the whole of its enforcement and nothing else would notice
// it going missing.
func TestTheMergedWorkGateIsWiredToTheLiveRefinery(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "main.go"))

	if !strings.Contains(src, "agentRegistry.SetMergedWorkGate(refineryMergedWork(func() *refinery.Refinery { return mergeQueue }))") {
		t.Error("pogod does not install the merged-work dispatch gate over the live refinery, so " +
			"`pogo agent spawn-polecat` still accepts a dispatch onto an item whose branch has " +
			"already merged (mg-9d4e)")
	}
}

// And the merged-but-not-closed alert, at the one call site that establishes the
// fact. Before mg-9d4e that branch was a log.Printf which could not tell the
// benign already-done race from the state this ticket is about.
func TestTheMergedButOpenAlertIsWiredToTheFailedClose(t *testing.T) {
	src := stripGoComments(readSourceFile(t, "reap.go"))

	if !strings.Contains(src, "reportMergedButOpen(mr, who, completeErr)") {
		t.Error("the post-merge close path does not raise the merged-but-not-closed alert, so a " +
			"work item that merged and refused to close is again reported by nothing (mg-9d4e)")
	}
}
