package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/refinery"
	"github.com/drellem2/pogo/internal/stallwatch"
)

// emptyRefinery is a real refinery holding nothing, so a probe under it has
// genuinely CONSULTED the queue and found the branch absent from it.
//
// A real one and not a nil thunk, because those two answers are exactly what
// mg-64bb is about keeping apart: nil means nobody asked, and every test below
// that reads a clean Uncertain would then be asserting over a snapshot that says
// it could not tell. See TestStallStrandedSaysWhenTheQueueWasNotConsulted for
// the other side, and note that a submitted merge request is left UNSTARTED —
// Start is a separate call, so the request stays pending and nothing merges.
func emptyRefinery(t *testing.T) func() *refinery.Refinery {
	t.Helper()
	return refineryWith(t)
}

// refineryWith builds a refinery holding these requests, pending.
func refineryWith(t *testing.T, reqs ...refinery.MergeRequest) func() *refinery.Refinery {
	t.Helper()
	r, err := refinery.New(refinery.Config{
		Enabled:     true,
		WorktreeDir: t.TempDir(),
		// No macguffin gate and no persistence, so the test never reads or
		// writes the host's real ~/.macguffin or ~/.pogo state.
		MacguffinDir: "",
		StatePath:    "",
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, req := range reqs {
		if _, err := r.Submit(req); err != nil {
			t.Fatalf("submit %s: %v", req.Branch, err)
		}
	}
	return func() *refinery.Refinery { return r }
}

// strandRepo is a throwaway git repository with an origin, built per test.
//
// A REAL REPOSITORY AND NOT A FAKE, because the thing under test here is the
// join between an item id and a branch NAME plus the strandedwork.Inspect call
// underneath it. A fake probe would exercise the map-building and nothing that
// can actually be wrong: whether `git cherry` sees the commit, whether the
// remote-tracking ref is preferred over the local head, and whether Pushed comes
// out true are all facts about git.
type strandRepo struct {
	t      *testing.T
	dir    string
	origin string
}

func newStrandRepo(t *testing.T) *strandRepo {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	root := t.TempDir()
	r := &strandRepo{t: t, dir: filepath.Join(root, "work"), origin: filepath.Join(root, "origin.git")}
	runGit(t, root, "init", "--bare", "--initial-branch=main", r.origin)
	runGit(t, root, "init", "--initial-branch=main", r.dir)
	r.git("config", "user.email", "test@example.com")
	r.git("config", "user.name", "Test")
	r.git("config", "commit.gpgsign", "false")
	r.git("remote", "add", "origin", r.origin)
	r.commit("README.md", "chore: initial commit")
	r.git("push", "-q", "origin", "main")
	return r
}

func (r *strandRepo) git(args ...string) string {
	r.t.Helper()
	return runGit(r.t, r.dir, args...)
}

func (r *strandRepo) commit(file, subject string) {
	r.t.Helper()
	path := filepath.Join(r.dir, file)
	prev, _ := os.ReadFile(path)
	body := string(prev) + subject + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		r.t.Fatalf("write %s: %v", path, err)
	}
	r.git("add", file)
	r.git("commit", "-q", "-m", subject)
}

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// TestStallStrandedFindsAPushedUnmergedBranch is the state mg-4bf1 was filed
// about, reproduced in git: a polecat pushed its work, its claim was released,
// and the item is back in available/ while the branch sits unmerged.
//
// It is also the POSITIVE CONTROL for every negative assertion below. Without
// it a probe that answered "nothing stranded" to everything — a broken join, a
// wrong ref namespace, a git that would not run — would pass the suppression
// tests and look like a working guard.
func TestStallStrandedFindsAPushedUnmergedBranch(t *testing.T) {
	r := newStrandRepo(t)
	r.git("checkout", "-q", "-b", "polecat-ta932", "main")
	r.commit("fix.go", "fix(netcontrol): net-control's up was a completed connect(2) (mg-a932)")
	r.git("push", "-q", "origin", "polecat-ta932")
	r.git("checkout", "-q", "main")

	work, known := newStallStranded(emptyRefinery(t)).Branches([]stallwatch.StrandedItem{{ID: "mg-a932", Repo: r.dir}})
	if !known {
		t.Fatal("known=false — the question was answerable and a false unknown puts every check back to advertising the item")
	}
	got := work.Items["mg-a932"]
	if len(got) != 1 {
		t.Fatalf("branches for mg-a932 = %+v, want exactly one", got)
	}
	if got[0].Branch != "polecat-ta932" {
		t.Errorf("Branch = %q, want polecat-ta932", got[0].Branch)
	}
	if !got[0].Pushed {
		t.Errorf("Pushed = false for a branch that is on origin — the remedy printed for a "+
			"local-only branch is one `pogo refinery submit` refuses (mg-586d). Ref=%q", got[0].Ref)
	}
	if got[0].Unmerged != 1 {
		t.Errorf("Unmerged = %d, want 1", got[0].Unmerged)
	}
	if got[0].Repo != r.dir {
		t.Errorf("Repo = %q, want %q — the remedy is not paste-ready without it", got[0].Repo, r.dir)
	}
	if work.Uncertain != "" {
		t.Errorf("Uncertain = %q on a clean read", work.Uncertain)
	}
}

// TestStallStrandedIgnoresAMergedBranch. Every branch this fleet lands is
// rebased, so its commits arrive on the target under NEW shas; a probe that read
// sha equality would report every healthy merge as stranded and refuse every
// item in the repo, which is a guard disarmed within the day.
func TestStallStrandedIgnoresAMergedBranch(t *testing.T) {
	r := newStrandRepo(t)
	r.git("checkout", "-q", "-b", "polecat-ta932", "main")
	r.commit("fix.go", "fix(netcontrol): the up probe (mg-a932)")
	r.git("push", "-q", "origin", "polecat-ta932")
	// Land it the way the refinery does: an unrelated commit first, then rebase
	// and fast-forward, so the sha is rewritten.
	r.git("checkout", "-q", "main")
	r.commit("other.md", "chore: something else landed first")
	r.git("push", "-q", "origin", "main")
	r.git("checkout", "-q", "polecat-ta932")
	r.git("rebase", "-q", "main")
	r.git("checkout", "-q", "main")
	r.git("merge", "-q", "--ff-only", "polecat-ta932")
	r.git("push", "-q", "origin", "main")

	work, known := newStallStranded(emptyRefinery(t)).Branches([]stallwatch.StrandedItem{{ID: "mg-a932", Repo: r.dir}})
	if !known {
		t.Fatal("known=false")
	}
	if got := work.Items["mg-a932"]; len(got) != 0 {
		t.Errorf("a branch whose work is already on the target was reported as stranded: %+v", got)
	}
}

// TestStallStrandedIgnoresABranchForAnotherItem pins the join. The suffix match
// is CONTAINMENT (strandedwork.BranchMatchesItem), which is deliberately loose;
// what it must not do is answer for an item whose id appears nowhere in the
// branch name.
func TestStallStrandedIgnoresABranchForAnotherItem(t *testing.T) {
	r := newStrandRepo(t)
	r.git("checkout", "-q", "-b", "polecat-pd788", "main")
	r.commit("fix.go", "fix(refusalwatch): the probe's own gate flake (mg-d788)")
	r.git("push", "-q", "origin", "polecat-pd788")
	r.git("checkout", "-q", "main")

	work, known := newStallStranded(emptyRefinery(t)).Branches([]stallwatch.StrandedItem{{ID: "mg-a932", Repo: r.dir}})
	if !known {
		t.Fatal("known=false")
	}
	if got := work.Items["mg-a932"]; len(got) != 0 {
		t.Errorf("another item's branch answered for mg-a932: %+v", got)
	}
}

// TestStallStrandedReportsAnUnlistableRepoAsUncertainty. A repository that could
// not be listed is a GAP, not a clean verdict, and the gap has to reach the
// dispatch notices — mg-8baa's lesson, which this file is close enough to
// re-learn. known stays true so the repos that DID answer are not discarded
// along with the one that did not.
func TestStallStrandedReportsAnUnlistableRepoAsUncertainty(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	missing := filepath.Join(t.TempDir(), "not-a-repo")

	work, known := newStallStranded(emptyRefinery(t)).Branches([]stallwatch.StrandedItem{{ID: "mg-a932", Repo: missing}})
	if !known {
		t.Fatal("known=false — one unlistable repo must not discard the whole snapshot")
	}
	if work.Uncertain == "" {
		t.Fatal("a repository that could not be listed produced a silent clean answer")
	}
	if !strings.Contains(work.Uncertain, missing) {
		t.Errorf("Uncertain = %q, want it to name the repository a reader can chase", work.Uncertain)
	}
}

// TestStallStrandedSaysSoWhenAnItemNamesNoRepo. A branch lives in a repository
// nothing but the item names, so an item with no `repo:` cannot be answered for
// at all — and "not looked for" must not render as "nothing there".
func TestStallStrandedSaysSoWhenAnItemNamesNoRepo(t *testing.T) {
	work, known := newStallStranded(emptyRefinery(t)).Branches([]stallwatch.StrandedItem{{ID: "mg-a932"}})
	if !known {
		t.Fatal("known=false")
	}
	if !strings.Contains(work.Uncertain, "name no repo") {
		t.Errorf("Uncertain = %q, want it to state that no branch was looked for", work.Uncertain)
	}
}

// TestStallStrandedIsQuietOnAnEmptyPopulation, so a fleet with nothing available
// pays nothing and reports nothing.
func TestStallStrandedIsQuietOnAnEmptyPopulation(t *testing.T) {
	work, known := newStallStranded(emptyRefinery(t)).Branches(nil)
	if !known {
		t.Fatal("known=false on an empty population")
	}
	if len(work.Items) != 0 || work.Uncertain != "" {
		t.Errorf("work = %+v, want empty", work)
	}
}

// TestStallStrandedNamesTheMergeRequestForAQueuedBranch is mg-64bb's headline
// state, reproduced end to end: the branch is pushed, unmerged, and its merge is
// ALREADY RUNNING. Before this, that branch reached the notice indistinguishable
// from one nobody had submitted, and the notice printed `pogo refinery submit`
// at it — measured four times over ~36 minutes on mg-a19a while
// mr-dacudtqtjv1hjkm21420 was queued throughout.
//
// The item is still REPORTED. The queue changes the remedy, not the exclusion.
func TestStallStrandedNamesTheMergeRequestForAQueuedBranch(t *testing.T) {
	r := newStrandRepo(t)
	r.git("checkout", "-q", "-b", "polecat-pa19a", "main")
	r.commit("fix.go", "fix(stallwatch): work that already exists reads available (mg-a19a)")
	r.git("push", "-q", "origin", "polecat-pa19a")
	r.git("checkout", "-q", "main")

	queue := refineryWith(t, refinery.MergeRequest{
		RepoPath:  r.dir,
		Branch:    "polecat-pa19a",
		TargetRef: "main",
		Author:    "mg-a19a",
	})

	work, known := newStallStranded(queue).Branches([]stallwatch.StrandedItem{{ID: "mg-a19a", Repo: r.dir}})
	if !known {
		t.Fatal("known=false")
	}
	got := work.Items["mg-a19a"]
	if len(got) != 1 {
		t.Fatalf("branches for mg-a19a = %+v, want exactly one — a queued branch is still reported, "+
			"because suppressing it drops the do-not-dispatch instruction with it (mg-4bf1)", got)
	}
	if got[0].Queued == nil {
		t.Fatal("a branch whose merge is ALREADY IN THE QUEUE came back indistinguishable from one " +
			"nobody has submitted — which is the state that gets a paste-ready duplicate submit printed at it")
	}
	if got[0].Queued.MR == "" {
		t.Error("Queued.MR is empty — a reader told 'this is in flight' and not told which request " +
			"has to go find it is the arbitration this field exists to end")
	}
	if got[0].Queued.Status == "" {
		t.Error("Queued.Status is empty — 'queued behind others' and 'a gate is running on it now' " +
			"are different answers to how long this item has to be left alone")
	}
	if !work.QueueConsulted {
		t.Error("QueueConsulted = false while the queue plainly answered")
	}
}

// TestStallStrandedIgnoresAQueuedBranchInAnotherRepo. A branch NAME is not
// unique across repositories, and a same-named branch queued elsewhere must not
// answer for this one — that direction is the dangerous one, because it
// SUPPRESSES the submit line for a branch nobody has actually submitted.
func TestStallStrandedIgnoresAQueuedBranchInAnotherRepo(t *testing.T) {
	r := newStrandRepo(t)
	r.git("checkout", "-q", "-b", "polecat-pa19a", "main")
	r.commit("fix.go", "fix: work (mg-a19a)")
	r.git("push", "-q", "origin", "polecat-pa19a")
	r.git("checkout", "-q", "main")

	// A DIFFERENT repository, with a branch of the same name queued in it.
	other := newStrandRepo(t)
	other.git("checkout", "-q", "-b", "polecat-pa19a", "main")
	other.commit("elsewhere.go", "fix: an unrelated repo's branch of the same name")
	other.git("push", "-q", "origin", "polecat-pa19a")
	other.git("checkout", "-q", "main")

	queue := refineryWith(t, refinery.MergeRequest{
		RepoPath:  other.dir,
		Branch:    "polecat-pa19a",
		TargetRef: "main",
		Author:    "mg-a19a",
	})

	work, known := newStallStranded(queue).Branches([]stallwatch.StrandedItem{{ID: "mg-a19a", Repo: r.dir}})
	if !known {
		t.Fatal("known=false")
	}
	got := work.Items["mg-a19a"]
	if len(got) != 1 {
		t.Fatalf("branches for mg-a19a = %+v, want exactly one", got)
	}
	if got[0].Queued != nil {
		t.Errorf("another repository's queued branch answered for this one: %+v — the submit line "+
			"this suppresses is the remedy for work that is genuinely sitting there", got[0].Queued)
	}
}

// TestStallStrandedSaysWhenTheQueueWasNotConsulted. With no refinery — disabled
// in config, or replaced between the thunk and the call — every branch comes
// back unqueued, which is exactly what an empty queue looks like. mg-8baa's
// collapse, and here it would be handed to the reader as a submit against a
// running merge.
func TestStallStrandedSaysWhenTheQueueWasNotConsulted(t *testing.T) {
	r := newStrandRepo(t)
	r.git("checkout", "-q", "-b", "polecat-pa19a", "main")
	r.commit("fix.go", "fix: work (mg-a19a)")
	r.git("push", "-q", "origin", "polecat-pa19a")
	r.git("checkout", "-q", "main")

	noQueue := func() *refinery.Refinery { return nil }
	work, known := newStallStranded(noQueue).Branches([]stallwatch.StrandedItem{{ID: "mg-a19a", Repo: r.dir}})
	if !known {
		t.Fatal("known=false — an unreadable queue must not discard the branch finding it qualifies")
	}
	if len(work.Items["mg-a19a"]) != 1 {
		t.Fatalf("the branch was dropped along with the queue answer: %+v", work.Items["mg-a19a"])
	}
	if work.QueueConsulted {
		t.Error("QueueConsulted = true with no refinery at all")
	}
	if !strings.Contains(work.Uncertain, "refinery queue") {
		t.Errorf("Uncertain = %q, want it to state that the queue was not asked", work.Uncertain)
	}
}

// TestQueuedForIsKeyedOnRepoAndBranch. A branch name is not unique across
// repositories, and the wrong-repo match is the dangerous direction: it
// SUPPRESSES the submit line for a branch nobody has submitted. The unclean
// spelling is the other half — filepath.Clean equality, so a trailing slash is
// the same repository and a different repository is not.
func TestQueuedForIsKeyedOnRepoAndBranch(t *testing.T) {
	inQueue := map[string][]refinery.MergeRequest{
		"polecat-pa19a": {{ID: "mr-dacudtqtjv1hjkm21420", RepoPath: "/Users/daniel/dev/pogo",
			Branch: "polecat-pa19a", Status: refinery.StatusQueued}},
	}
	q, ok := queuedFor(inQueue, "/Users/daniel/dev/pogo", "polecat-pa19a")
	if !ok || q.MR != "mr-dacudtqtjv1hjkm21420" {
		t.Fatalf("full path did not match: ok=%v q=%+v", ok, q)
	}
	if _, ok := queuedFor(inQueue, "/Users/daniel/dev/pogo", "polecat-pd788"); ok {
		t.Error("a different branch matched")
	}
	if _, ok := queuedFor(inQueue, "/Users/daniel/dev/onethird_program", "polecat-pa19a"); ok {
		t.Error("a different repository matched")
	}
	if _, ok := queuedFor(inQueue, "/Users/daniel/dev/pogo/", "polecat-pa19a"); !ok {
		t.Error("a trailing slash was treated as a different repository")
	}
}

// TestQueuedForCarriesTheStatusVerbatim. "queued" and "processing" both mean
// do-not-resubmit and are printed unchanged anyway, because they are different
// answers to how long the item has to be left alone.
func TestQueuedForCarriesTheStatusVerbatim(t *testing.T) {
	for _, status := range []refinery.MergeStatus{refinery.StatusQueued, refinery.StatusProcessing} {
		inQueue := map[string][]refinery.MergeRequest{
			"polecat-pa19a": {{ID: "mr-x", RepoPath: "/repo", Branch: "polecat-pa19a", Status: status}},
		}
		q, ok := queuedFor(inQueue, "/repo", "polecat-pa19a")
		if !ok {
			t.Fatalf("status %q did not match", status)
		}
		if q.Status != string(status) {
			t.Errorf("Status = %q, want %q verbatim", q.Status, status)
		}
	}
}

// TestRefineryQueueByBranchSaysUnknownWithNoRefinery, and the positive control
// beside it: without the control, a lookup that answered "not consulted" to
// everything would pass the negative assertion and look like a working guard.
func TestRefineryQueueByBranchSaysUnknownWithNoRefinery(t *testing.T) {
	if _, ok := refineryQueueByBranch(nil); ok {
		t.Error("a nil thunk reported the queue as consulted")
	}
	if _, ok := refineryQueueByBranch(func() *refinery.Refinery { return nil }); ok {
		t.Error("a thunk over a nil refinery reported the queue as consulted")
	}
	if _, ok := refineryQueueByBranch(emptyRefinery(t)); !ok {
		t.Error("a real refinery reported the queue as unconsulted — the instrument answers " +
			"'not consulted' to everything and the negatives above say nothing")
	}
}
