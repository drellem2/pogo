package agent

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/config"
)

// The question mg-4bf1 was filed to leave open, answered here (mg-4bf1).
//
// The ticket's central unknown was whether `spawn-polecat` would actually ALLOW
// a dispatch at an item whose branch is pushed, unmerged and already submitted
// to the refinery — and it was left unanswered on purpose, because the only test
// is to attempt the spawn and attempting it is the thing that causes the harm if
// the answer is yes. These tests are that attempt, run somewhere it can do no
// damage.
//
// THE ANSWER IS NO, and it does not depend on the refinery at all:
// strandedWorkRefusal reads the BRANCH off disk (strandedwork.Scan) and nothing
// in its inputs is a merge-request state, so submitting the branch does not
// disarm it. That is what makes the whole finding a REPORTING defect rather than
// a data-loss one — and it is a property worth pinning, because "skip the git
// work when the refinery is already merging it" is a one-line optimisation that
// reads as obviously safe and would open the hole for real.
//
// AND THE MAYOR'S PRECAUTION RESTS ON A PREMISE THAT IS FALSE. The 22:52Z note
// on the ticket says the cap refusal and the stranded refusal "are both a 409",
// so a tester running the experiment on a full repo would read a cap refusal as
// the stranded guard firing and close the ticket green. Measured here: the
// stranded refusal is 409 and the cap refusal is 503, and the stranded gate runs
// FIRST (api.go: strandedWorkRefusal at the conflict gates, repoCapRefusal down
// with the "later" gates). So a repo at cap does NOT mask this experiment, in
// either direction. The precaution is still good advice — read the refusal text
// — but the masking it was guarding against cannot happen while these two tests
// pass.

// TestSpawnStillRefusedWhileTheBranchIsAlreadySubmitted is the experiment the
// ticket declined to run on a live item.
func TestSpawnStillRefusedWhileTheBranchIsAlreadySubmitted(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-ta932", "netcontrol.md", "fix(netcontrol): the up probe (mg-a932)")

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "ua932", Id: "mg-a932", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("spawn at an item whose branch is pushed and unmerged: status = %d, want 409. "+
			"The refinery having the branch in its queue is not one of this gate's inputs and must "+
			"never become one", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "polecat-ta932") {
		t.Errorf("the refusal does not name the branch, which is what makes it distinguishable "+
			"from a cap refusal by TEXT as well as by status: %s", body)
	}
}

// TestStrandedRefusalIsNotMaskedByAFullRepo pins the ordering and the two status
// codes against the ticket's masking worry.
//
// The repo is at its cap AND the item has a stranded branch, which is the exact
// combination the mayor said would produce an unreadable answer. It does not:
// the stranded gate answers first, with 409 and the branch name, and the cap
// refusal — 503, naming the cap and the workers — never gets to speak.
func TestStrandedRefusalIsNotMaskedByAFullRepo(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-ta932", "netcontrol.md", "fix(netcontrol): the up probe (mg-a932)")

	sandboxWitness(t)
	reg := newDrainTestRegistry(t)
	reg.SetDispatchCap(config.DefaultDispatchCapConfig())
	for i := 0; i < config.DefaultMaxPolecatsPerRepo; i++ {
		name := string(rune('a'+i)) + "-cat"
		a := livePolecat(name, "mg-"+name)
		a.SourceRepo = repo
		reg.agents[name] = a
	}

	// The positive control for the cap half: with no stranded branch involved,
	// this same full repo really does refuse, and with the OTHER status. Without
	// it, a stranded 409 below would be consistent with a cap gate that is simply
	// not armed in this fixture.
	body, err := json.Marshal(SpawnPolecatAPIRequest{
		Name: "zcap", Id: "mg-zcap", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if err != nil {
		t.Fatal(err)
	}
	capRR := httptest.NewRecorder()
	reg.handleSpawnPolecat(capRR, httptest.NewRequest("POST", "/agents/spawn-polecat", strings.NewReader(string(body))))
	if capRR.Code != http.StatusServiceUnavailable {
		t.Fatalf("cap refusal status = %d, want 503 — the control did not fire, so the 409 below "+
			"says nothing about which gate answered", capRR.Code)
	}
	if !strings.Contains(capRR.Body.String(), "the cap is") {
		t.Errorf("the cap refusal does not name the cap: %s", capRR.Body.String())
	}

	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "ua932", Id: "mg-a932", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("stranded spawn into a FULL repo: status = %d, want 409. A 503 here would mean the "+
			"cap answered first and the stranded experiment is unrunnable on a busy repo, which is "+
			"what the ticket's 22:52Z precaution assumed", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "polecat-ta932") {
		t.Errorf("the refusal that won does not name the branch: %s", rr.Body.String())
	}
	if strings.Contains(rr.Body.String(), "the cap is") {
		t.Errorf("the cap refusal masked the stranded one: %s", rr.Body.String())
	}
}
