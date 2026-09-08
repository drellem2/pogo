package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/refinery"
)

// failedMR is the merge request OnFailed is handed. It is in the refinery's
// history by then — appended before the callbacks fire — which is why every
// test below puts it there too.
func guardFailedMR(id, author, branch string) *refinery.MergeRequest {
	return &refinery.MergeRequest{
		ID: id, Author: author, Branch: branch,
		TargetRef: "main", Status: refinery.StatusFailed,
	}
}

func guardMergedMR(id, author, branch, target, sha string, prFlow bool) refinery.MergeRequest {
	return refinery.MergeRequest{
		ID: id, Author: author, Branch: branch, TargetRef: target,
		Status: refinery.StatusMerged, MergedSHA: sha, PRFlow: prFlow,
		DoneTime: time.Now(),
	}
}

// recordingReopen stands in for client.ReopenMGWorkItem and remembers whether
// it was called at all — "did not run" and "ran and was refused" are different
// facts, and only the first is what this guard buys.
func recordingReopen(err error, calls *[]string) func(string) error {
	return func(id string) error {
		*calls = append(*calls, id)
		return err
	}
}

// TestReopenDeclinedWhenTheAuthorAlreadyMerged is the reported defect
// (drellem2/pogo#164, mg-4d21). One author, two merge requests: the first
// merged and closed the item, the second failed. The old code ran `mg reopen`
// unconditionally, which moves a done item to claimed/ — so a completed item
// came back as work in progress that nothing owned.
func TestReopenDeclinedWhenTheAuthorAlreadyMerged(t *testing.T) {
	failed := guardFailedMR("mr-2", "mg-4d21", "polecat-t4d21-docs")
	history := []refinery.MergeRequest{
		guardMergedMR("mr-1", "mg-4d21", "polecat-t4d21", "main", "45b4421abc", false),
		*failed,
	}
	var calls []string
	res := reopenAfterFailure(failed, history, recordingReopen(nil, &calls))

	if res.Outcome != reopenDeclinedLanded {
		t.Fatalf("outcome = %q, want %q", res.Outcome, reopenDeclinedLanded)
	}
	if len(calls) != 0 {
		t.Fatalf("mg reopen ran anyway (%v) — the guard has to prevent the call, not classify it after", calls)
	}
	if res.Landed.ID != "mr-1" {
		t.Errorf("named the wrong merge: %q", res.Landed.ID)
	}
	line := reopenMailLine(failed, res)
	for _, want := range []string{"was NOT reopened", "mr-1", "polecat-t4d21", "main", "45b4421", "mg reopen mg-4d21"} {
		if !strings.Contains(line, want) {
			t.Errorf("mail line does not carry %q so the claim cannot be checked: %s", want, line)
		}
	}
}

// TestReopenDeclinedWhenTheLandedMergeWasPRFlow is the shape the report was
// filed against: three branches at --target=develop, which is a PR-flow target.
// The dispatch gate (mergedWorkFor) deliberately IGNORES PR-flow merges because
// real work remains after them; this guard must not, because the polecat closes
// the item itself on that lane and the close is deliberate.
func TestReopenDeclinedWhenTheLandedMergeWasPRFlow(t *testing.T) {
	failed := guardFailedMR("mr-2", "mg-4d21", "polecat-t4d21-follow-up")
	history := []refinery.MergeRequest{
		guardMergedMR("mr-1", "mg-4d21", "polecat-t4d21", "develop", "deadbeef99", true),
		*failed,
	}
	var calls []string
	res := reopenAfterFailure(failed, history, recordingReopen(nil, &calls))

	if res.Outcome != reopenDeclinedLanded || len(calls) != 0 {
		t.Fatalf("a PR-flow merge did not block the reopen: outcome=%q calls=%v", res.Outcome, calls)
	}
	// The dispatch gate's opposite ruling is intentional and is pinned here so
	// the two cannot be "unified" by someone reading only one of them.
	if _, ok := mergedWorkFor(history, "mg-4d21"); ok {
		t.Error("mergedWorkFor now counts PR-flow merges; the two rules have drifted")
	}
}

// TestReopenStillHappensWhenNothingLanded is the case mg-06f2 built the reopen
// for, and the guard must leave it alone: the author's only merge requests
// failed, so the item is closed over work that is not on the target.
//
// The failed MR is in the history, which is the trap this pins: a scan that
// matched any MR by this author would match the failure itself and never
// reopen anything again.
func TestReopenStillHappensWhenNothingLanded(t *testing.T) {
	failed := guardFailedMR("mr-2", "mg-4d21", "polecat-t4d21")
	history := []refinery.MergeRequest{
		{ID: "mr-1", Author: "mg-4d21", Branch: "polecat-t4d21", Status: refinery.StatusFailed},
		*failed,
	}
	var calls []string
	res := reopenAfterFailure(failed, history, recordingReopen(nil, &calls))

	if res.Outcome != reopenPerformed {
		t.Fatalf("outcome = %q, want %q", res.Outcome, reopenPerformed)
	}
	if len(calls) != 1 || calls[0] != "mg-4d21" {
		t.Fatalf("mg reopen calls = %v, want one for mg-4d21", calls)
	}
	line := reopenMailLine(failed, res)
	for _, want := range []string{"REOPENED", "NOTHING OWNS IT", "no pid", "available/"} {
		if !strings.Contains(line, want) {
			t.Errorf("the flip is announced without %q, which is what made the last one need a hand repair: %s", want, line)
		}
	}
}

// TestReopenIgnoresAnotherAuthorsMerge: the guard is keyed on the author, and a
// different item's landed merge must not suppress this one's retry.
func TestReopenIgnoresAnotherAuthorsMerge(t *testing.T) {
	failed := guardFailedMR("mr-2", "mg-4d21", "polecat-t4d21")
	history := []refinery.MergeRequest{
		guardMergedMR("mr-1", "mg-9999", "polecat-t9999", "main", "abc1234", false),
		*failed,
	}
	var calls []string
	if res := reopenAfterFailure(failed, history, recordingReopen(nil, &calls)); res.Outcome != reopenPerformed {
		t.Fatalf("another author's merge blocked the reopen: %q", res.Outcome)
	}
}

// TestReopenAuthorMatchIsFoldedAndTrimmed: --author is a free string, and a
// guard that missed on case or a stray space would fail OPEN — back to the
// defect.
func TestReopenAuthorMatchIsFoldedAndTrimmed(t *testing.T) {
	failed := guardFailedMR("mr-2", "  MG-4D21 ", "polecat-t4d21")
	history := []refinery.MergeRequest{
		guardMergedMR("mr-1", "mg-4d21", "polecat-t4d21", "main", "abc1234", false),
		*failed,
	}
	var calls []string
	if res := reopenAfterFailure(failed, history, recordingReopen(nil, &calls)); res.Outcome != reopenDeclinedLanded {
		t.Fatalf("outcome = %q, want the landed merge to be found across case and padding", res.Outcome)
	}
}

// TestReopenSkippedWithoutAnAuthor: a hand-submitted branch with no --author
// names no work item, so nothing is reopened, nothing is said and nothing is
// counted.
func TestReopenSkippedWithoutAnAuthor(t *testing.T) {
	for _, author := range []string{"", "   "} {
		failed := guardFailedMR("mr-2", author, "some-branch")
		var calls []string
		res := reopenAfterFailure(failed, nil, recordingReopen(nil, &calls))
		if res.Outcome != reopenSkippedNoAuthor || len(calls) != 0 {
			t.Fatalf("author %q: outcome=%q calls=%v", author, res.Outcome, calls)
		}
		if line := reopenMailLine(failed, res); line != "" {
			t.Errorf("author %q: mail line names no item but says something: %s", author, line)
		}
		if _, ok := reopenEvent(failed, res); ok {
			t.Errorf("author %q: emitted an event about no work item", author)
		}
	}
}

// TestReopenClassifiesMGRefusals: the two benign refusals stay benign and
// everything else stays a failure. An archived item is the mg-3ba8 negative
// instance — `mg reopen` cannot reach archive/, which is why prompt archiving
// masked this defect rather than avoiding it.
func TestReopenClassifiesMGRefusals(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want reopenOutcome
	}{
		{"still claimed", fmt.Errorf("%w: x", client.ErrMGWorkItemNotDone), reopenNotDone},
		{"archived", fmt.Errorf("%w: x", client.ErrMGWorkItemArchived), reopenArchived},
		{"anything else", errors.New("mg reopen failed: store unreadable"), reopenFailed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			failed := guardFailedMR("mr-2", "mg-4d21", "polecat-t4d21")
			var calls []string
			res := reopenAfterFailure(failed, []refinery.MergeRequest{*failed}, recordingReopen(tc.err, &calls))
			if res.Outcome != tc.want {
				t.Fatalf("outcome = %q, want %q", res.Outcome, tc.want)
			}
			if res.Err == nil {
				t.Error("mg's own refusal was dropped")
			}
		})
	}
}

// TestReopenMailLineExistsForEveryActedOutcome. The flip was repaired by hand
// because the MERGE FAILED mail described the merge and never the side effect
// on the work item. Every outcome that reaches an item now says what happened
// to it, including the ones where the answer is "nothing".
func TestReopenMailLineExistsForEveryActedOutcome(t *testing.T) {
	failed := guardFailedMR("mr-2", "mg-4d21", "polecat-t4d21")
	for _, out := range []reopenOutcome{reopenDeclinedLanded, reopenPerformed, reopenNotDone, reopenArchived, reopenFailed} {
		res := reopenResult{Outcome: out, Err: errors.New("some refusal")}
		line := reopenMailLine(failed, res)
		if line == "" {
			t.Errorf("outcome %q says nothing in the failure mail", out)
			continue
		}
		if !strings.HasPrefix(line, "Work item: mg-4d21 ") {
			t.Errorf("outcome %q does not name the item first: %s", out, line)
		}
	}
}

// TestReopenEventCoversTheRareOutcomesOnly. The flip and the refusal that
// prevents it are recorded as they are made; "still claimed" is the ordinary
// case (22 in one log) and an event per failure would bury the rare one.
func TestReopenEventCoversTheRareOutcomesOnly(t *testing.T) {
	failed := guardFailedMR("mr-2", "mg-4d21", "polecat-t4d21")
	emitted := map[reopenOutcome]bool{
		reopenPerformed: true, reopenDeclinedLanded: true, reopenFailed: true,
		reopenNotDone: false, reopenArchived: false, reopenSkippedNoAuthor: false,
	}
	for out, want := range emitted {
		res := reopenResult{Outcome: out, Landed: guardMergedMR("mr-1", "mg-4d21", "polecat-t4d21", "main", "45b4421abc", false)}
		ev, ok := reopenEvent(failed, res)
		if ok != want {
			t.Errorf("outcome %q emitted=%v, want %v", out, ok, want)
			continue
		}
		if !ok {
			continue
		}
		if ev.EventType != "work_item_reopen_after_merge_failure" || ev.WorkItemID != "mg-4d21" {
			t.Errorf("outcome %q: wrong envelope %+v", out, ev)
		}
		if ev.Details["outcome"] != string(out) {
			t.Errorf("outcome %q: details say %v", out, ev.Details["outcome"])
		}
		if out == reopenDeclinedLanded && ev.Details["landed_sha"] != "45b4421abc" {
			t.Errorf("the declined event does not name the commit it declined on: %+v", ev.Details)
		}
	}
}

// TestReopenIsWiredIntoOnFailed pins the call site. The guard is only a guard if
// the daemon runs it, and the old unconditional `client.ReopenMGWorkItem` in
// that callback is exactly what it replaces.
func TestReopenIsWiredIntoOnFailed(t *testing.T) {
	src := readSourceFile(t, "main.go")
	if !strings.Contains(src, "reopenAfterFailure(mr, refineryHistoryOrNil(mergeQueue), client.ReopenMGWorkItem)") {
		t.Error("OnFailed does not run the reopen guard")
	}
	if strings.Contains(src, "if err := client.ReopenMGWorkItem(mr.Author); err != nil {") {
		t.Error("the unconditional reopen is still in OnFailed")
	}
	if !strings.Contains(src, "reopenMailLine(mr, reopenRes)") {
		t.Error("the failure mail does not carry the work item's disposition")
	}
}
