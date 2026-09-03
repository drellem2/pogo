package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/events"
)

// detachOrphanWorktree puts wtRepo's worktree into mg-8d25's loss state: it
// COMMITS inside the tree, detaches HEAD, and deletes the branch, so the commit
// is held by that worktree's HEAD and by no ref anywhere.
//
// It also publishes the base as origin/main, because the durability question is
// "does any origin ref hold this" — without a published base every commit in the
// fixture is at risk and the test would pass for the wrong reason.
func detachOrphanWorktree(t *testing.T, repo, wt string) string {
	t.Helper()
	git := func(dir string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	base := git(repo, "rev-parse", "main")
	git(repo, "update-ref", "refs/remotes/origin/main", base)

	if err := os.WriteFile(filepath.Join(wt, "rescue.go"),
		[]byte("package rescue // the only copy on the machine\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(wt, "add", "rescue.go")
	git(wt, "commit", "-qm", "rescue: work that exists nowhere else")
	head := git(wt, "rev-parse", "HEAD")
	git(wt, "checkout", "-q", "--detach")
	git(repo, "branch", "-D", "polecat-cat1")

	// Premise, asserted rather than assumed: `git status` reads this tree and
	// finds it CLEAN. If that ever stops being true this test has silently
	// become a re-run of the mg-ee02 dirty case.
	if out := git(wt, "status", "--porcelain"); out != "" {
		t.Fatalf("the fixture must be CLEAN, `git status --porcelain` said:\n%s", out)
	}
	return head
}

// TestCleanupAgentWorktreeKeepsDetachedTreeHoldingTheOnlyCopy is mg-8d25 at the
// layer that actually reaps such a tree.
//
// The ticket is written about "gc's removal guard", and the guard is where the
// fix went — but `pogo gc`'s SWEEP never reaches a detached worktree at all
// (gitgc.TestSweepNeverReachesADetachedWorktree measures why: phase 1 skips any
// worktree whose branch lacks the polecat- prefix, and a detached tree reports no
// branch). This exit hook is the caller that does reach it: it fires on every
// no-restart agent exit and passes the tree by path, whatever HEAD says.
//
// On the pre-fix code this test FAILS — the tree was clean, so the guard cleared
// it, `git worktree remove` deleted the per-worktree HEAD, and nothing was left
// holding the commit.
func TestCleanupAgentWorktreeKeepsDetachedTreeHoldingTheOnlyCopy(t *testing.T) {
	repo, wt := wtRepo(t)
	head := detachOrphanWorktree(t, repo, wt)

	var gotSubject, gotBody string
	mail := func(to, from, subject, body string) error {
		gotSubject, gotBody = subject, body
		return nil
	}

	outcome := cleanupAgentWorktree(catAgent("cat1", "mg-8d25", repo, wt), "mayor", mail)
	if outcome != worktreeUnpushed {
		t.Fatalf("outcome = %v, want worktreeUnpushed", outcome)
	}
	if _, err := os.Stat(wt); err != nil {
		t.Fatalf("THE COMMITS WERE DESTROYED — the tree must survive: %v", err)
	}
	if err := exec.Command("git", "-C", repo, "cat-file", "-e", head+"^{commit}").Run(); err != nil {
		t.Fatalf("the commit should still be in the object store: %v", err)
	}

	// The notice must make the THREE-way distinction the outcomes make. Saying
	// "uncommitted work" here sends a reader to `git status`, hands them a clean
	// tree, and ends the investigation on the reading that the alert was
	// spurious — a plausible innocent cause, which gh #97 records as worse than
	// silence because a missing line prompts investigation and a plausible one
	// ends it.
	if strings.Contains(gotSubject, "uncommitted") || strings.Contains(gotSubject, "could not check") {
		t.Errorf("subject must not claim uncommitted work or a failed check, got %q", gotSubject)
	}
	if !strings.Contains(gotSubject, "exist nowhere else") {
		t.Errorf("subject must say what is actually at stake, got %q", gotSubject)
	}
	if !strings.Contains(gotBody, "CLEAN") {
		t.Errorf("body must say the tree is clean so nobody hunts files in it, got:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "detached HEAD") {
		t.Errorf("body must name the detached HEAD — that is what makes these commits "+
			"unreachable rather than merely unpushed. Got:\n%s", gotBody)
	}
	// The remedy has to be the one that works. `pogo gc --apply --force` does
	// NOT reclaim a detached tree (the sweep never sees it), so recommending it
	// would leave the operator believing they cleared a tree that is still there.
	if !strings.Contains(gotBody, "worktree remove --force") {
		t.Errorf("body must give the reclaim command that actually works on a detached tree, "+
			"got:\n%s", gotBody)
	}
	if !strings.Contains(gotBody, "NOT `pogo gc --apply --force`") {
		t.Errorf("body must say why the usual reclaim command does nothing here, got:\n%s", gotBody)
	}
	// And the do-not-dispatch sentence, in the subject, for the same reason
	// mg-32e3 put it there: the subject is the part that gets skimmed.
	if !strings.Contains(gotSubject, "do NOT dispatch at mg-8d25") {
		t.Errorf("subject must carry the prohibition, got %q", gotSubject)
	}
	if !strings.Contains(gotBody, "DO NOT DISPATCH A WORKER AT mg-8d25") {
		t.Errorf("body must carry the prohibition, got:\n%s", gotBody)
	}
	if strings.Contains(gotBody, "work that was never committed") {
		t.Errorf("the dispatch warning must not claim the work was never committed — it was; "+
			"that is the whole difference between this outcome and \"preserved\". Got:\n%s", gotBody)
	}
}

// TestCleanupAgentWorktreeRecordsTheUnpushedRetention covers the RECORD half.
//
// A retention whose mail is the only trace is a retention that disappears when
// the mail is lost, which is the defect mg-32e3 fixed for the other two outcomes.
// This one has to land on the spine under its own outcome word for the same
// reason: `pogo events --type worktree_preserved` is where the population is
// counted, and an outcome missing from it is a tree nobody can query for.
func TestCleanupAgentWorktreeRecordsTheUnpushedRetention(t *testing.T) {
	spine := filepath.Join(t.TempDir(), "events.log")
	events.SetLogPathForTesting(spine)
	t.Cleanup(func() { events.SetLogPathForTesting(testEventLogPath) })

	repo, wt := wtRepo(t)
	detachOrphanWorktree(t, repo, wt)

	mail := func(to, from, subject, body string) error { return nil }
	if got := cleanupAgentWorktree(catAgent("cat1", "mg-8d25", repo, wt), "mayor", mail); got != worktreeUnpushed {
		t.Fatalf("outcome = %v, want worktreeUnpushed", got)
	}

	found, err := events.ReadFiltered(spine, events.Filter{Type: "worktree_preserved"})
	if err != nil {
		t.Fatalf("reading the spine: %v", err)
	}
	var ev *events.Event
	for i := range found {
		if found[i].Details["outcome"] == "unpushed" {
			ev = &found[i]
		}
	}
	if ev == nil {
		t.Fatalf("a retained tree left nothing on the spine under outcome \"unpushed\"; got %+v", found)
	}
	if ev.WorkItemID != "mg-8d25" {
		t.Errorf("WorkItemID = %q, want mg-8d25 — a lost notice makes this event the only surviving "+
			"trace, so it must answer which item is now unsafe to dispatch at", ev.WorkItemID)
	}
	// The dirty counts must be ABSENT, not zero: this tree was never dirty, and
	// a `dirty_paths: 0` on it is a measurement nobody took.
	if _, ok := ev.Details["dirty_paths"]; ok {
		t.Errorf("dirty_paths must not appear for a clean tree, got %+v", ev.Details)
	}
	if detail, _ := ev.Details["detail"].(string); !strings.Contains(detail, "DETACHED") {
		t.Errorf("detail must carry the refusal, got %q", detail)
	}
}
