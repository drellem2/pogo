package agent

import (
	"errors"
	"strings"
	"testing"

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
	stubPRProbe(t, func(_, branch string) (int, error) {
		if branch == "polecat-pa95f" {
			return 146, nil
		}
		return 0, nil
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
	stubPRProbe(t, func(string, string) (int, error) {
		return 0, errors.New("could not resolve host: api.github.com")
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
	stubPRProbe(t, func(string, string) (int, error) { return 7, nil })

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
