package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// flowStore is a FlowReader over a fixed map of work item id → carrier.
type flowStore map[string]FlowCarrier

func (f flowStore) ReadFlow(id string) (FlowCarrier, bool) {
	c, ok := f[id]
	return c, ok
}

func ghBuild(stage string) FlowCarrier {
	return FlowCarrier{Workflow: GHIssueWorkflow, Stage: stage}
}

func ghReview(build string) FlowCarrier {
	return FlowCarrier{Workflow: GHIssueWorkflow, Stage: "review", Reviews: build}
}

// flowRegistry builds a capped registry whose goRepo holds one live polecat per
// entry of live (polecat name → work item id), with carriers read from store.
func flowRegistry(t *testing.T, store flowStore, live map[string]string) *Registry {
	t.Helper()
	reg := capRegistry(t, 0)
	for name, id := range live {
		a := livePolecat(name, id)
		a.SourceRepo = goRepo
		reg.agents[name] = a
	}
	reg.SetFlowReader(store)
	return reg
}

// TestSecondGHIssueBuildIsRefused is the 2026-09-08 incident, stopped at the
// dispatch that built it: with one gh-issue build live, a second one needs two
// slots and only one is free once the first flow's reviewer is accounted for.
// Before mg-bf42 this was 1 of 3 and admitted, and so was the third.
func TestSecondGHIssueBuildIsRefused(t *testing.T) {
	store := flowStore{"mg-1530": ghBuild("build"), "mg-38d1": ghBuild("build")}
	reg := flowRegistry(t, store, map[string]string{"t1530": "mg-1530"})

	occ := reg.RepoOccupancyFor(goRepo)
	if len(occ.ReviewSlotHolds) != 1 || occ.ReviewSlotHolds[0].Build != "mg-1530" {
		t.Fatalf("holds = %+v, want one for mg-1530 — a live gh-issue builder held no slot for its reviewer", occ.ReviewSlotHolds)
	}
	if occ.WouldRefuse {
		t.Errorf("an ordinary dispatch was refused at 1 live + 1 held of 3: %+v", occ)
	}
	if !occ.WouldRefuseGHIssueBuild {
		t.Errorf("WouldRefuseGHIssueBuild = false at 1 live + 1 held of 3 — a coordinator planning from host load would dispatch the deadlock")
	}

	rr := spawnIntoRepo(t, reg, "38d1", goRepo) // Id mg-38d1, a gh-issue build
	if rr.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 — a second gh-issue flow was admitted into a cap-3 repo: %s", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	for _, want := range []string{"HELD", "t1530 (mg-1530)", "TWO slots", "LATER", "mg-bf42"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal missing %q; got: %s", want, body)
		}
	}
}

// TestReviewerIsAdmittedIntoItsHeldSlot is the other half, and the one that
// makes the reserve a reserve rather than a lower cap: the slot is held FOR the
// reviewer, so a repo the reserve fills for everyone else still takes it.
func TestReviewerIsAdmittedIntoItsHeldSlot(t *testing.T) {
	store := flowStore{
		"mg-1530":  ghBuild("review"),
		"mg-097c":  ghReview("mg-1530"),
		"mg-other": {},
	}
	// Builder + one unrelated worker: 2 live + 1 held = 3 of 3.
	reg := flowRegistry(t, store, map[string]string{"t1530": "mg-1530", "pother": "mg-other"})

	occ := reg.RepoOccupancyFor(goRepo)
	if !occ.WouldRefuse {
		t.Fatalf("an ordinary dispatch was admitted into the slot held for mg-1530's reviewer: %+v", occ)
	}
	if msg := reg.repoCapRefusal(goRepo, "mg-other2"); !strings.Contains(msg, "HELD") {
		t.Errorf("ordinary dispatch refusal does not name the held slot: %q", msg)
	}
	if msg := reg.repoCapRefusal(goRepo, "mg-097c"); msg != "" {
		t.Fatalf("the reviewer the slot is held for was refused — this IS the deadlock: %s", msg)
	}
	// A reviewer for a DIFFERENT build does not get this builder's slot.
	store["mg-zzzz"] = ghReview("mg-nope")
	if msg := reg.repoCapRefusal(goRepo, "mg-zzzz"); msg == "" {
		t.Error("a reviewer for an unrelated build consumed mg-1530's held slot")
	}
}

// TestRunningReviewerReleasesTheHold. Once the reviewer is live it is in Count,
// and holding a slot for it as well would count it twice.
func TestRunningReviewerReleasesTheHold(t *testing.T) {
	store := flowStore{"mg-1530": ghBuild("review"), "mg-097c": ghReview("mg-1530")}
	reg := flowRegistry(t, store, map[string]string{"t1530": "mg-1530", "t097c": "mg-097c"})

	occ := reg.RepoOccupancyFor(goRepo)
	if len(occ.ReviewSlotHolds) != 0 {
		t.Fatalf("holds = %+v, want none — a running reviewer was held for again", occ.ReviewSlotHolds)
	}
	if occ.WouldRefuse {
		t.Errorf("refused at 2 of 3 with the flow's reviewer already running: %+v", occ)
	}
}

// TestOnlyBuildAndReviewStagesHold. At `merge` the reviewer has passed; a
// triage worker is not a builder; an item that is not on the gh-issue track
// declares nothing. None of them may shrink the repo.
func TestOnlyBuildAndReviewStagesHold(t *testing.T) {
	store := flowStore{
		"mg-a": ghBuild("merge"),
		"mg-b": ghBuild("triage"),
		"mg-c": {Workflow: "other", Stage: "build"},
	}
	reg := flowRegistry(t, store, map[string]string{"pa": "mg-a", "pb": "mg-b"})
	if occ := reg.RepoOccupancyFor(goRepo); len(occ.ReviewSlotHolds) != 0 {
		t.Errorf("holds = %+v, want none", occ.ReviewSlotHolds)
	}
	if msg := reg.repoCapRefusal(goRepo, "mg-c"); msg != "" {
		t.Errorf("a non-gh-issue item at 2 of 3 was refused: %s", msg)
	}
}

// TestUnreadableItemsHoldNothing: the reserve fails OPEN, like the cap. A work
// item the reader cannot find declares no flow.
func TestUnreadableItemsHoldNothing(t *testing.T) {
	reg := flowRegistry(t, flowStore{}, map[string]string{"pa": "mg-a", "pb": "mg-b"})
	if msg := reg.repoCapRefusal(goRepo, "mg-new"); msg != "" {
		t.Errorf("refused at 2 of 3 with nothing readable: %s", msg)
	}
}

// TestOneFlowFitsBesideTheRefineryReserve. The reserve drops the cap to 2,
// which is exactly one flow: an empty repo takes a gh-issue build, and that
// build's reviewer still fits beside it. A second flow does not.
func TestOneFlowFitsBesideTheRefineryReserve(t *testing.T) {
	store := flowStore{
		"mg-1": ghBuild("build"), "mg-2": ghBuild("build"),
		"mg-1r": ghReview("mg-1"),
	}
	reg := flowRegistry(t, store, nil)
	reg.SetRefineryActivity(RefineryActivityFunc(func(string) (bool, bool) { return true, true }))

	if msg := reg.repoCapRefusal(goRepo, "mg-1"); msg != "" {
		t.Fatalf("an empty repo at cap 2 refused a gh-issue build: %s", msg)
	}
	a := livePolecat("p1", "mg-1")
	a.SourceRepo = goRepo
	reg.agents["p1"] = a

	if msg := reg.repoCapRefusal(goRepo, "mg-1r"); msg != "" {
		t.Errorf("the reviewer of the only flow was refused at cap 2: %s", msg)
	}
	msg := reg.repoCapRefusal(goRepo, "mg-2")
	if msg == "" {
		t.Fatal("a second flow was admitted at cap 2")
	}
	if !strings.Contains(msg, "reserved for the refinery") {
		t.Errorf("refusal does not mention the refinery reserve that shrank the cap: %s", msg)
	}
}

// TestCapOfOneSaysAFlowCanNeverFit. max_polecats_per_repo = 1 cannot hold a
// builder and its reviewer at once, so a gh-issue build there would wait
// forever; the refusal must say the remedy is the configuration.
func TestCapOfOneSaysAFlowCanNeverFit(t *testing.T) {
	reg := flowRegistry(t, flowStore{"mg-1": ghBuild("build")}, nil)
	reg.SetDispatchCap(config.DispatchCapConfig{MaxPolecatsPerRepo: 1})
	msg := reg.repoCapRefusal(goRepo, "mg-1")
	if !strings.Contains(msg, "can NEVER hold a gh-issue flow") {
		t.Errorf("cap-1 refusal does not name the configuration: %q", msg)
	}
}

// TestDisarmedCapHoldsNothingBack. Zero means unlimited; the reserve is part
// of the cap and disarms with it.
func TestDisarmedCapHoldsNothingBack(t *testing.T) {
	reg := flowRegistry(t, flowStore{"mg-1": ghBuild("build"), "mg-2": ghBuild("build")},
		map[string]string{"p1": "mg-1"})
	reg.SetDispatchCap(config.DispatchCapConfig{})
	if msg := reg.repoCapRefusal(goRepo, "mg-2"); msg != "" {
		t.Errorf("a disarmed cap refused: %s", msg)
	}
}

// TestSurvivingBuilderStillHolds. A builder that outlived a pogod restart is
// known only to the witness, and it still needs its reviewer.
func TestSurvivingBuilderStillHolds(t *testing.T) {
	reg := flowRegistry(t, flowStore{"mg-1530": ghBuild("review")}, nil)
	if err := RecordPolecatWitness("t1530", liveProcess(t), "mg-1530", goRepo); err != nil {
		t.Fatal(err)
	}
	occ := reg.RepoOccupancyFor(goRepo)
	if len(occ.ReviewSlotHolds) != 1 {
		t.Errorf("holds = %+v, want the witnessed builder's", occ.ReviewSlotHolds)
	}
}

// TestHostLoadServesTheHolds: the coordinator plans from /agents/hostload, so
// the held slots and the gh-issue verdict must be on it.
func TestHostLoadServesTheHolds(t *testing.T) {
	reg := flowRegistry(t, flowStore{"mg-1": ghBuild("build")}, map[string]string{"p1": "mg-1"})
	rr := httptest.NewRecorder()
	reg.handleHostLoad(rr, httptest.NewRequest("GET", "/agents/hostload?repo="+goRepo, nil))
	var resp struct {
		RepoOccupancy map[string]any `json:"repo_occupancy"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.RepoOccupancy["would_refuse_gh_issue_build"] != true {
		t.Errorf("would_refuse_gh_issue_build not true: %v", resp.RepoOccupancy)
	}
	if holds, _ := resp.RepoOccupancy["review_slot_holds"].([]any); len(holds) != 1 {
		t.Errorf("review_slot_holds = %v, want one", resp.RepoOccupancy["review_slot_holds"])
	}
}

// TestReviewerWithoutReviewsLineIsStillAdmitted. `reviews:` is required on the
// review ticket but omitting it is silent, and such a ticket reads as
// `workflow: gh-issue, stage: review` — the same carrier a builder has once its
// PR is open. Charged as a build it would be refused the slot held for it,
// which is the deadlock this gate exists to prevent.
func TestReviewerWithoutReviewsLineIsStillAdmitted(t *testing.T) {
	store := flowStore{
		"mg-1530":  ghBuild("review"),
		"mg-097c":  {Workflow: GHIssueWorkflow, Stage: "review"}, // reviews: omitted
		"mg-other": {},
	}
	reg := flowRegistry(t, store, map[string]string{"t1530": "mg-1530", "pother": "mg-other"})
	if msg := reg.repoCapRefusal(goRepo, "mg-097c"); msg != "" {
		t.Fatalf("a review ticket missing its reviews: line was refused its held slot: %s", msg)
	}
}
