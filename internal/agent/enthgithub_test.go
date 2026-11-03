package agent

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/gitgc"
	"github.com/drellem2/pogo/internal/vcs"
)

// enth-vcs slice 9 (mg-2e9fc): the GitHub boundary for an enth repo. Every
// test here has a git control beside it: the same request on a git repo is
// exactly what it was.

var sliceNineEnth = vcs.EnthRepo{Remote: "https://enth.example", Repo: "o/r"}

func TestEnthPRFlowRefusal(t *testing.T) {
	for _, tc := range []struct {
		name, tmpl, branch, want string
		trunk                    string
	}{
		{name: "build-pr template", tmpl: "polecat-build-pr", want: "has no PR flow"},
		{name: "build-pr template by path", tmpl: "/x/templates/polecat-build-pr.md", want: "has no PR flow"},
		{name: "integration branch", tmpl: "polecat", branch: "integ", want: "targets refs/heads/integ, which is not this enth repo's trunk refs/heads/main"},
		{name: "integration ref", tmpl: "polecat", branch: "refs/heads/release-1", want: "not this enth repo's trunk"},
		{name: "main against a custom trunk", tmpl: "polecat", branch: "main", trunk: "refs/heads/trunk", want: "trunk refs/heads/trunk"},
		{name: "ordinary build", tmpl: "polecat"},
		{name: "architect", tmpl: "polecat-architect"},
		{name: "branch is the trunk by name", tmpl: "polecat", branch: "main"},
		{name: "branch is the trunk by ref", tmpl: "polecat", branch: "refs/heads/main"},
		{name: "custom trunk by name", tmpl: "polecat", branch: "trunk", trunk: "refs/heads/trunk"},
	} {
		e := sliceNineEnth
		e.Trunk = tc.trunk
		got := enthPRFlowRefusal(e, tc.tmpl, tc.branch)
		if tc.want == "" {
			if got != "" {
				t.Errorf("%s: refused: %s", tc.name, got)
			}
			continue
		}
		if !strings.Contains(got, tc.want) || !strings.Contains(got, "Nothing was dispatched") {
			t.Errorf("%s: refusal = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// useEnthRepo makes the project record say repo is enth for one test.
func useEnthRepo(t *testing.T, repo string) {
	t.Helper()
	SetRepoVCS(func(p string) (vcs.Kind, *vcs.EnthRepo) {
		if p == repo {
			e := sliceNineEnth
			return vcs.Enth, &e
		}
		return vcs.Git, nil
	})
	t.Cleanup(func() { SetRepoVCS(nil) })
}

// TestSpawnRefusesPRFlowOnAnEnthRepo drives the real spawn path: both PR-flow
// shapes are a 409 on an enth repo with nothing created, and the identical
// requests on the same repo as git are dispatched (the control).
func TestSpawnRefusesPRFlowOnAnEnthRepo(t *testing.T) {
	repo := strandedRepo(t)
	pushBranch(t, repo, "integ", "integ.md", "feat: integration-branch work")
	writeTemplate(t, "polecat-build-pr", "+++\nworktree = true\n+++\n# PR-flow build for {{.Id}}\n")
	polecats, _ := gitgc.DefaultPolecatsDir()

	cases := []struct {
		name string
		req  SpawnPolecatAPIRequest
		want string
	}{
		{"build-pr", SpawnPolecatAPIRequest{Name: "e9a01", Id: "mg-9a01", Repo: repo, Template: "polecat-build-pr"}, "has no PR flow"},
		{"integration branch", SpawnPolecatAPIRequest{Name: "e9a02", Id: "mg-9a02", Repo: repo, Template: adoptTemplate(t), Branch: "integ"}, "not this enth repo's trunk"},
	}

	t.Run("enth refuses", func(t *testing.T) {
		useEnthRepo(t, repo)
		reg := adoptRegistry(t)
		for _, tc := range cases {
			rr := spawnPolecat(t, reg, tc.req)
			if rr.Code != http.StatusConflict || !strings.Contains(rr.Body.String(), tc.want) {
				t.Errorf("%s: status %d, body %s; want 409 %q", tc.name, rr.Code, rr.Body.String(), tc.want)
			}
			if reg.Get(tc.req.Name) != nil {
				t.Errorf("%s: a refused spawn left an agent behind", tc.name)
			}
			if _, err := os.Stat(filepath.Join(polecats, tc.req.Name)); !os.IsNotExist(err) {
				t.Errorf("%s: a refused spawn left a workspace behind (%v)", tc.name, err)
			}
		}
	})

	t.Run("git control", func(t *testing.T) {
		reg := adoptRegistry(t)
		for _, tc := range cases {
			rr := spawnPolecat(t, reg, tc.req)
			if rr.Code != http.StatusCreated {
				t.Errorf("%s: the same spawn on a git repo = %d, want 201: %s", tc.name, rr.Code, rr.Body.String())
			}
		}
	})
}

// TestEnthReleaseSkipsTheOpenPRProbe: on an enth repo GitHub is never asked
// (its answer here would suppress), and an open refinery MR for the item is
// what suppresses instead. The git control asks GitHub, as before.
func TestEnthReleaseSkipsTheOpenPRProbe(t *testing.T) {
	setup := func(t *testing.T, enth bool, mrs []QueuedMerge) (func() []string, func() int, *[]string) {
		logPath := useTempEventLog(t)
		mail := captureStrandedMail(t)
		repo := strandedRepo(t)
		pushBranch(t, repo, "polecat-pe9b1", "f.md", "feat: built (mg-e9b1)")
		var asked []string
		stubPRProbe(t, func(_, branch string) (int, error) { asked = append(asked, branch); return 146, nil })
		if enth {
			useEnthRepo(t, repo)
		}
		reg := newDrainTestRegistry(t)
		reg.SetClaimReleaser(&stubReleaser{released: true})
		reg.SetMergeQueue(MergeQueueReaderFunc(func(r string) ([]QueuedMerge, bool) {
			if r != repo {
				return nil, true
			}
			return mrs, true
		}))
		a := livePolecat("pe9b1", "mg-e9b1")
		a.SourceRepo = repo
		if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
			t.Fatalf("releasePolecatClaim: %v", err)
		}
		events := func() []string {
			var out []string
			for _, ev := range readEventLines(t, logPath) {
				if et, _ := ev["event_type"].(string); strings.HasPrefix(et, "work_item_") {
					out = append(out, et)
				}
			}
			return out
		}
		return events, func() int { return len(mail()) }, &asked
	}

	t.Run("enth, MR open: suppressed by the refinery, GitHub not asked", func(t *testing.T) {
		events, mails, asked := setup(t, true, []QueuedMerge{{Author: "mg-e9b1"}})
		if len(*asked) != 0 {
			t.Errorf("the open-PR probe was asked %v on an enth repo", *asked)
		}
		if n := mails(); n != 0 {
			t.Errorf("an item with an open MR mailed %d alert(s)", n)
		}
		if ev := strings.Join(events(), ","); !strings.Contains(ev, "work_item_push_awaiting_review") || strings.Contains(ev, "work_item_stranded_push") {
			t.Errorf("events = %s, want awaiting_review and no stranded_push", ev)
		}
	})
	t.Run("enth, no MR: alerted, GitHub not asked", func(t *testing.T) {
		events, mails, asked := setup(t, true, []QueuedMerge{{Author: "mg-other"}})
		if len(*asked) != 0 {
			t.Errorf("the open-PR probe was asked %v on an enth repo", *asked)
		}
		if n := mails(); n != 1 {
			t.Errorf("no MR, and GitHub's open PR must not count: %d mail(s), want 1", n)
		}
		if ev := strings.Join(events(), ","); !strings.Contains(ev, "work_item_stranded_push") {
			t.Errorf("events = %s, want stranded_push", ev)
		}
	})
	t.Run("git control: GitHub asked and its PR suppresses, the queue is not consulted", func(t *testing.T) {
		events, mails, asked := setup(t, false, nil)
		if strings.Join(*asked, ",") != "polecat-pe9b1" {
			t.Errorf("git repo: open-PR probe asked %v, want polecat-pe9b1", *asked)
		}
		if n := mails(); n != 0 {
			t.Errorf("git open PR mailed %d alert(s)", n)
		}
		if ev := strings.Join(events(), ","); !strings.Contains(ev, "work_item_push_awaiting_review") {
			t.Errorf("events = %s, want awaiting_review", ev)
		}
	})
}

func TestRefineryOpenMRProbe(t *testing.T) {
	reg := newDrainTestRegistry(t)
	if _, err := reg.refineryOpenMRProbe("/r", "p1")("mg-1"); err == nil {
		t.Error("no queue wired: answered instead of failing (a probe that can only suppress must not guess)")
	}
	reg.SetMergeQueue(MergeQueueReaderFunc(func(string) ([]QueuedMerge, bool) { return nil, false }))
	if _, err := reg.refineryOpenMRProbe("/r", "p1")("mg-1"); err == nil {
		t.Error("unreadable queue: answered instead of failing")
	}
	reg.SetMergeQueue(MergeQueueReaderFunc(func(string) ([]QueuedMerge, bool) {
		return []QueuedMerge{{Author: "mg-2"}, {Author: "p1"}}, true
	}))
	if mr, err := reg.refineryOpenMRProbe("/r", "p1")("mg-1"); err != nil || !strings.Contains(mr, "p1") {
		t.Errorf("MR authored by the worker name = %q, %v", mr, err)
	}
	if mr, err := reg.refineryOpenMRProbe("/r", "p9")("mg-2"); err != nil || !strings.Contains(mr, "mg-2") {
		t.Errorf("MR authored by the item = %q, %v", mr, err)
	}
	if mr, err := reg.refineryOpenMRProbe("/r", "p9")("mg-3"); err != nil || mr != "" {
		t.Errorf("no MR = %q, %v", mr, err)
	}
}

// An unreadable refinery queue on an enth repo keeps the alert, and the mail
// says which check failed. The git wording is unchanged (its own test,
// TestReleaseAlertsWhenThePRProbeFails, pins "could not ask GitHub").
func TestEnthReleaseAlertsWhenTheQueueIsUnreadable(t *testing.T) {
	useTempEventLog(t)
	mail := captureStrandedMail(t)
	repo := strandedRepo(t)
	pushBranch(t, repo, "polecat-pe9b2", "f.md", "feat: built (mg-e9b2)")
	stubPRProbe(t, func(string, string) (int, error) { return 146, nil })
	useEnthRepo(t, repo)
	reg := newDrainTestRegistry(t)
	reg.SetClaimReleaser(&stubReleaser{released: true})
	reg.SetMergeQueue(MergeQueueReaderFunc(func(string) ([]QueuedMerge, bool) { return nil, false }))
	a := livePolecat("pe9b2", "mg-e9b2")
	a.SourceRepo = repo
	if _, err := reg.releasePolecatClaim(a, "agent_stopped"); err != nil {
		t.Fatalf("releasePolecatClaim: %v", err)
	}
	sent := mail()
	if len(sent) != 1 {
		t.Fatalf("an unreadable queue sent %d mail(s), want 1 — it must alert, never suppress", len(sent))
	}
	_, body := sent[0].Message()
	if !strings.Contains(body, "could not ask the refinery") || strings.Contains(body, "could not ask GitHub") {
		t.Errorf("the mail does not name the refinery check:\n%s", body)
	}
}
