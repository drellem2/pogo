package stallwatch

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeRepoCarrierItem writes an available high-priority item in repo whose body
// leads with a state carrier block, the way the coordinator files gh-issue
// tickets.
func writeRepoCarrierItem(t *testing.T, workRoot, id, repo, carrier string, modTime time.Time) {
	t.Helper()
	path := filepath.Join(workRoot, "available", id+".md")
	content := fmt.Sprintf("---\nid: %s\ntype: task\nassignee: pm-pogo\nrepo: %s\npriority: high\n---\n# %s\n%s\n",
		id, repo, id, carrier)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatal(err)
	}
}

// oneBuilderNoReviewer is the 2026-09-28 macguffin shape: cap 3, one live
// gh-issue builder whose reviewer is not running, so one slot is held for that
// reviewer. An ordinary dispatch fits (1 + 1 held < 3); a gh-issue BUILD, which
// needs two slots, does not (1 + 1 held + 2 > 3). Both verdicts are the spawn
// gate's, as agent.RepoOccupancy reports them.
func oneBuilderNoReviewer(ghRefused bool) *fakeCapacity {
	return &fakeCapacity{byRepo: map[string]RepoCapacity{
		capRepoA: {
			Repo: capRepoA, Count: 1, Cap: 3, Polecats: []string{"p28dcf"},
			AtCapGHIssueBuild: ghRefused, ReviewSlotsHeld: 1,
		},
	}}
}

// TestPriorityWakeReportsAQueuedGHBuildAsAtCap is mg-1acf2: the wake told the
// coordinator "1 can be dispatched now: mg-715a2" while the spawn gate would
// refuse that gh-issue build for want of its reviewer's slot.
func TestPriorityWakeReportsAQueuedGHBuildAsAtCap(t *testing.T) {
	w, rec, workRoot := capEnv(t, priorityCfg(), oneBuilderNoReviewer(true))
	now := time.Now()
	writeRepoCarrierItem(t, workRoot, "mg-715a2", capRepoA, "workflow: gh-issue\nstage: build\ngh: x/y#39", now.Add(-10*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if !strings.HasPrefix(msg, "priority-wake:") {
		t.Fatalf("expected the priority-wake notice, got: %s", msg)
	}
	for _, bad := range []string{"can be dispatched now", "claim or dispatch now"} {
		if strings.Contains(msg, bad) {
			t.Errorf("a gh-issue build the gate refuses was offered for dispatch (%q): %s", bad, msg)
		}
	}
	for _, want := range []string{"mg-715a2", "gh-issue BUILD", "2 slots", "p28dcf", "1 slot(s) held", "LATER"} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing %q in at-cap gh-build notice: %s", want, msg)
		}
	}
	// A repo below its plain count must not be described as "at its cap of 3"
	// with one worker in it — that reads as a miscount.
	if strings.Contains(msg, "is at its cap of 3 (1 worker") {
		t.Errorf("gh-build refusal rendered as a plain at-cap: %s", msg)
	}
	d := lastDetails(t, rec)
	assertIDs(t, d, "at_cap_ids", []string{"mg-715a2"})
	if _, ok := d["dispatchable_ids"]; ok {
		t.Errorf("dispatchable_ids stamped for a refused gh build: %v", d["dispatchable_ids"])
	}
}

// TestGHBuildChargeLeavesOtherItemsDispatchable: the two-slot charge is per
// ITEM, not per repo. In the same repo an ordinary item and a gh-issue review
// ticket still fit, and must still be asked for; an item whose carrier the gate
// cannot read is charged as the gate charges it — one slot.
func TestGHBuildChargeLeavesOtherItemsDispatchable(t *testing.T) {
	w, rec, workRoot := capEnv(t, priorityCfg(), oneBuilderNoReviewer(true))
	now := time.Now()
	old := now.Add(-10 * time.Minute)
	writeRepoCarrierItem(t, workRoot, "mg-build", capRepoA, "workflow: gh-issue\nstage: build", old)
	writeRepoCarrierItem(t, workRoot, "mg-plain", capRepoA, "", old)
	writeRepoCarrierItem(t, workRoot, "mg-revw", capRepoA, "workflow: gh-issue\nstage: review\nreviews: mg-other", old)
	writeRepoCarrierItem(t, workRoot, "mg-trig", capRepoA, "workflow: gh-issue\nstage: triage", old)

	w.Check(now)

	d := lastDetails(t, rec)
	assertIDs(t, d, "at_cap_ids", []string{"mg-build"})
	msg := lastNudge(t, rec)
	if !strings.Contains(msg, "3 can be dispatched now") {
		t.Errorf("the items the gate admits must still be asked for: %s", msg)
	}
	ask := msg[strings.Index(msg, "can be dispatched now:"):]
	if before, _, ok := strings.Cut(ask, ". "); ok && strings.Contains(before, "mg-build") {
		t.Errorf("the refused gh build appeared in the dispatch imperative: %s", msg)
	}
}

// TestGHBuildWithRoomIsDispatchable is the positive control: the same gh-issue
// build in a repo whose two-slot check passes is an ordinary dispatch request,
// so the classification above comes from the gate's verdict and not from the
// carrier alone.
func TestGHBuildWithRoomIsDispatchable(t *testing.T) {
	w, rec, workRoot := capEnv(t, priorityCfg(), oneBuilderNoReviewer(false))
	now := time.Now()
	writeRepoCarrierItem(t, workRoot, "mg-715a2", capRepoA, "workflow: gh-issue\nstage: build", now.Add(-10*time.Minute))

	w.Check(now)

	msg := lastNudge(t, rec)
	if !strings.Contains(msg, "claim or dispatch now: mg-715a2") {
		t.Errorf("a gh build the gate admits must be asked for: %s", msg)
	}
}
