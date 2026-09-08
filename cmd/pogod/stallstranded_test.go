package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/drellem2/pogo/internal/stallwatch"
)

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

	work, known := newStallStranded().Branches([]stallwatch.StrandedItem{{ID: "mg-a932", Repo: r.dir}})
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

	work, known := newStallStranded().Branches([]stallwatch.StrandedItem{{ID: "mg-a932", Repo: r.dir}})
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

	work, known := newStallStranded().Branches([]stallwatch.StrandedItem{{ID: "mg-a932", Repo: r.dir}})
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

	work, known := newStallStranded().Branches([]stallwatch.StrandedItem{{ID: "mg-a932", Repo: missing}})
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
	work, known := newStallStranded().Branches([]stallwatch.StrandedItem{{ID: "mg-a932"}})
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
	work, known := newStallStranded().Branches(nil)
	if !known {
		t.Fatal("known=false on an empty population")
	}
	if len(work.Items) != 0 || work.Uncertain != "" {
		t.Errorf("work = %+v, want empty", work)
	}
}
