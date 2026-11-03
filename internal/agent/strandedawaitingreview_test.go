package agent

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/ghpr"
	"github.com/drellem2/pogo/internal/strandedwork"
)

// drellem2/pogo#147 (mg-dbb75): the PR track's builder is stopped to wait for
// its review with its work pushed and unmerged — which, against the default
// branch, is exactly what mg-9a19 looked like. Both agent-driven emitters now
// ask whether an open PR owns the head before alerting.

// stubPRProbe installs a PR probe for one test.
func stubPRProbe(t *testing.T, probe strandedwork.PRProbe) {
	t.Helper()
	prev := strandedPRProbe
	strandedPRProbe = probe
	t.Cleanup(func() { strandedPRProbe = prev })
}

// TestReleasingAPolecatWhoseBranchIsAnOpenPRMailsNobody: no mail, no
// work_item_stranded_push — and a work_item_push_awaiting_review event plus a
// log line, because a suppression nobody can count is the same silence as a
// dead detector.
func TestReleasingAPolecatWhoseBranchIsAnOpenPRMailsNobody(t *testing.T) {
	logPath := useTempEventLog(t)
	logs := captureLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built, PR open (mg-a95f)")
	stubPRProbe(t, func(_, branch string) (ghpr.Answer, error) {
		if branch == "polecat-pa95f" {
			return ghpr.Answer{Number: 146, State: "OPEN"}, nil
		}
		return ghpr.Answer{}, nil
	})

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})
	a := livePolecat("pa95f", "mg-a95f")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}

	if sent := mail(); len(sent) != 0 {
		subject, _ := sent[0].Message()
		t.Fatalf("a branch that is the head of open PR #146 mailed %d alert(s): %q", len(sent), subject)
	}
	lines := readEventLines(t, logPath)
	if ev := findEvent(lines, "work_item_stranded_push", "cat-pa95f"); ev != nil {
		t.Fatalf("an open PR emitted work_item_stranded_push: %v", ev)
	}
	ev := findEvent(lines, "work_item_push_awaiting_review", "cat-pa95f")
	if ev == nil {
		t.Fatal("the suppression left no work_item_push_awaiting_review event")
	}
	details, _ := ev["details"].(map[string]any)
	if got, _ := details["pr"].(float64); got != 146 {
		t.Errorf("event details.pr = %v, want 146", details["pr"])
	}
	if out := logs(); !strings.Contains(out, "awaiting review") {
		t.Errorf("the suppression was silent in the log; got: %s", out)
	}
}

// TestReleaseAlertsWhenThePRProbeFails pins the direction end to end: GitHub
// unreachable is the alert as before, with the failure on the event and in the
// mail so it can be told from "there was no PR".
func TestReleaseAlertsWhenThePRProbeFails(t *testing.T) {
	logPath := useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built (mg-a95f)")
	stubPRProbe(t, func(string, string) (ghpr.Answer, error) {
		return ghpr.Answer{}, errors.New("could not resolve host: api.github.com")
	})

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})
	a := livePolecat("pa95f", "mg-a95f")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}

	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("a failed PR probe sent %d mail(s), want 1 — it must alert, never suppress", len(sent))
	}
	if _, body := sent[0].Message(); !strings.Contains(body, "could not ask GitHub") {
		t.Errorf("the mail does not say the PR check failed:\n%s", body)
	}
	ev := findEvent(readEventLines(t, logPath), "work_item_stranded_push", "cat-pa95f")
	if ev == nil {
		t.Fatal("no work_item_stranded_push event")
	}
	details, _ := ev["details"].(map[string]any)
	if got, _ := details["pr_probe_error"].(string); !strings.Contains(got, "api.github.com") {
		t.Errorf("event details.pr_probe_error = %q, want the probe's error", got)
	}
	if got, _ := details["pr_probe_branch"].(string); got != "polecat-pa95f" {
		t.Errorf("event details.pr_probe_branch = %q, want the name the failed probe asked about", got)
	}
}

// TestCrossPushedAlertIsNotLocalOnly: a branch whose head is on origin under
// another name still alerts when no PR owns it — part (a) changes the words,
// not the verdict — but it must not say the work is off origin, nor tell the
// reader to push it.
func TestCrossPushedAlertIsNotLocalOnly(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "feature-x", "f.md", "feat: the work (mg-5ab5)")
	gitRun(t, repo, "branch", "polecat-p5ab5", "feature-x")

	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})
	a := livePolecat("p5ab5", "mg-5ab5")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}

	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("sent %d mail(s), want 1", len(sent))
	}
	_, body := sent[0].Message()
	if strings.Contains(body, "THE WORK IS NOT ON ORIGIN") || strings.Contains(body, "push origin") {
		t.Errorf("a cross-pushed branch was called local-only or told to push:\n%s", body)
	}
	for _, want := range []string{"On origin:  refs/remotes/origin/feature-x", "pogo refinery submit feature-x"} {
		if !strings.Contains(body, want) {
			t.Errorf("mail is missing %q:\n%s", want, body)
		}
	}
}

// TestStartupSweepCountsAnOpenPRAsAwaitingReview: the sweep's report keeps the
// suppression countable, apart from Clean.
func TestStartupSweepCountsAnOpenPRAsAwaitingReview(t *testing.T) {
	sandboxWitness(t)
	logPath := useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-a564", "deliver.md", "feat(deliver): finished, in review (mg-a564)")
	deadWitness(t, "a564", "mg-a564", repo)
	stubPRProbe(t, func(string, string) (ghpr.Answer, error) { return ghpr.Answer{Number: 7, State: "OPEN"}, nil })

	rep := newDrainTestRegistry(t).ReportStrandedWorkAcrossRestart()
	if rep.Candidates != 1 || rep.AwaitingReview != 1 || rep.Stranded != 0 || rep.Clean != 0 {
		t.Fatalf("sweep report = %+v, want 1 candidate, 1 awaiting review", rep)
	}
	if sent := mail(); len(sent) != 0 {
		t.Fatalf("the sweep mailed %d alert(s) for an open PR", len(sent))
	}
	if ev := findEvent(readEventLines(t, logPath), "work_item_push_awaiting_review", "cat-a564"); ev == nil {
		t.Fatal("the sweep's suppression left no work_item_push_awaiting_review event")
	}
}

// TestStrandedAlertRecordsWhatThePRProbeAnswered covers both emitters
// (drellem2/pogo#259): when GitHub answers "no PR", the event and the mail say
// which name was asked, of which repository, and what came back — so an alert
// sent beside an open PR can be traced to the question that missed it.
func TestStrandedAlertRecordsWhatThePRProbeAnswered(t *testing.T) {
	answer := func(string, string) (ghpr.Answer, error) {
		return ghpr.Answer{Repo: "drellem2/pogo"}, nil
	}
	check := func(t *testing.T, logPath, agent string, sent []string) {
		t.Helper()
		ev := findEvent(readEventLines(t, logPath), "work_item_stranded_push", agent)
		if ev == nil {
			t.Fatal("no work_item_stranded_push event")
		}
		details, _ := ev["details"].(map[string]any)
		if got, _ := details["pr_probe_branch"].(string); got != "polecat-pa95f" {
			t.Errorf("details.pr_probe_branch = %v, want polecat-pa95f", details["pr_probe_branch"])
		}
		if got, _ := details["pr_probe_result"].(string); got != "none repo:drellem2/pogo" {
			t.Errorf("details.pr_probe_result = %v, want \"none repo:drellem2/pogo\"", details["pr_probe_result"])
		}
		if _, ok := details["pr_probe_error"]; ok {
			t.Errorf("a probe that answered carries pr_probe_error: %v", details["pr_probe_error"])
		}
		if len(sent) != 1 {
			t.Fatalf("sent %d mail(s), want 1", len(sent))
		}
		if want := "PR check:   asked GitHub about polecat-pa95f: none repo:drellem2/pogo"; !strings.Contains(sent[0], want) {
			t.Errorf("mail is missing %q:\n%s", want, sent[0])
		}
	}

	t.Run("release", func(t *testing.T) {
		logPath := useTempEventLog(t)
		mail := captureStrandedMail(t)
		repo := strandedRepo(t)
		pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built (mg-a95f)")
		stubPRProbe(t, answer)
		reg := newDrainTestRegistry(t)
		reg.SetClaimReleaser(&stubReleaser{released: true})
		a := livePolecat("pa95f", "mg-a95f")
		a.SourceRepo = repo
		if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
			t.Fatalf("releasePolecatClaim: %v", err)
		}
		var bodies []string
		for _, m := range mail() {
			_, body := m.Message()
			bodies = append(bodies, body)
		}
		check(t, logPath, "cat-pa95f", bodies)
	})

	t.Run("restart sweep", func(t *testing.T) {
		sandboxWitness(t)
		logPath := useTempEventLog(t)
		mail := captureStrandedMail(t)
		repo := strandedRepo(t)
		pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built (mg-a95f)")
		deadWitness(t, "pa95f", "mg-a95f", repo)
		stubPRProbe(t, answer)
		rep := newDrainTestRegistry(t).ReportStrandedWorkAcrossRestart()
		if rep.Stranded != 1 {
			t.Fatalf("sweep report = %+v, want 1 stranded", rep)
		}
		var bodies []string
		for _, m := range mail() {
			_, body := m.Message()
			bodies = append(bodies, body)
		}
		check(t, logPath, "cat-pa95f", bodies)
	})
}

// --- The dispatch gate (drellem2/pogo#163, mg-64751) -------------------------

// TestDispatchAtAnItemWhoseBranchIsAnOpenPRIsNotRefused: the same open-PR rule
// #147 gave the release notice now reaches the spawn gate. A branch that is the
// head of an open PR is awaiting review, so a dispatch attributable to its item
// is allowed — and the suppression is emitted, never silent.
func TestDispatchAtAnItemWhoseBranchIsAnOpenPRIsNotRefused(t *testing.T) {
	logPath := useTempEventLog(t)
	logs := captureLog(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built, PR open (mg-a95f)")
	var asked []string
	stubPRProbe(t, func(_, branch string) (ghpr.Answer, error) {
		asked = append(asked, branch)
		if branch == "polecat-pa95f" {
			return ghpr.Answer{Number: 146, State: "OPEN"}, nil
		}
		return ghpr.Answer{}, nil
	})

	reg := newDrainTestRegistry(t)
	if refusal := reg.strandedWorkRefusal("mg-a95f", repo, "main"); refusal != "" {
		t.Fatalf("a branch that is the head of open PR #146 refused the dispatch: %s", refusal)
	}
	if len(asked) != 1 || asked[0] != "polecat-pa95f" {
		t.Errorf("the probe was asked about %v, want exactly [polecat-pa95f]", asked)
	}
	ev := findEvent(readEventLines(t, logPath), "work_item_push_awaiting_review", "pogod")
	if ev == nil {
		t.Fatal("the dispatch-time suppression left no work_item_push_awaiting_review event")
	}
	details, _ := ev["details"].(map[string]any)
	if got, _ := details["pr"].(float64); got != 146 {
		t.Errorf("event details.pr = %v, want 146", details["pr"])
	}
	if got, _ := details["route"].(string); got != RouteDispatch {
		t.Errorf("event details.route = %q, want %q", got, RouteDispatch)
	}
	if got, _ := ev["work_item_id"].(string); got != "mg-a95f" {
		t.Errorf("event work_item_id = %q, want mg-a95f", got)
	}
	if out := logs(); !strings.Contains(out, "awaiting review") {
		t.Errorf("the suppression was silent in the log; got: %s", out)
	}
}

// TestDispatchStillRefusedWhenThePRProbeFails pins the direction at the gate:
// GitHub unreachable is a refusal, as it was before the rule existed, and the
// refusal says it stands because the check could not run. Daniel's GO for #163:
// "A probe failure must still refuse, never pass."
func TestDispatchStillRefusedWhenThePRProbeFails(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built (mg-a95f)")
	stubPRProbe(t, func(string, string) (ghpr.Answer, error) {
		return ghpr.Answer{}, errors.New("could not resolve host: api.github.com")
	})

	reg := newDrainTestRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "pa95f2", Id: "mg-a95f", Repo: repo, Branch: "main", Template: BuildWorkerTemplate,
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("a failed PR probe: status = %d, want 409 — it must refuse, never pass", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"polecat-pa95f", "PUSHED, UNMERGED", "could not ask GitHub", "api.github.com"} {
		if !strings.Contains(body, want) {
			t.Errorf("refusal does not mention %q; got: %s", want, body)
		}
	}
}

// TestDispatchRefusalCarriesNoProbeNoteWhenGitHubAnswered: "no PR" is an
// answer, and the refusal must not claim the check failed.
func TestDispatchRefusalCarriesNoProbeNoteWhenGitHubAnswered(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built (mg-a95f)")
	stubPRProbe(t, func(string, string) (ghpr.Answer, error) { return ghpr.Answer{Repo: "drellem2/pogo"}, nil })

	refusal := newDrainTestRegistry(t).strandedWorkRefusal("mg-a95f", repo, "main")
	if refusal == "" {
		t.Fatal("a pushed, unmerged branch with no PR dispatched")
	}
	if strings.Contains(refusal, "could not ask GitHub") {
		t.Errorf("a probe that answered \"none\" is worded as a failure: %s", refusal)
	}
}

// TestDispatchStillRefusedForTheBranchWithoutAPR: one of the item's branches
// being in review does not pass the dispatch while ANOTHER is stranded; the
// refusal names the stranded one.
func TestDispatchStillRefusedForTheBranchWithoutAPR(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built, PR open (mg-a95f)")
	pushBranch(t, repo, "polecat-xa95f", "g.md", "feat: a sibling's attempt (mg-a95f)")
	stubPRProbe(t, func(_, branch string) (ghpr.Answer, error) {
		if branch == "polecat-pa95f" {
			return ghpr.Answer{Number: 146, State: "OPEN"}, nil
		}
		return ghpr.Answer{}, nil
	})

	findings, refusal, inReview := newDrainTestRegistry(t).strandedWorkCheck("mg-a95f", repo, "main")
	if refusal == "" {
		t.Fatal("an item with one branch in review and one stranded dispatched")
	}
	if len(findings) != 1 || findings[0].Branch != "polecat-xa95f" {
		t.Fatalf("findings = %+v, want only polecat-xa95f (the open PR's branch is not stranded)", findings)
	}
	if !strings.Contains(refusal, "polecat-xa95f") {
		t.Errorf("refusal does not name the stranded branch: %s", refusal)
	}
	if len(inReview) != 1 || inReview[0].Branch != "polecat-pa95f" || inReview[0].PR != 146 {
		t.Errorf("inReview = %+v, want polecat-pa95f as the head of PR #146", inReview)
	}
}

// TestStrandedAdoptContinuesABranchThatIsAnOpenPR is mg-64751 review round 1's
// blocking finding pinned: an item whose only unmerged branch is the head of an
// open PR refuses nothing, and an explicit --stranded-adopt must STILL base the
// worktree on that branch — the likeliest adopt of all is continuing a PR whose
// builder was stopped mid-review. Before the fix the flag was never consulted
// and the worker started from the target under a flag that said it adopted.
func TestStrandedAdoptContinuesABranchThatIsAnOpenPR(t *testing.T) {
	logPath := useTempEventLog(t)
	repo := strandedRepo(t)
	sha := pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished, PR open (mg-9a19)")
	stubPRProbe(t, func(_, branch string) (ghpr.Answer, error) {
		if branch == "polecat-9a19" {
			return ghpr.Answer{Number: 146, State: "OPEN"}, nil
		}
		return ghpr.Answer{}, nil
	})

	reg := adoptRegistry(t)
	rr := spawnPolecat(t, reg, SpawnPolecatAPIRequest{
		Name: "q9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt: "the builder was stopped mid-review; continue PR #146",
	})
	if rr.Code != http.StatusCreated {
		t.Fatalf("--stranded-adopt onto an open PR's branch: status = %d, want 201: %s", rr.Code, rr.Body.String())
	}
	if !carriesCommit(t, repo, "polecat-q9a19", sha) {
		t.Fatalf("the adopting worker's branch does NOT carry %s: --stranded-adopt was dropped and the "+
			"worktree was based on the target", sha[:12])
	}
	ev := findEvent(readEventLines(t, logPath), "dispatch_stranded_work_adopted", "cat-q9a19")
	if ev == nil {
		t.Fatal("the adoption left no dispatch_stranded_work_adopted event")
	}
	details, _ := ev["details"].(map[string]any)
	if got, _ := details["adopted_branch"].(string); got != "polecat-9a19" {
		t.Errorf("adopted_branch = %q, want polecat-9a19", got)
	}
	if got, _ := details["refusal"].(string); !strings.Contains(got, "open PR #146") {
		t.Errorf("the record does not say the adopted branch was an open PR: %q", got)
	}
}

// TestStrandedAdoptAndOverrideOnAnOpenPRAreRefused: the two exits stay
// mutually exclusive when it is the in-review branch they are about.
func TestStrandedAdoptAndOverrideOnAnOpenPRAreRefused(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-9a19", "audit.md", "feat(audit): finished, PR open (mg-9a19)")
	stubPRProbe(t, func(string, string) (ghpr.Answer, error) { return ghpr.Answer{Number: 146, State: "OPEN"}, nil })

	rr := spawnPolecat(t, adoptRegistry(t), SpawnPolecatAPIRequest{
		Name: "v9a19", Id: "mg-9a19", Repo: repo, Branch: "main", Template: adoptTemplate(t),
		StrandedAdopt: "continue it", StrandedOverride: "start over",
	})
	if rr.Code != http.StatusConflict {
		t.Fatalf("adopt + override onto an open PR's branch: status = %d, want 409: %s", rr.Code, rr.Body.String())
	}
	if body := rr.Body.String(); !strings.Contains(body, "OPPOSITE") {
		t.Errorf("refusal does not explain the conflict: %s", body)
	}
}

// TestDispatchStillRefusedForAPreRegistrationBranchWithAnOpenPR: an open PR
// does not stop a re-dispatch from writing predictions after the results, so
// the pre-registration refusal is untouched (strandedwork.Finding.CheckOpenPR
// skips it).
func TestDispatchStillRefusedForAPreRegistrationBranchWithAnOpenPR(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-f3ff", "predictions.md", "predictions: three of the six checks will fail")
	stubPRProbe(t, func(string, string) (ghpr.Answer, error) { return ghpr.Answer{Number: 9, State: "OPEN"}, nil })

	refusal := newDrainTestRegistry(t).strandedWorkRefusal("mg-f3ff", repo, "main")
	if !strings.Contains(refusal, "PRE-REGISTRATION") {
		t.Fatalf("an open PR passed a pre-registration branch's dispatch; refusal = %q", refusal)
	}
}

// TestDispatchRefusalNamesEveryFailedPRProbe: the refusal leads with one
// branch, but a probe that failed on ANOTHER kept branch is still said — that
// branch could be the open PR the gate could not see (mg-64751 review round 1).
func TestDispatchRefusalNamesEveryFailedPRProbe(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pa95f", "f.md", "feat: built (mg-a95f)")
	pushBranch(t, repo, "polecat-xa95f", "g.md", "feat: a sibling's attempt (mg-a95f)")
	stubPRProbe(t, func(_, branch string) (ghpr.Answer, error) {
		if branch == "polecat-xa95f" {
			return ghpr.Answer{}, errors.New("could not resolve host: api.github.com")
		}
		return ghpr.Answer{Repo: "drellem2/pogo"}, nil
	})

	findings, refusal, _ := newDrainTestRegistry(t).strandedWorkCheck("mg-a95f", repo, "main")
	if len(findings) != 2 {
		t.Fatalf("findings = %+v, want both branches", findings)
	}
	if !strings.Contains(refusal, "could not ask GitHub about polecat-xa95f (could not resolve host") {
		t.Errorf("the refusal does not name the failed probe on the second branch: %s", refusal)
	}
	if strings.Contains(refusal, "about polecat-pa95f") {
		t.Errorf("a probe that answered is worded as failed: %s", refusal)
	}
}
