package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/agent"
	"github.com/drellem2/pogo/internal/client"
	"github.com/drellem2/pogo/internal/refinery"
)

// mg-d86e. On 2026-07-29, between 16:05 and 16:10Z, two release items merged a
// version bump to main, and pogod's merge-success path marked each one done and
// stopped its polecat. Neither polecat reached the tag step; neither release
// existed. `mg show` said done, the result sidecar said completed_by=refinery,
// the refinery mailed MERGED, CI was green — and `git describe` said v0.6.0.
//
//	mg-ca3c   pogo v0.7.0
//	mg-9f17   macguffin v0.3.0
//
// Both are used here as fixtures rather than an invented scenario, because the
// shape of the bug is exactly "a real release ticket, merged to the default
// branch, by a polecat that passed no flag" — every other lane (--defer-done,
// PR flow) already leaves such a polecat alone, and neither applies here.
//
// The control that matters is the POSITIVE one: an item WITHOUT the declaration
// must still be auto-completed. A suite that only asserts the declared item
// survives passes just as well when the completion path is broken outright,
// which would re-open the polecat leak of gh #35.

// declaringStore is the fake work-item store: a set of ids carrying
// client.PostMergeWorkTag. Anything else answers "no declaration", exactly as
// the real probe does for an ordinary build ticket.
type declaringStore struct {
	declared map[string]bool
	err      error
	probed   []string
}

func (s *declaringStore) probe(id string) (bool, error) {
	s.probed = append(s.probed, id)
	if s.err != nil {
		return false, s.err
	}
	return s.declared[id], nil
}

// releaseMR builds the merge request each release polecat actually produced: a
// version-bump branch, merged to main, submitted with no --defer-done.
func releaseMR(workItem string) *refinery.MergeRequest {
	return &refinery.MergeRequest{
		ID:        "mr-" + strings.TrimPrefix(workItem, "mg-"),
		Branch:    "polecat-" + strings.TrimPrefix(workItem, "mg-"),
		Author:    workItem,
		TargetRef: "main",
	}
}

func polecatRegistry(workItems ...string) *fakeReaper {
	agents := map[string]*agent.Agent{}
	for _, id := range workItems {
		name := strings.TrimPrefix(id, "mg-")
		agents[name] = &agent.Agent{Name: name, WorkItemID: id, Type: agent.TypePolecat}
	}
	return &fakeReaper{agents: agents}
}

// TestMergeCompletion_ConsultsTheWorkItem is the acceptance control, run in
// BOTH directions against the same probe. The declared items are the two real
// releases; the undeclared one is mg-bdda, an ordinary gitgc fix that merged to
// main the same day and whose auto-completion was correct.
func TestMergeCompletion_ConsultsTheWorkItem(t *testing.T) {
	cases := []struct {
		workItem     string
		declared     bool
		wantComplete bool
	}{
		{"mg-ca3c", true, false}, // pogo v0.7.0 — tag, artifacts, all after the merge
		{"mg-9f17", true, false}, // macguffin v0.3.0 — same shape, ten minutes later
		{"mg-bdda", false, true}, // ordinary fix: the branch IS the deliverable
	}

	store := &declaringStore{declared: map[string]bool{"mg-ca3c": true, "mg-9f17": true}}

	for _, tc := range cases {
		t.Run(tc.workItem, func(t *testing.T) {
			reg := polecatRegistry(tc.workItem)
			var escalations []string
			backstop, _ := newTestBackstop(reg, &escalations)

			mr := releaseMR(tc.workItem)
			verdict := resolvePostMergeWork(reg, mr, store.probe)

			var completedID string
			reapMergedPolecat(reg, mr, func(id, _ string) error {
				completedID = id
				return nil
			}, verdict, backstop)

			if verdict.Declared != tc.declared {
				t.Errorf("verdict.Declared = %v for %s, want %v (reason=%q)",
					verdict.Declared, tc.workItem, tc.declared, verdict.Reason)
			}

			bare := strings.TrimPrefix(tc.workItem, "mg-")
			if tc.wantComplete {
				if completedID != tc.workItem {
					t.Errorf("an item that declares NOTHING must still be auto-completed on merge; mg done was called for %q, want %q — "+
						"without this direction the suite passes with the completion path removed entirely (gh #35)",
						completedID, tc.workItem)
				}
				if len(reg.stopped) != 1 || reg.stopped[0] != bare {
					t.Errorf("expected the merged polecat %q to be stopped, got %v", bare, reg.stopped)
				}
				backstop.mu.Lock()
				_, armed := backstop.timers[bare]
				backstop.mu.Unlock()
				if armed {
					t.Error("no backstop should be armed on the completion fast path")
				}
				return
			}

			if completedID != "" {
				t.Errorf("%s declares post-merge work, and the refinery marked it done anyway — this is the mg-d86e defect: "+
					"the tag step never ran and the release was reported complete", tc.workItem)
			}
			if len(reg.stopped) != 0 {
				t.Errorf("%s declares post-merge work, and its polecat was stopped %v — the remaining work cannot happen even in principle",
					tc.workItem, reg.stopped)
			}
			backstop.mu.Lock()
			_, armed := backstop.timers[bare]
			backstop.mu.Unlock()
			if !armed {
				t.Errorf("a polecat left running past its merge must be bounded by the backstop, or it holds its slot forever (gh #34/#81)")
			}
		})
	}

	if len(store.probed) != len(cases) {
		t.Errorf("the work item was probed %d times for %d merges (%v) — the marker is only load-bearing if it is actually read",
			len(store.probed), len(cases), store.probed)
	}
}

// TestResolvePostMergeWork_UnreadableItemDeclines fixes the direction an
// unreadable item resolves in. "I could not read the ticket" is not evidence
// that merging completed it, and the whole defect is a completion asserted
// without the standing to assert it. Declining costs a bounded backstop window;
// completing wrongly costs a silently truncated ticket that nothing catches.
func TestResolvePostMergeWork_UnreadableItemDeclines(t *testing.T) {
	reg := polecatRegistry("mg-ca3c")
	store := &declaringStore{err: errors.New("mg show: store unavailable")}

	v := resolvePostMergeWork(reg, releaseMR("mg-ca3c"), store.probe)

	if !v.Declared {
		t.Fatal("an unreadable work item must NOT be treated as completed by its merge")
	}
	if !strings.Contains(v.Reason, "store unavailable") {
		t.Errorf("the reason must carry the underlying failure so the log and the MERGED mail say why, got %q", v.Reason)
	}
}

// TestResolvePostMergeWork_SkipsNonPolecatMerges keeps the probe off merges the
// reap path ignores anyway. A crew agent's name is not a work-item id, so
// probing it would fail and — under the rule above — mislabel an ordinary crew
// merge as having post-merge work.
func TestResolvePostMergeWork_SkipsNonPolecatMerges(t *testing.T) {
	reg := &fakeReaper{agents: map[string]*agent.Agent{
		"mayor": {Name: "mayor", Type: agent.TypeCrew},
		"ca3c":  {Name: "ca3c", WorkItemID: "mg-ca3c", Type: agent.TypePolecat},
	}}
	store := &declaringStore{err: errors.New("no such work item")}

	for _, mr := range []*refinery.MergeRequest{
		{ID: "mr-1", Branch: "b", Author: "mayor", TargetRef: "main"},
		{ID: "mr-2", Branch: "b", Author: "", TargetRef: "main"},
		{ID: "mr-3", Branch: "b", Author: "mg-nobody", TargetRef: "main"},
	} {
		if v := resolvePostMergeWork(reg, mr, store.probe); v.Declared {
			t.Errorf("author %q: expected no verdict for a merge the reap path ignores, got %q", mr.Author, v.Reason)
		}
	}
	if len(store.probed) != 0 {
		t.Errorf("expected no work-item probe for non-polecat merges, probed %v", store.probed)
	}
}

// TestResolvePostMergeWork_SkipsAlreadyDeferredMerges avoids a redundant probe
// on the two lanes that already defer. They reach the same outcome by their own
// route and carry their own reason strings, which are more specific than this
// one.
func TestResolvePostMergeWork_SkipsAlreadyDeferredMerges(t *testing.T) {
	reg := polecatRegistry("mg-ca3c")
	store := &declaringStore{declared: map[string]bool{"mg-ca3c": true}}

	deferred := releaseMR("mg-ca3c")
	deferred.DeferDone = true
	if v := resolvePostMergeWork(reg, deferred, store.probe); v.Declared {
		t.Errorf("--defer-done already defers; expected no second verdict, got %q", v.Reason)
	}

	prFlow := releaseMR("mg-ca3c")
	prFlow.PRFlow = true
	if v := resolvePostMergeWork(reg, prFlow, store.probe); v.Declared {
		t.Errorf("PR flow already defers; expected no second verdict, got %q", v.Reason)
	}

	if len(store.probed) != 0 {
		t.Errorf("expected no probe for already-deferred merges, probed %v", store.probed)
	}
}

// TestResolvePostMergeWork_NoProbeKeepsFastPath pins the behaviour when pogod
// has no probe wired: the merge completes the item exactly as it did before
// mg-d86e. The marker adds a way to decline; it does not change the default.
func TestResolvePostMergeWork_NoProbeKeepsFastPath(t *testing.T) {
	reg := polecatRegistry("mg-ca3c")
	if v := resolvePostMergeWork(reg, releaseMR("mg-ca3c"), nil); v.Declared {
		t.Errorf("with no probe configured the fast path must stand, got %q", v.Reason)
	}
}

// TestPostMergeVerdict_ReasonNamesTheTag is what an operator reads in the log
// and in the coordinator's MERGED mail. "Not completed" without the reason
// sends them to the source; naming the tag and the item ends the question.
func TestPostMergeVerdict_ReasonNamesTheTag(t *testing.T) {
	reg := polecatRegistry("mg-9f17")
	store := &declaringStore{declared: map[string]bool{"mg-9f17": true}}

	v := resolvePostMergeWork(reg, releaseMR("mg-9f17"), store.probe)

	if !strings.Contains(v.Reason, "mg-9f17") || !strings.Contains(v.Reason, client.PostMergeWorkTag) {
		t.Errorf("reason must name both the item and the tag that produced the verdict, got %q", v.Reason)
	}
}

// TestDeclaredMerge_SurvivesARealMerge is the end-to-end half: a real bare
// origin, a real refinery loop, a real fast-forward merge onto the default
// branch, and the same two-line composition pogod runs in its OnMerged hook.
// The unit tests above hand-build the verdict; this one earns it, because the
// mg-7746 version of this bug survived a passing unit suite.
func TestDeclaredMerge_SurvivesARealMerge(t *testing.T) {
	fx := newPRFlowFixture(t, "mg-ca3c", "main", "")
	store := &declaringStore{declared: map[string]bool{"mg-ca3c": true}}

	out, mr := runMergeThroughReap(t, fx, "main", store.probe)

	if mr.PRFlow || mr.DeferDone {
		t.Fatalf("fixture must reproduce the real case — a plain default-branch merge with no flags (PRFlow=%v DeferDone=%v)",
			mr.PRFlow, mr.DeferDone)
	}
	if !out.postMerge.Declared {
		t.Fatalf("the merge resolved with no post-merge verdict for a declaring item (reason=%q)", out.postMerge.Reason)
	}
	if out.completedID != "" {
		t.Errorf("mg-ca3c was marked done by the merge (%q) — the tag it still owes would never be pushed", out.completedID)
	}
	if len(out.stopped) != 0 {
		t.Errorf("the polecat was stopped at merge time (%v); it cannot tag a release it no longer exists to tag", out.stopped)
	}
	if !out.armed {
		t.Error("expected the bounded backstop to be armed for the surviving polecat")
	}
}

// TestUndeclaredMerge_StillCompletesThroughARealMerge is the end-to-end
// positive control, and the one that would fail if this change had simply
// disabled event-driven completion.
func TestUndeclaredMerge_StillCompletesThroughARealMerge(t *testing.T) {
	fx := newPRFlowFixture(t, "mg-bdda", "main", "")
	store := &declaringStore{declared: map[string]bool{"mg-ca3c": true, "mg-9f17": true}}

	out, _ := runMergeThroughReap(t, fx, "main", store.probe)

	if out.postMerge.Declared {
		t.Fatalf("mg-bdda declares nothing, but the merge resolved as deferred: %q", out.postMerge.Reason)
	}
	if out.completedID != "mg-bdda" {
		t.Errorf("an undeclared item must still be completed by its merge; mg done was called for %q", out.completedID)
	}
	if len(out.stopped) != 1 || out.stopped[0] != "bdda" {
		t.Errorf("expected the merged polecat to be stopped, got %v", out.stopped)
	}
	if len(store.probed) == 0 {
		t.Error("the work item was never probed — the fast path must be REACHED through the check, not around it")
	}
}
